package core

import (
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/pocketbase/dbx"
)

// Shared cache versions let instances that use the same database notice
// collection and settings changes made by the others (see cache_sync.go).
//
// A version row is bumped whenever the collections or the settings change.
// Bumps made inside a transaction are only recorded on the transaction and
// written once, as the last statements of the outermost transaction, so the
// version row lock is always taken last and never in parallel with another
// lock order.
const (
	cacheVersionsTable = "_cacheVersions"

	cacheVersionCollections = "collections"
	cacheVersionSettings    = "settings"

	// cacheNotifyChannel is the postgres channel used as wake-up signal.
	// The payload is only informative, receivers always re-read the versions.
	cacheNotifyChannel = "pb_cache"
)

// InitCacheVersions creates the shared cache versions table.
func InitCacheVersions(app App) error {
	if asBaseApp(app).SQLDialect() == DialectMySQL {
		return ExecuteSchema(app, "CREATE TABLE IF NOT EXISTS {{_cacheVersions}} ([[name]] VARCHAR(64) NOT NULL PRIMARY KEY, [[version]] BIGINT NOT NULL DEFAULT 0, [[updated_at]] DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6))")
	}
	return ExecuteSchema(app, "CREATE TABLE IF NOT EXISTS {{_cacheVersions}} ([[name]] VARCHAR(64) NOT NULL PRIMARY KEY, [[version]] BIGINT NOT NULL DEFAULT 0, [[updated_at]] TIMESTAMPTZ NOT NULL DEFAULT NOW())")
}

// bumpCacheVersion records that the named shared cache changed.
//
// Inside a transaction the bump is deferred to the end of the outermost
// transaction (so it also never happens for a transaction that fails
// before it gets there). Outside of a transaction it is written directly.
//
// Errors are logged: a lost bump is repaired by the slow reconcile of the
// cache sync (settings bumps never fail the write that caused them; collections
// bumps do, see flushCacheVersions).
func (app *BaseApp) bumpCacheVersion(name string) {
	if app.txInfo != nil {
		if app.txInfo.isForAuxDB {
			return
		}
		app.txInfo.markCacheDirty(name)
		return
	}

	if err := app.writeCacheVersions(app.NonconcurrentDB(), []string{name}); err != nil {
		app.Logger().Warn("Failed to bump the shared cache version", "name", name, "error", err)
	}
}

// writeCacheVersions increments the named versions (sorted, so concurrent
// writers always lock the rows in the same order) and wakes the other
// instances. It does nothing when the table doesn't exist yet
// (e.g. while the system migrations are still running).
func (app *BaseApp) writeCacheVersions(db dbx.Builder, names []string) error {
	if len(names) == 0 {
		return nil
	}

	// a failing statement aborts a postgres transaction, so the check never
	// runs on the transaction itself
	if !app.cacheVersionsTableExists() {
		return nil
	}

	names = slices.Clone(names)
	slices.Sort(names)

	mysql := app.SQLDialect() == DialectMySQL

	for _, name := range names {
		var q string
		if mysql {
			q = "INSERT INTO {{_cacheVersions}} ([[name]], [[version]], [[updated_at]]) VALUES ({:name}, 1, CURRENT_TIMESTAMP(6)) " +
				"ON DUPLICATE KEY UPDATE [[version]] = [[version]] + 1, [[updated_at]] = CURRENT_TIMESTAMP(6)"
		} else {
			q = "INSERT INTO {{_cacheVersions}} ([[name]], [[version]], [[updated_at]]) VALUES ({:name}, 1, NOW()) " +
				"ON CONFLICT ([[name]]) DO UPDATE SET [[version]] = {{_cacheVersions}}.[[version]] + 1, [[updated_at]] = NOW()"
		}
		if _, err := db.NewQuery(q).Bind(dbx.Params{"name": name}).Execute(); err != nil {
			return err
		}
	}

	if !mysql {
		// delivered on commit and discarded on rollback
		if _, err := db.NewQuery("SELECT pg_notify({:channel}, {:payload})").
			Bind(dbx.Params{"channel": cacheNotifyChannel, "payload": names[0]}).
			Execute(); err != nil {
			return err
		}
	}

	return nil
}

