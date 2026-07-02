package core

import (
	"fmt"

	"github.com/pocketbase/dbx"
)

// HasPostgres reports whether a PostgreSQL connection is configured and open.
func (app *BaseApp) HasPostgres() bool {
	return app != nil && app.postgresNonconcurrentDB != nil
}

// PostgresConfig returns the loaded PostgreSQL configuration.
func (app *BaseApp) PostgresConfig() PostgresConfig {
	if app == nil {
		return PostgresConfig{}
	}
	return app.postgresConfig
}

// PostgresConcurrentDB returns the concurrent PostgreSQL builder instance.
func (app *BaseApp) PostgresConcurrentDB() dbx.Builder {
	return app.postgresConcurrentDB
}

// PostgresNonconcurrentDB returns the nonconcurrent PostgreSQL builder instance.
func (app *BaseApp) PostgresNonconcurrentDB() dbx.Builder {
	return app.postgresNonconcurrentDB
}

// IsPostgresBacked reports whether collection record data is stored in PostgreSQL.
func (app *BaseApp) IsPostgresBacked(c *Collection) bool {
	if app == nil || c == nil || !app.HasPostgres() {
		return false
	}

	if c.UsesPostgresRecords() {
		return true
	}

	if c.IsExternal() {
		return true
	}

	return isPostgresSatelliteCollection(c.Name)
}

// ManagesPostgresRecordSchema reports whether PocketBase should create/sync the physical Postgres table.
func (app *BaseApp) ManagesPostgresRecordSchema(c *Collection) bool {
	if app == nil || c == nil || !app.IsPostgresBacked(c) || c.IsExternal() {
		return false
	}

	return c.UsesPostgresRecords() || isPostgresSatelliteCollection(c.Name)
}

// RecordReadDB returns the db builder used for record reads.
func (app *BaseApp) RecordReadDB(c *Collection) dbx.Builder {
	if app.IsPostgresBacked(c) {
		return app.PostgresConcurrentDB()
	}
	return app.ConcurrentDB()
}

// RecordWriteDB returns the db builder used for record writes.
func (app *BaseApp) RecordWriteDB(c *Collection) dbx.Builder {
	if app.IsPostgresBacked(c) {
		return app.PostgresNonconcurrentDB()
	}
	return app.NonconcurrentDB()
}

// RecordTable returns the SQL table reference for the collection records.
func (app *BaseApp) RecordTable(c *Collection) string {
	if !app.IsPostgresBacked(c) {
		return c.Name
	}

	schema := c.PostgresSchemaName(app)
	table := c.PostgresTableName(app)

	return fmt.Sprintf(`"%s"."%s"`, schema, table)
}

func modelWriteDB(app App, model Model, isForAuxDB bool) dbx.Builder {
	if isForAuxDB {
		return app.AuxNonconcurrentDB()
	}

	if ba := asBaseApp(app); ba != nil {
		if record, ok := model.(*Record); ok && record.Collection() != nil {
			return ba.RecordWriteDB(record.Collection())
		}
	}

	return app.NonconcurrentDB()
}

func modelTableName(app App, model Model) string {
	if ba := asBaseApp(app); ba != nil {
		if record, ok := model.(*Record); ok && record.Collection() != nil && ba.IsPostgresBacked(record.Collection()) {
			return ba.RecordTable(record.Collection())
		}
	}

	return model.TableName()
}

// BaseAppAccessor is implemented by App wrappers that can expose their underlying BaseApp.
type BaseAppAccessor interface {
	UnsafeUnwrapBaseApp() *BaseApp
}

// AsBaseApp returns the underlying BaseApp instance, including from App wrappers.
func AsBaseApp(app App) *BaseApp {
	return asBaseApp(app)
}

func (app *BaseApp) UnsafeUnwrapBaseApp() *BaseApp {
	return app
}

func asBaseApp(app App) *BaseApp {
	if ba, ok := app.(*BaseApp); ok {
		return ba
	}
	if a, ok := app.(BaseAppAccessor); ok {
		return a.UnsafeUnwrapBaseApp()
	}
	return nil
}
