package core_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/types"

	_ "github.com/pocketbase/pocketbase/migrations"
)

// two instances (own data dir, one shared database)
func newTwoInstances(t *testing.T) (*core.BaseApp, *core.BaseApp) {
	t.Helper()
	return newTwoInstancesWith(t, nil)
}

// prepareB runs on the second instance right after its bootstrap, before the cache sync starts
func newTwoInstancesWith(t *testing.T, prepareB func(*core.BaseApp)) (*core.BaseApp, *core.BaseApp) {
	t.Helper()

	oldInterval := core.CacheSyncInterval
	core.CacheSyncInterval = 40 * time.Millisecond
	t.Cleanup(func() { core.CacheSyncInterval = oldInterval })

	cfgA := tests.NewDatabaseConfig(t)
	a := core.NewBaseApp(cfgA)
	if err := a.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.ClearBootstrap() })

	cfgB := cfgA
	cfgB.DataDir = t.TempDir()
	b := core.NewBaseApp(cfgB)
	if prepareB != nil {
		b.OnBootstrap().Bind(&hook.Handler[*core.BootstrapEvent]{
			Func: func(e *core.BootstrapEvent) error {
				if err := e.Next(); err != nil {
					return err
				}
				prepareB(b)
				return nil
			},
			Priority: -996, // after the bootstrap, before the cache sync starts (-997 continues)
		})
	}
	if err := b.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.ClearBootstrap() })

	return a, b
}

// holdLocalCollectionTx starts a transaction on app that saves a collection
// and then blocks until released. The test cleanup always releases it and
// waits for it to end, so it never outlives the app.
func holdLocalCollectionTx(t *testing.T, app *core.BaseApp, name string) (inTx <-chan struct{}, release func(), done <-chan error) {
	t.Helper()

	return holdTx(t, app, func(tx core.App) error {
		return tx.Save(core.NewBaseCollection(name))
	})
}

// holdTx runs fn in a transaction on app and then blocks until released.
func holdTx(t *testing.T, app *core.BaseApp, fn func(tx core.App) error) (inTx <-chan struct{}, release func(), done <-chan error) {
	t.Helper()

	reached := make(chan struct{})
	gate := make(chan struct{})
	result := make(chan error, 1)
	finished := make(chan struct{})

	go func() {
		defer close(finished)
		result <- app.RunInTransaction(func(tx core.App) error {
			if err := fn(tx); err != nil {
				return err
			}
			close(reached)
			<-gate
			return nil
		})
	}()

	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() {
		release()
		<-finished
	})

	return reached, release, result
}

// waitOrFail waits for the transaction to reach its blocking point, failing
// (instead of hanging) when it ended early.
func waitOrFail(t *testing.T, reached <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-reached:
	case err := <-done:
		t.Fatalf("the transaction ended before blocking: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the transaction")
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", what)
}

func cacheVersion(t *testing.T, app *core.BaseApp, name string) int64 {
	t.Helper()
	var v int64
	err := app.DB().NewQuery("SELECT [[version]] FROM {{_cacheVersions}} WHERE [[name]] = {:name}").
		Bind(map[string]any{"name": name}).Row(&v)
	if err != nil {
		return 0 // no row yet
	}
	return v
}

func TestCacheSyncCollectionChangeReachesOtherInstance(t *testing.T) {
	a, b := newTwoInstances(t)

	c := core.NewBaseCollection("sync_posts")
	c.Fields.Add(&core.TextField{Name: "title"})
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}

	eventually(t, "B sees the new collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("sync_posts")
		return err == nil
	})

	// tighten the rule on A
	rule := "@request.auth.id != ''"
	c.ListRule = &rule
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}

	eventually(t, "B enforces the new rule", func() bool {
		cached, err := b.FindCachedCollectionByNameOrId("sync_posts")
		return err == nil && cached.ListRule != nil && *cached.ListRule == rule
	})

	if err := a.Delete(c); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B drops the deleted collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("sync_posts")
		return err != nil
	})
}