// cacheVersionsTableExists reports whether the shared versions table exists
// (it doesn't while the system migrations are still running). The probe uses
// the root app's connection pool, never an open transaction, and a positive
// answer is remembered.
func (app *BaseApp) cacheVersionsTableExists() bool {
	cs := app.cacheSync
	if cs != nil && cs.tableReady.Load() {
		return true
	}

	root := app
	for root.txInfo != nil && root.txInfo.parent != nil {
		root = root.txInfo.parent
	}

	exists := root.hasTable(root.ConcurrentDB(), cacheVersionsTable)
	if exists && cs != nil {
		cs.tableReady.Store(true)
	}

	return exists
}

// markCacheDirty records a pending shared cache version bump.
func (a *TxAppInfo) markCacheDirty(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.dirtyCaches == nil {
		a.dirtyCaches = map[string]struct{}{}
	}
	a.dirtyCaches[name] = struct{}{}
}

func (a *TxAppInfo) takeDirtyCaches() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.dirtyCaches) == 0 {
		return nil
	}

	names := make([]string, 0, len(a.dirtyCaches))
	for name := range a.dirtyCaches {
		names = append(names, name)
	}
	a.dirtyCaches = nil

	return names
}

// flushCacheVersions writes the pending bumps inside the transaction,
// as the last statements before it commits.
//
// A failed settings bump never fails the write that caused it (the other
// instances then repair their settings with the periodic reconcile): the bump
// runs under a savepoint, since a failed statement would otherwise abort a
// postgres transaction. A failed collections bump (or savepoint) does fail it,
// together with the scope rewrite of a rename: a collection change must not
// commit without telling the other instances that their cache is behind.
func (app *BaseApp) flushCacheVersions(tx dbx.Builder, names []string) error {
	if len(names) == 0 {
		return nil
	}

	const savepoint = "pb_cache_bump"

	if _, err := tx.NewQuery("SAVEPOINT " + savepoint).Execute(); err != nil {
		if errors.Is(err, sql.ErrTxDone) {
			return nil // already ended by a callee
		}

		// the transaction is most likely unusable and fails on its own
		app.Logger().Warn("Failed to create the cache bump savepoint", "error", err)
		if slices.Contains(names, cacheVersionCollections) {
			return err // a collection change must not commit without its bump
		}
		return nil
	}

	if err := app.writeCacheVersions(tx, names); err != nil {
		app.Logger().Warn("Failed to bump the shared cache versions", "names", names, "error", err)

		// a collection change that commits without its version bump (and the
		// scope rewrite that goes with a rename) would hide from the other
		// instances that their cache is behind: the whole transaction fails.
		// A settings bump is repaired by the reconcile instead.
		if slices.Contains(names, cacheVersionCollections) {
			return err
		}

		if _, rbErr := tx.NewQuery("ROLLBACK TO SAVEPOINT " + savepoint).Execute(); rbErr != nil {
			return errors.Join(err, rbErr)
		}
	}

	if _, err := tx.NewQuery("RELEASE SAVEPOINT " + savepoint).Execute(); err != nil {
		app.Logger().Debug("Failed to release the cache bump savepoint", "error", err)
	}

	return nil
}

// BumpCacheVersionsForQuery bumps the shared cache versions that a raw SQL
// write statement may have changed (raw statements bypass the model hooks).
func (app *BaseApp) BumpCacheVersionsForQuery(query string) {
	q := strings.ToLower(query)

	if strings.Contains(q, "_collections") {
		app.bumpCacheVersion(cacheVersionCollections)
	}
	if strings.Contains(q, "_params") {
		app.bumpCacheVersion(cacheVersionSettings)
	}
}
