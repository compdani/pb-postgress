package core

// RunSatelliteCascade preserves the legacy API while using the instance transaction.
func (app *BaseApp) RunSatelliteCascade(fn func(txApp App) error) error {
	return app.RunInTransaction(fn)
}

// RunSatelliteCascade executes relation maintenance atomically on the instance DB.
func RunSatelliteCascade(app App, fn func(txApp App) error) error {
	return app.RunInTransaction(fn)
}

// RunInPostgresTransaction is retained for source compatibility.
// Deprecated: use RunInTransaction; there is no separate PostgreSQL connection.
func (app *BaseApp) RunInPostgresTransaction(fn func(txApp App) error) error {
	return app.RunInTransaction(fn)
}

// PostgresTxInfo is retained for source compatibility.
// Deprecated: use TxInfo; all operations share the instance transaction.
func (app *BaseApp) PostgresTxInfo() *TxAppInfo { return app.TxInfo() }