func TestCacheSyncBumpsOncePerTransactionAndNotOnRollback(t *testing.T) {
	a, _ := newTwoInstances(t)

	before := cacheVersion(t, a, "collections")

	err := a.RunInTransaction(func(tx core.App) error {
		for _, name := range []string{"bump_one", "bump_two"} {
			c := core.NewBaseCollection(name)
			if err := tx.Save(c); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := cacheVersion(t, a, "collections"); got != before+1 {
		t.Fatalf("expected a single bump for the whole transaction, got %d -> %d", before, got)
	}

	rollback := errors.New("rollback")
	before = cacheVersion(t, a, "collections")
	err = a.RunInTransaction(func(tx core.App) error {
		if err := tx.Save(core.NewBaseCollection("bump_rolled_back")); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if got := cacheVersion(t, a, "collections"); got != before {
		t.Fatalf("a rolled back transaction must not bump (postgres): %d -> %d", before, got)
	}
}

func TestCacheSyncSettingsReachOtherInstance(t *testing.T) {
	a, b := newTwoInstances(t)

	settings := a.Settings()
	settings.Meta.AppName = "Synced Name"
	if err := a.Save(settings); err != nil {
		t.Fatal(err)
	}

	eventually(t, "B reloads the settings", func() bool {
		clone, err := b.Settings().Clone() // read under the settings lock
		return err == nil && clone.Meta.AppName == "Synced Name"
	})
}

func TestCacheSyncAuthSecretChangeInvalidatesOtherInstance(t *testing.T) {
	a, b := newTwoInstances(t)

	users := core.NewAuthCollection("sync_users")
	if err := a.Save(users); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B sees the auth collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("sync_users")
		return err == nil
	})

	var calls atomic.Int32
	var gotId, gotName atomic.Value
	b.Store().Set(core.StoreKeyAuthCollectionInvalidator, core.AuthCollectionInvalidatorFunc(
		func(_ core.App, id, name string) {
			gotId.Store(id)
			gotName.Store(name)
			calls.Add(1)
		},
	))

	// changing the auth rule rotates the token secret
	rule := "verified = true"
	users.AuthRule = &rule
	if err := a.Save(users); err != nil {
		t.Fatal(err)
	}

	eventually(t, "B invalidates the auth collection", func() bool { return calls.Load() > 0 })
	if gotId.Load() != users.Id || gotName.Load() != "sync_users" {
		t.Fatalf("unexpected invalidation: %v %v", gotId.Load(), gotName.Load())
	}

	// deleting it also invalidates
	calls.Store(0)
	if err := a.Delete(users); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B invalidates the deleted auth collection", func() bool { return calls.Load() > 0 })
}

func TestCacheSyncRenamedCollectionKeepsOtherInstanceScope(t *testing.T) {
	a, b := newTwoInstances(t)

	c := core.NewBaseCollection("sync_hidden")
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B sees the collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("sync_hidden")
		return err == nil
	})

	// B hides it
	if err := b.SetInstanceScope(core.InstanceScope{Mode: "deny", Collections: []string{"sync_hidden"}}); err != nil {
		t.Fatal(err)
	}

	c.Name = "sync_hidden_renamed"
	c.Updated = types.NowDateTime()
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}

	// the scope was rewritten by the same transaction as the rename
	scope, err := b.InstanceScope()
	_ = scope
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	id, _ := b.InstanceID()
	if err := b.DB().NewQuery("SELECT [[scopeCollections]] FROM {{_instances}} WHERE [[id]] = {:id}").
		Bind(map[string]any{"id": id}).Row(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != `["sync_hidden_renamed"]` {
		t.Fatalf("scope not carried over by the rename: %s", raw)
	}

	// and it never became visible on B
	eventually(t, "B has the new name cached", func() bool {
		cached, err := b.FindCachedCollectionByNameOrId("sync_hidden_renamed")
		return err == nil && !b.CollectionVisible(cached)
	})
}

func TestCacheSyncDoesNotEchoLocalAuthChange(t *testing.T) {
	a, _ := newTwoInstances(t)

	users := core.NewAuthCollection("echo_users")
	if err := a.Save(users); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	a.Store().Set(core.StoreKeyAuthCollectionInvalidator, core.AuthCollectionInvalidatorFunc(
		func(core.App, string, string) { calls.Add(1) },
	))

	rule := "verified = true"
	users.AuthRule = &rule // rotates the secret
	if err := a.Save(users); err != nil {
		t.Fatal(err)
	}

	// the local hooks already handled it: the sync must not unset the clients
	// that authenticated again since
	time.Sleep(500 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Fatalf("local change was echoed back %d time(s)", n)
	}
}

func TestCacheSyncDefersWhileLocalCollectionWriteInFlight(t *testing.T) {
	a, b := newTwoInstances(t)

	inTx, releaseTx, txDone := holdLocalCollectionTx(t, b, "deferred_local")
	waitOrFail(t, inTx, txDone)

	// another instance commits a change while B's transaction is still running
	if err := a.Save(core.NewBaseCollection("deferred_remote")); err != nil {
		t.Fatal(err)
	}

	// several sync cycles later B's in-transaction cache must not have been reverted
	time.Sleep(400 * time.Millisecond)
	if _, err := b.FindCachedCollectionByNameOrId("deferred_local"); err != nil {
		t.Fatalf("the sync reverted the cache of the local transaction: %v", err)
	}

	releaseTx()
	if err := <-txDone; err != nil {
		t.Fatal(err)
	}

	eventually(t, "B converges on both changes", func() bool {
		_, e1 := b.FindCachedCollectionByNameOrId("deferred_local")
		_, e2 := b.FindCachedCollectionByNameOrId("deferred_remote")
		return e1 == nil && e2 == nil
	})
}

func TestCacheSyncRolledBackAuthChangeKeepsBaseline(t *testing.T) {
	a, b := newTwoInstances(t)

	users := core.NewAuthCollection("baseline_users")
	if err := a.Save(users); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B sees the auth collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("baseline_users")
		return err == nil
	})

	var calls atomic.Int32
	b.Store().Set(core.StoreKeyAuthCollectionInvalidator, core.AuthCollectionInvalidatorFunc(
		func(core.App, string, string) { calls.Add(1) },
	))

	// B deletes it inside a transaction that rolls back
	local, err := b.FindCollectionByNameOrId("baseline_users")
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback")
	err = b.RunInTransaction(func(tx core.App) error {
		if err := tx.Delete(local); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}

	// the rolled back delete must not hide the real, later one made elsewhere
	if err := a.Delete(users); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B invalidates the clients of the deleted collection", func() bool { return calls.Load() > 0 })
}

