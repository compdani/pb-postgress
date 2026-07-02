package core

import (
	"fmt"

	"github.com/pocketbase/dbx"
)

// SyncPostgresRecordTableSchema creates or updates a PostgreSQL record table for managed collections.
func (app *BaseApp) SyncPostgresRecordTableSchema(newCollection *Collection, oldCollection *Collection) error {
	if newCollection == nil || newCollection.IsView() || !app.ManagesPostgresRecordSchema(newCollection) {
		return nil
	}

	tableRef := app.RecordTable(newCollection)

	if oldCollection == nil || !app.postgresTableExists(newCollection) {
		cols := make(map[string]string, len(newCollection.Fields))
		for _, field := range newCollection.Fields {
			cols[field.GetName()] = postgresColumnType(field)
		}

		parts := make([]string, 0, len(cols))
		for name, colType := range cols {
			parts = append(parts, fmt.Sprintf(`"%s" %s`, name, colType))
		}

		sql := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (%s)`, tableRef, joinComma(parts))
		_, err := app.PostgresNonconcurrentDB().NewQuery(sql).Execute()
		if err != nil {
			return err
		}

		return app.UpsertPostgresTableSchema(newCollection)
	}

	// For v1 keep existing tables unchanged after initial create.
	return nil
}

func (app *BaseApp) postgresTableExists(collection *Collection) bool {
	schema := collection.PostgresSchemaName(app)
	table := collection.PostgresTableName(app)

	var exists bool
	_ = app.PostgresConcurrentDB().NewQuery(`
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = {:schema} AND table_name = {:table}
		)
	`).
		Bind(dbx.Params{"schema": schema, "table": table}).
		Row(&exists)

	return exists
}

func postgresColumnType(field Field) string {
	switch field.Type() {
	case FieldTypeBool:
		return "BOOLEAN"
	case FieldTypeNumber:
		return "DOUBLE PRECISION"
	case FieldTypeJSON:
		return "JSONB"
	case FieldTypeDate:
		return "TIMESTAMPTZ"
	default:
		return "TEXT"
	}
}

func joinComma(items []string) string {
	if len(items) == 0 {
		return ""
	}

	result := items[0]
	for i := 1; i < len(items); i++ {
		result += ", " + items[i]
	}
	return result
}

// InitPostgresSatelliteTables ensures auth satellite tables exist in PostgreSQL.
func (app *BaseApp) InitPostgresSatelliteTables() error {
	if !app.HasPostgres() {
		return nil
	}

	for _, name := range postgresSatelliteCollectionNames {
		collection, err := app.FindCachedCollectionByNameOrId(name)
		if err != nil {
			continue
		}

		if err := app.SyncPostgresRecordTableSchema(collection, nil); err != nil {
			return fmt.Errorf("failed to init postgres satellite table %q: %w", name, err)
		}

		if err := app.UpsertPostgresTableSchema(collection); err != nil {
			return fmt.Errorf("failed to snapshot postgres satellite table %q: %w", name, err)
		}

		if err := app.UpsertPostgresCollectionMetadata(collection); err != nil {
			return fmt.Errorf("failed to mirror postgres satellite metadata %q: %w", name, err)
		}
	}

	return nil
}

func (app *BaseApp) initPostgresDB() error {
	app.postgresConfig = LoadPostgresConfigFromEnv()
	if !app.postgresConfig.Enabled() {
		return nil
	}

	concurrentDB, err := DefaultPostgresConnect(app.postgresConfig.URL)
	if err != nil {
		return err
	}
	concurrentDB.DB().SetMaxOpenConns(app.config.DataMaxOpenConns)
	concurrentDB.DB().SetMaxIdleConns(app.config.DataMaxIdleConns)

	nonconcurrentDB, err := DefaultPostgresConnect(app.postgresConfig.URL)
	if err != nil {
		_ = concurrentDB.Close()
		return err
	}
	nonconcurrentDB.DB().SetMaxOpenConns(1)
	nonconcurrentDB.DB().SetMaxIdleConns(1)

	app.postgresConcurrentDB = concurrentDB
	app.postgresNonconcurrentDB = nonconcurrentDB
	app.initPostgresInstanceId()

	return nil
}
