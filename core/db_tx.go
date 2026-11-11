package core

import (
	"errors"
	"fmt"
	"sync"

	"github.com/pocketbase/dbx"
)

// RunInTransaction wraps fn into a transaction for the regular app database.
//
// It is safe to nest RunInTransaction calls as long as you use the callback's txApp.
func (app *BaseApp) RunInTransaction(fn func(txApp App) error) error {
	return app.runInTransaction(app.NonconcurrentDB(), fn, false)
}

// AuxRunInTransaction wraps fn into a transaction for the auxiliary app database.
//
// It is safe to nest RunInTransaction calls as long as you use the callback's txApp.
func (app *BaseApp) AuxRunInTransaction(fn func(txApp App) error) error {
	return app.runInTransaction(app.AuxNonconcurrentDB(), fn, true)
}

func (app *BaseApp) runInTransaction(db dbx.Builder, fn func(txApp App) error, isForAuxDB bool) error {
	switch txOrDB := db.(type) {
	case *dbx.Tx:
		// run as part of the already existing transaction
		return fn(app)
	case *dbx.DB:
		var txApp *BaseApp
		var dirtyCaches []string
		var realtimeGap bool

		// the transaction can touch collections in many places (imports,
		// cascades), so the cache sync must not publish a reload while it runs
		defer func() {
			if txApp != nil && txApp.txInfo != nil {
				txApp.txInfo.releaseCollectionWrite(app)
			}
		}()

		txErr := txOrDB.Transactional(func(tx *dbx.Tx) error {
			txApp = app.createTxApp(tx, isForAuxDB)
			if err := fn(txApp); err != nil {
				dirtyCaches = txApp.txInfo.takeDirtyCaches()
				return err
			}

			// the shared cache versions are written last, as the final statements
			// of the outermost transaction (see cache_versions.go)
			dirtyCaches = txApp.txInfo.takeDirtyCaches()
			if err := txApp.flushCacheVersions(tx, dirtyCaches); err != nil {
				return err
			}

			// the realtime events go last (see realtime_bus.go)
			if !isForAuxDB {
				events, overflow := txApp.txInfo.takeRealtimeEvents()
				gap, err := txApp.flushRealtimeEvents(tx, events, overflow)
				realtimeGap = gap
				return err
			}

			return nil
		})

		// events that could not be published (or were dropped): the other
		// instances must re-validate their clients
		if txErr == nil && realtimeGap {
			app.signalRealtimeGap()
		}

		// only a committed transaction changes what the others will see
		if txErr == nil && app.cacheSync != nil {
			app.cacheSync.applyBaselineChanges(txApp.txInfo.takeBaselineChanges())
		}

		// mysql DDL commits implicitly, so a failed transaction may still have
		// changed the schema; a spurious bump only costs the others a reload
		if txErr != nil && len(dirtyCaches) > 0 && app.SQLDialect() == DialectMySQL {
			if err := app.writeCacheVersions(app.NonconcurrentDB(), dirtyCaches); err != nil {
				app.Logger().Warn("Failed to bump the shared cache version after a failed transaction", "error", err)
			}
		}

		// execute all after event calls on transaction complete
		if txApp != nil && txApp.txInfo != nil {
			afterFuncErr := txApp.txInfo.runAfterFuncs(txErr)
			if afterFuncErr != nil {
				return errors.Join(txErr, afterFuncErr)
			}
		}

		return txErr
	default:
		return errors.New("failed to start transaction (unknown db type)")
	}
}

// createTxApp shallow clones the current app and assigns a new tx state.
func (app *BaseApp) createTxApp(tx *dbx.Tx, isForAuxDB bool) *BaseApp {
	clone := *app

	clone.concurrentDB = tx
	clone.nonconcurrentDB = tx
	clone.auxConcurrentDB = tx
	clone.auxNonconcurrentDB = tx

	clone.txInfo = &TxAppInfo{
		parent:     app,
		isForAuxDB: isForAuxDB,
	}

	// the transaction reads the scope itself and never shares its cache with
	// the parent; the parent's cache is dropped once the transaction ends
	txCache := &scopeCache{}
	clone.scopeCache = txCache
	clone.txInfo.OnComplete(func(error) error {
		// only when the transaction wrote the scope (its generation moved)
		if txCache.generation() > 0 {
			app.resetScopeCache()
		}
		return nil
	})

	return &clone
}

// TxAppInfo represents an active transaction context associated to an existing app instance.
type TxAppInfo struct {
	parent     *BaseApp
	afterFuncs []func(txErr error) error
	mu         sync.Mutex
	isForAuxDB bool

	// shared cache versions to bump when the transaction ends (see cache_versions.go)
	dirtyCaches map[string]struct{}

	// whether this transaction holds the cache sync's collection write lock
	collectionWriteHeld bool

	// local auth collection secret changes, applied to the cache sync's
	// baseline once the transaction commits
	baselineChanges []baselineChange

	// record events buffered for the realtime bus (see realtime_bus.go)
	realtimeEvents   []realtimeBusEvent
	realtimeIndex    map[string]struct{}
	realtimeNonAuth  int
	realtimeOverflow bool
}

// OnComplete registers the provided callback that will be invoked
// once the related transaction ends (either completes successfully or rollbacked with an error).
//
// The callback receives the transaction error (if any) as its argument.
// Any additional errors returned by the OnComplete callbacks will be
// joined together with txErr when returning the final transaction result.
func (a *TxAppInfo) OnComplete(fn func(txErr error) error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.afterFuncs = append(a.afterFuncs, fn)
}

// note: can be called only once because TxAppInfo is cleared
func (a *TxAppInfo) runAfterFuncs(txErr error) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	var errs []error

	for _, call := range a.afterFuncs {
		if err := call(txErr); err != nil {
			errs = append(errs, err)
		}
	}

	a.afterFuncs = nil

	if len(errs) > 0 {
		return fmt.Errorf("transaction afterFunc errors: %w", errors.Join(errs...))
	}

	return nil
}