func TestCacheSyncFailedReloadDoesNotSpin(t *testing.T) {
	var reloads atomic.Int32
	a, b := newTwoInstancesWith(t, func(b *core.BaseApp) {
		b.ConcurrentDB().(*dbx.DB).QueryLogFunc = func(_ context.Context, _ time.Duration, sql string, _ *sql.Rows, _ error) {
			if strings.Contains(sql, `FROM "_collections" ORDER BY`) {
				reloads.Add(1)
			}
		}
	})

	inTx, _, txDone := holdLocalCollectionTx(t, b, "spin_local")
	waitOrFail(t, inTx, txDone)

	if err := a.Save(core.NewBaseCollection("spin_remote")); err != nil {
		t.Fatal(err)
	}

	// the publication is deferred for the whole transaction: it is retried
	// once per poll, not in a tight loop
	reloads.Store(0)
	time.Sleep(600 * time.Millisecond)
	n := reloads.Load()
	if n == 0 {
		t.Fatal("the deferred publication was never retried (or the reload query was not observed)")
	}
	if n > 40 {
		t.Fatalf("deferred reload spins: %d collection reloads in 600ms", n)
	}
}

func TestCacheSyncReconcileRepairsLostBump(t *testing.T) {
	oldReconcile := core.CacheSyncReconcileInterval
	core.CacheSyncReconcileInterval = 150 * time.Millisecond
	t.Cleanup(func() { core.CacheSyncReconcileInterval = oldReconcile })

	a, b := newTwoInstances(t)

	c := core.NewBaseCollection("reconcile_posts")
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B sees the collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("reconcile_posts")
		return err == nil
	})

	// a raw write changes the rule without bumping the version or touching `updated`
	if _, err := a.DB().NewQuery("UPDATE {{_collections}} SET [[listRule]] = {:rule} WHERE [[id]] = {:id}").
		Bind(map[string]any{"rule": "@request.auth.id != ''", "id": c.Id}).Execute(); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the reconcile repairs the lost bump", func() bool {
		cached, err := b.FindCachedCollectionByNameOrId("reconcile_posts")
		return err == nil && cached.ListRule != nil && *cached.ListRule == "@request.auth.id != ''"
	})
}

