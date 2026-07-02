package core

import (
	"errors"

	"github.com/pocketbase/dbx"
)

// RunSatelliteCascade runs fn in a Postgres transaction when satellites are Postgres-backed.
func (app *BaseApp) RunSatelliteCascade(fn func(txApp App) error) error {
	if app.HasPostgres() {
		return app.RunInPostgresTransaction(fn)
	}
	return fn(app)
}

// RunSatelliteCascade runs fn in a Postgres transaction when satellites are Postgres-backed.
func RunSatelliteCascade(app App, fn func(txApp App) error) error {
	if ba := asBaseApp(app); ba != nil {
		return ba.RunSatelliteCascade(fn)
	}
	return fn(app)
}

// RunInPostgresTransaction wraps fn in a PostgreSQL transaction.
func (app *BaseApp) RunInPostgresTransaction(fn func(txApp App) error) error {
	if !app.HasPostgres() {
		return errors.New("postgres is not configured")
	}

	return app.runInPostgresTransaction(app.PostgresNonconcurrentDB(), fn)
}

func (app *BaseApp) runInPostgresTransaction(db dbx.Builder, fn func(txApp App) error) error {
	switch txOrDB := db.(type) {
	case *dbx.Tx:
		return fn(app)
	case *dbx.DB:
		var txApp *BaseApp
		txErr := txOrDB.Transactional(func(tx *dbx.Tx) error {
			txApp = app.createPostgresTxApp(tx)
			return fn(txApp)
		})

		if txApp != nil && txApp.postgresTxInfo != nil {
			afterFuncErr := txApp.postgresTxInfo.runAfterFuncs(txErr)
			if afterFuncErr != nil {
				return errors.Join(txErr, afterFuncErr)
			}
		}

		return txErr
	default:
		return errors.New("failed to start postgres transaction (unknown db type)")
	}
}

func (app *BaseApp) createPostgresTxApp(tx *dbx.Tx) *BaseApp {
	clone := *app
	clone.postgresConcurrentDB = tx
	clone.postgresNonconcurrentDB = tx
	clone.postgresTxInfo = &TxAppInfo{
		parent:          app,
		isForPostgresDB: true,
	}
	return &clone
}

// PostgresTxInfo returns the active PostgreSQL transaction info (if any).
func (app *BaseApp) PostgresTxInfo() *TxAppInfo {
	return app.postgresTxInfo
}