func TestCacheSyncReconcileRepairsLostSecretBump(t *testing.T) {
	oldReconcile := core.CacheSyncReconcileInterval
	core.CacheSyncReconcileInterval = 150 * time.Millisecond
	t.Cleanup(func() { core.CacheSyncReconcileInterval = oldReconcile })

	a, b := newTwoInstances(t)

	users := core.NewAuthCollection("reconcile_secret_users")
	if err := a.Save(users); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B sees the collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("reconcile_secret_users")
		return err == nil
	})

	var calls atomic.Int32
	b.Store().Set(core.StoreKeyAuthCollectionInvalidator, core.AuthCollectionInvalidatorFunc(
		func(core.App, string, string) { calls.Add(1) },
	))

	// a raw write rotates the token secret: no bump, `updated` untouched
	// (the secret lives in the collection's JSON `options`)
	res, err := a.DB().NewQuery("UPDATE {{_collections}} SET [[options]] = REPLACE([[options]]::text, {:old}, {:new})::jsonb WHERE [[id]] = {:id}").
		Bind(map[string]any{"old": users.AuthToken.Secret, "new": strings.Repeat("x", len(users.AuthToken.Secret)), "id": users.Id}).Execute()
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("expected the raw rewrite to touch the collection, affected %d", n)
	}

	eventually(t, "the reconcile notices the secret-only change", func() bool { return calls.Load() > 0 })
}

func TestCacheSyncReconcileRepairsLostSettingsChange(t *testing.T) {
	oldReconcile := core.CacheSyncReconcileInterval
	core.CacheSyncReconcileInterval = 150 * time.Millisecond
	t.Cleanup(func() { core.CacheSyncReconcileInterval = oldReconcile })

	a, b := newTwoInstances(t)

	settings, err := a.Settings().Clone()
	if err != nil {
		t.Fatal(err)
	}
	settings.Meta.AppName = "Raw Changed"
	data, err := settings.DBExport(a)
	if err != nil {
		t.Fatal(err)
	}

	// a raw write: no bump (the stored `updated` does change)
	if _, err := a.DB().Update("_params", map[string]any{"value": data["value"], "updated": time.Now().UTC().Format("2006-01-02 15:04:05.000Z")}, dbx.HashExp{"id": "settings"}).Execute(); err != nil {
		t.Fatal(err)
	}

	eventually(t, "B reloads the settings changed without a bump", func() bool {
		clone, err := b.Settings().Clone()
		return err == nil && clone.Meta.AppName == "Raw Changed"
	})
}

func TestCacheSyncReconcileRepairsValueOnlySettingsChange(t *testing.T) {
	oldReconcile := core.CacheSyncReconcileInterval
	core.CacheSyncReconcileInterval = 150 * time.Millisecond
	t.Cleanup(func() { core.CacheSyncReconcileInterval = oldReconcile })

	a, b := newTwoInstances(t)

	settings, err := a.Settings().Clone()
	if err != nil {
		t.Fatal(err)
	}
	settings.Meta.AppName = "Value Only"
	data, err := settings.DBExport(a)
	if err != nil {
		t.Fatal(err)
	}

	// a raw write that changes the value but NOT the `updated` column and does not bump
	if _, err := a.DB().Update("_params", map[string]any{"value": data["value"]}, dbx.HashExp{"id": "settings"}).Execute(); err != nil {
		t.Fatal(err)
	}

	eventually(t, "B reloads settings changed without touching updated", func() bool {
		clone, err := b.Settings().Clone()
		return err == nil && clone.Meta.AppName == "Value Only"
	})
}

func TestCacheSyncRolledBackSettingsAreRestored(t *testing.T) {
	oldReconcile := core.CacheSyncReconcileInterval
	core.CacheSyncReconcileInterval = 150 * time.Millisecond
	t.Cleanup(func() { core.CacheSyncReconcileInterval = oldReconcile })

	_, b := newTwoInstances(t)

	committed, err := b.Settings().Clone()
	if err != nil {
		t.Fatal(err)
	}
	original := committed.Meta.AppName

	rollback := errors.New("rollback")
	err = b.RunInTransaction(func(tx core.App) error {
		s, err := tx.Settings().Clone()
		if err != nil {
			return err
		}
		s.Meta.AppName = "ROLLED BACK"
		if err := tx.Save(s); err != nil {
			return err
		}
		if err := tx.ReloadSettings(); err != nil { // loads the uncommitted value
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}

	eventually(t, "the committed settings are restored after the rollback", func() bool {
		clone, err := b.Settings().Clone()
		return err == nil && clone.Meta.AppName == original
	})
}

func TestCacheSyncDenyScopeNeverShowsRenamedCollectionWhileCacheIsBehind(t *testing.T) {
	a, b := newTwoInstances(t)

	c := core.NewBaseCollection("deny_old_name")
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B sees the collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("deny_old_name")
		return err == nil
	})
	if err := b.SetInstanceScope(core.InstanceScope{Mode: "deny", Collections: []string{"deny_old_name"}}); err != nil {
		t.Fatal(err)
	}
	// let the sync settle on the scope that matches the collections
	time.Sleep(300 * time.Millisecond)

	cached, err := b.FindCachedCollectionByNameOrId("deny_old_name")
	if err != nil {
		t.Fatal(err)
	}
	if b.CollectionVisible(cached) {
		t.Fatal("precondition: the collection must be hidden on B")
	}

	// B holds a local collection write, so the publication of what A changes is deferred
	inTx, release, txDone := holdLocalCollectionTx(t, b, "deny_local")
	waitOrFail(t, inTx, txDone)

	c.Name = "deny_new_name"
	if err := a.Save(c); err != nil { // rewrites the deny list in the same transaction
		t.Fatal(err)
	}

	// a request that resolved the collection before anything changed keeps this object
	retained := cached

	assertHidden := func(phase string) {
		if b.CollectionVisible(retained) {
			t.Fatalf("%s: a collection that B denies became visible (retained object)", phase)
		}
		if stale, err := b.FindCachedCollectionByNameOrId("deny_old_name"); err == nil && b.CollectionVisible(stale) {
			t.Fatalf("%s: a collection that B denies became visible while B's collections cache was behind", phase)
		}
	}

	// B's scope cache expires and loads the NEW deny list while its collections
	// cache still names the collection "deny_old_name"
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := b.FindCachedCollectionByNameOrId("deny_old_name"); err != nil {
			t.Fatalf("the cache changed while the publication was deferred: %v", err)
		}
		assertHidden("deferred")
		time.Sleep(50 * time.Millisecond)
	}

	// the cache catches up: never visible on the way, hidden under the new name too
	release()
	if err := <-txDone; err != nil {
		t.Fatal(err)
	}
	published := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !published {
		assertHidden("catching up")
		if renamed, err := b.FindCachedCollectionByNameOrId("deny_new_name"); err == nil {
			if b.CollectionVisible(renamed) {
				t.Fatal("the renamed collection must stay hidden on B")
			}
			published = true
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !published {
		t.Fatal("B never published the rename")
	}

	// and the object retained from before stays hidden for the rest of the grace period
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		assertHidden("after the publication")
	}
}

func TestCacheSyncBackToBackPublicationsKeepTheGraceOfTheFirst(t *testing.T) {
	a, b := newTwoInstances(t)

	c := core.NewBaseCollection("grace_old_name")
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B sees the collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("grace_old_name")
		return err == nil
	})
	if err := b.SetInstanceScope(core.InstanceScope{Mode: "deny", Collections: []string{"grace_old_name"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	retained, err := b.FindCachedCollectionByNameOrId("grace_old_name")
	if err != nil {
		t.Fatal(err)
	}

	c.Name = "grace_new_name"
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B publishes the rename", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("grace_new_name")
		return err == nil
	})

	// an unrelated change publishes again right after the first one
	if err := a.Save(core.NewBaseCollection("grace_unrelated")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B publishes the second change", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("grace_unrelated")
		return err == nil
	})

	for end := time.Now().Add(4 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if b.CollectionVisible(retained) {
			t.Fatal("the second publication discarded the grace of the first")
		}
	}
}

func TestCacheSyncSwitchToDenyDoesNotExposeRenamedHiddenCollection(t *testing.T) {
	a, b := newTwoInstances(t)

	other := core.NewBaseCollection("mode_other")
	hidden := core.NewBaseCollection("mode_old_name")
	for _, c := range []*core.Collection{other, hidden} {
		if err := a.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "B sees both", func() bool {
		_, e1 := b.FindCachedCollectionByNameOrId("mode_other")
		_, e2 := b.FindCachedCollectionByNameOrId("mode_old_name")
		return e1 == nil && e2 == nil
	})

	// B only allows "mode_other": "mode_old_name" is hidden
	if err := b.SetInstanceScope(core.InstanceScope{Mode: "allow", Collections: []string{"mode_other"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	retained, err := b.FindCachedCollectionByNameOrId("mode_old_name")
	if err != nil {
		t.Fatal(err)
	}
	if b.CollectionVisible(retained) {
		t.Fatal("precondition: hidden under the allow scope")
	}

	// B holds a local write on a collection it can see (no scope rewrite), so
	// the publication of what A changes is deferred
	inTx, releaseSwitch, txDone := holdTx(t, b, func(tx core.App) error {
		c, err := tx.FindCollectionByNameOrId("mode_other")
		if err != nil {
			return err
		}
		c.Fields.Add(&core.TextField{Name: "extra"})
		return tx.Save(c)
	})
	waitOrFail(t, inTx, txDone)

	hidden.Name = "mode_new_name"
	if err := a.Save(hidden); err != nil {
		t.Fatal(err)
	}

	// meanwhile the scope of B changes to a deny scope that no longer names the old collection
	id, err := b.InstanceID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB().NewQuery("UPDATE {{_instances}} SET [[scopeMode]] = 'deny', [[scopeCollections]] = {:names} WHERE [[id]] = {:id}").
		Bind(map[string]any{"names": `["mode_new_name"]`, "id": id}).Execute(); err != nil {
		t.Fatal(err)
	}

	for end := time.Now().Add(7 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if b.CollectionVisible(retained) {
			t.Fatal("switching to a deny scope exposed a hidden collection while B's cache was behind")
		}
	}

	// catch-up: the rename is published and the collection is still hidden (deny "mode_new_name")
	releaseSwitch()
	if err := <-txDone; err != nil {
		t.Fatal(err)
	}
	eventually(t, "B publishes the rename", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("mode_new_name")
		return err == nil
	})
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if b.CollectionVisible(retained) {
			t.Fatal("the collection must stay hidden after the cache caught up")
		}
	}
}

func TestCacheSyncNewlyDeniedWhilePublicationDeferredStaysHiddenThroughRename(t *testing.T) {
	a, b := newTwoInstances(t)

	c := core.NewBaseCollection("late_old_name")
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B sees the collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("late_old_name")
		return err == nil
	})
	time.Sleep(300 * time.Millisecond)

	retained, err := b.FindCachedCollectionByNameOrId("late_old_name")
	if err != nil {
		t.Fatal(err)
	}

	// B holds a local write: whatever A changes is not published on B for now
	inTx, _, txDone := holdLocalCollectionTx(t, b, "late_local")
	waitOrFail(t, inTx, txDone)

	// an unrelated change from A: B's publication is pending
	if err := a.Save(core.NewBaseCollection("late_unrelated")); err != nil {
		t.Fatal(err)
	}

	// B's scope is switched, from outside, to deny the collection: it was never denied before
	id, err := b.InstanceID()
	if err != nil {
		t.Fatal(err)
	}
	setScope := func(names string) {
		if _, err := a.DB().NewQuery("UPDATE {{_instances}} SET [[scopeMode]] = 'deny', [[scopeCollections]] = {:names} WHERE [[id]] = {:id}").
			Bind(map[string]any{"names": names, "id": id}).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	setScope(`["late_old_name"]`)
	eventually(t, "the denial is enforced", func() bool { return !b.CollectionVisible(retained) })

	// A renames it, which rewrites the deny list of every instance that denies it
	c.Name = "late_new_name"
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}

	for end := time.Now().Add(7 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if b.CollectionVisible(retained) {
			t.Fatal("a collection denied while the publication was pending became visible after the rename")
		}
	}
}

func TestCacheSyncTransactionScopeReadDoesNotChangeTheGuard(t *testing.T) {
	a, b := newTwoInstances(t)

	c := core.NewBaseCollection("guard_old_name")
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B sees the collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("guard_old_name")
		return err == nil
	})
	if err := b.SetInstanceScope(core.InstanceScope{Mode: "deny", Collections: []string{"guard_old_name"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	// a rolled back transaction that opened the scope up and read it
	rollback := errors.New("rollback")
	err := b.RunInTransaction(func(tx core.App) error {
		if err := tx.(*core.BaseApp).SetInstanceScope(core.InstanceScope{Mode: "deny"}); err != nil {
			return err
		}
		if _, err := tx.(*core.BaseApp).InstanceScope(); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}

	inTx, _, txDone := holdLocalCollectionTx(t, b, "guard_local")
	waitOrFail(t, inTx, txDone)

	c.Name = "guard_new_name"
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		stale, err := b.FindCachedCollectionByNameOrId("guard_old_name")
		if err != nil {
			t.Fatalf("the cache changed while deferred: %v", err)
		}
		if b.CollectionVisible(stale) {
			t.Fatal("the uncommitted scope of a rolled back transaction weakened the guard")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestCacheSyncRenamingInstanceKeepsItsOwnScope(t *testing.T) {
	a, _ := newTwoInstances(t)

	hidden := core.NewBaseCollection("own_hidden")
	visible := core.NewBaseCollection("own_visible")
	for _, c := range []*core.Collection{hidden, visible} {
		if err := a.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.SetInstanceScope(core.InstanceScope{Mode: "deny", Collections: []string{"own_hidden"}}); err != nil {
		t.Fatal(err)
	}

	visible.Name = "own_visible_renamed"
	if err := a.Save(visible); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h, err := a.FindCachedCollectionByNameOrId("own_hidden")
		if err != nil || a.CollectionVisible(h) {
			t.Fatalf("the hidden collection must stay hidden on the renaming instance (%v)", err)
		}
		v, err := a.FindCachedCollectionByNameOrId("own_visible_renamed")
		if err != nil || !a.CollectionVisible(v) {
			t.Fatalf("the renamed visible collection must stay visible on the renaming instance (%v)", err)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func TestCacheSyncLocalDenyAfterRemoteRenameIsImmediate(t *testing.T) {
	a, b := newTwoInstances(t)

	c := core.NewBaseCollection("local_old_name")
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B sees the collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("local_old_name")
		return err == nil
	})
	time.Sleep(300 * time.Millisecond)

	retained, err := b.FindCachedCollectionByNameOrId("local_old_name")
	if err != nil {
		t.Fatal(err)
	}

	// what A changes is not published on B while this is held
	release := b.HoldCollectionCachePublication()
	defer release()

	c.Name = "local_new_name"
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}

	// B denies the collection under its NEW name, which its cache doesn't know yet
	if err := b.SetInstanceScope(core.InstanceScope{Mode: "deny", Collections: []string{"local_new_name"}}); err != nil {
		t.Fatal(err)
	}
	if b.CollectionVisible(retained) {
		t.Fatal("a scope that B just saved did not take effect for a collection renamed elsewhere")
	}

	// and it stays that way once the rename is published
	release()
	eventually(t, "B publishes the rename", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("local_new_name")
		return err == nil
	})
	for end := time.Now().Add(1 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if b.CollectionVisible(retained) {
			t.Fatal("the denial was lost when the rename was published")
		}
	}
}

func TestSettingsReloadHookMaySaveSettings(t *testing.T) {
	a, _ := newTwoInstances(t)

	var saved atomic.Bool
	a.OnSettingsReload().BindFunc(func(e *core.SettingsReloadEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		if saved.CompareAndSwap(false, true) {
			// saving settings reloads them again from this very goroutine
			return e.App.Save(e.App.Settings())
		}
		return nil
	})

	done := make(chan error, 1)
	go func() { done <- a.ReloadSettings() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a settings reload hook that saves settings deadlocked")
	}
}

func TestSettingsReloadsNeverOverlapEvenWithSlowHooks(t *testing.T) {
	a, b := newTwoInstances(t)

	// a hook that keeps reading the settings for a while after they were loaded
	var slow atomic.Bool
	b.OnSettingsReload().BindFunc(func(e *core.SettingsReloadEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		if slow.Load() {
			for end := time.Now().Add(400 * time.Millisecond); time.Now().Before(end); time.Sleep(time.Millisecond) {
				_ = e.App.Settings().Meta.AppName
				_ = e.App.Settings().Logs.MaxDays
			}
		}
		return nil
	})
	slow.Store(true)

	// another instance changes the settings: B's cache sync reloads them (slowly)
	settings, err := a.Settings().Clone()
	if err != nil {
		t.Fatal(err)
	}
	settings.Meta.AppName = "Slow Hook Name"
	if err := a.Save(settings); err != nil {
		t.Fatal(err)
	}

	// while that reload is inside its hook, a local reload starts: it must wait,
	// not write the settings under the hook's reads (the race detector fails the test otherwise)
	time.Sleep(150 * time.Millisecond)
	if err := b.ReloadSettings(); err != nil {
		t.Fatal(err)
	}
	slow.Store(false)

	eventually(t, "B has the settings of A", func() bool {
		clone, err := b.Settings().Clone()
		return err == nil && clone.Meta.AppName == "Slow Hook Name"
	})
}
