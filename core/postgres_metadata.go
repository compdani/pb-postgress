package core

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/types"
)

type postgresCollectionMetadataRow struct {
	Id         string         `db:"id"`
	System     bool           `db:"system"`
	Type       string         `db:"type"`
	Name       string         `db:"name"`
	Fields     types.JSONRaw  `db:"fields"`
	Indexes    types.JSONRaw  `db:"indexes"`
	ListRule   *string        `db:"listRule"`
	ViewRule   *string        `db:"viewRule"`
	CreateRule *string        `db:"createRule"`
	UpdateRule *string        `db:"updateRule"`
	DeleteRule *string        `db:"deleteRule"`
	Options    types.JSONRaw  `db:"options"`
	Created    types.DateTime `db:"created"`
	Updated    types.DateTime `db:"updated"`
	InstanceId string         `db:"instanceId"`
	Version    int64          `db:"version"`
}

type postgresTableSchemaRow struct {
	CollectionId string         `db:"collectionId"`
	Schema       string         `db:"schema"`
	TableName    string         `db:"tableName"`
	Columns      types.JSONRaw  `db:"columns"`
	PrimaryKey   types.JSONRaw  `db:"primaryKey"`
	Indexes      types.JSONRaw  `db:"indexes"`
	SyncedAt     types.DateTime `db:"syncedAt"`
}

func (app *BaseApp) postgresMetadataTable(name string) string {
	schema := app.postgresConfig.DefaultSchema
	return fmt.Sprintf(`"%s"."%s"`, schema, name)
}

// InitPostgresMetadata creates metadata tables in PostgreSQL if they don't exist.
func (app *BaseApp) InitPostgresMetadata() error {
	if !app.HasPostgres() {
		return nil
	}

	schema := app.postgresConfig.DefaultSchema
	queries := []string{
		fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS "%s"`, schema),
		fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				"id" TEXT PRIMARY KEY,
				"system" BOOLEAN NOT NULL DEFAULT FALSE,
				"type" TEXT NOT NULL DEFAULT 'base',
				"name" TEXT NOT NULL UNIQUE,
				"fields" JSONB NOT NULL DEFAULT '[]',
				"indexes" JSONB NOT NULL DEFAULT '[]',
				"listRule" TEXT,
				"viewRule" TEXT,
				"createRule" TEXT,
				"updateRule" TEXT,
				"deleteRule" TEXT,
				"options" JSONB NOT NULL DEFAULT '{}',
				"created" TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				"updated" TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				"instanceId" TEXT NOT NULL DEFAULT '',
				"version" BIGINT NOT NULL DEFAULT 1
			)`, app.postgresMetadataTable(PostgresMetadataCollectionsTable)),
		fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				"collectionId" TEXT PRIMARY KEY,
				"schema" TEXT NOT NULL DEFAULT 'public',
				"tableName" TEXT NOT NULL,
				"columns" JSONB NOT NULL DEFAULT '[]',
				"primaryKey" JSONB NOT NULL DEFAULT '[]',
				"indexes" JSONB NOT NULL DEFAULT '[]',
				"syncedAt" TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`, app.postgresMetadataTable(PostgresMetadataTableSchemasTable)),
	}

	for _, q := range queries {
		if _, err := app.PostgresNonconcurrentDB().NewQuery(q).Execute(); err != nil {
			return err
		}
	}

	return nil
}

func (app *BaseApp) nextPostgresCollectionVersion(collectionId string) int64 {
	var version int64
	_ = app.PostgresConcurrentDB().NewQuery(fmt.Sprintf(`
		SELECT COALESCE(MAX("version"), 0) + 1
		FROM %s
		WHERE "id" = {:id}
	`, app.postgresMetadataTable(PostgresMetadataCollectionsTable))).
		Bind(dbx.Params{"id": collectionId}).
		Row(&version)

	if version <= 0 {
		version = 1
	}

	return version
}

// UpsertPostgresCollectionMetadata writes collection metadata to PostgreSQL.
func (app *BaseApp) UpsertPostgresCollectionMetadata(collection *Collection) error {
	if !app.HasPostgres() || collection == nil || !app.shouldMirrorCollectionMetadata(collection) {
		return nil
	}

	exported, err := collection.DBExport(app)
	if err != nil {
		return err
	}

	version := app.nextPostgresCollectionVersion(collection.Id)
	instanceId := ""
	if app.store != nil {
		if v, ok := app.store.Get("postgresInstanceId").(string); ok {
			instanceId = v
		}
	}

	fieldsJSON, _ := json.Marshal(exported["fields"])
	indexesJSON, _ := json.Marshal(exported["indexes"])
	optionsJSON := normalizePostgresMetadataJSON(exported["options"])

	_, err = app.PostgresNonconcurrentDB().NewQuery(fmt.Sprintf(`
		INSERT INTO %s (
			"id", "system", "type", "name", "fields", "indexes",
			"listRule", "viewRule", "createRule", "updateRule", "deleteRule",
			"options", "created", "updated", "instanceId", "version"
		) VALUES (
			{:id}, {:system}, {:type}, {:name}, {:fields}::jsonb, {:indexes}::jsonb,
			{:listRule}, {:viewRule}, {:createRule}, {:updateRule}, {:deleteRule},
			{:options}::jsonb, {:created}, {:updated}, {:instanceId}, {:version}
		)
		ON CONFLICT ("id") DO UPDATE SET
			"system" = EXCLUDED."system",
			"type" = EXCLUDED."type",
			"name" = EXCLUDED."name",
			"fields" = EXCLUDED."fields",
			"indexes" = EXCLUDED."indexes",
			"listRule" = EXCLUDED."listRule",
			"viewRule" = EXCLUDED."viewRule",
			"createRule" = EXCLUDED."createRule",
			"updateRule" = EXCLUDED."updateRule",
			"deleteRule" = EXCLUDED."deleteRule",
			"options" = EXCLUDED."options",
			"created" = EXCLUDED."created",
			"updated" = EXCLUDED."updated",
			"instanceId" = EXCLUDED."instanceId",
			"version" = EXCLUDED."version"
	`, app.postgresMetadataTable(PostgresMetadataCollectionsTable))).
		Bind(dbx.Params{
			"id":         exported["id"],
			"system":     exported["system"],
			"type":       exported["type"],
			"name":       exported["name"],
			"fields":     string(fieldsJSON),
			"indexes":    string(indexesJSON),
			"listRule":   exported["listRule"],
			"viewRule":   exported["viewRule"],
			"createRule": exported["createRule"],
			"updateRule": exported["updateRule"],
			"deleteRule": exported["deleteRule"],
			"options":    optionsJSON,
			"created":    exported["created"],
			"updated":    exported["updated"],
			"instanceId": instanceId,
			"version":    version,
		}).
		Execute()

	return err
}

// DeletePostgresCollectionMetadata removes collection metadata from PostgreSQL.
func (app *BaseApp) DeletePostgresCollectionMetadata(collection *Collection) error {
	if !app.HasPostgres() || collection == nil || !app.shouldMirrorCollectionMetadata(collection) {
		return nil
	}

	_, err := app.PostgresNonconcurrentDB().NewQuery(fmt.Sprintf(`
		DELETE FROM %s WHERE "id" = {:id}
	`, app.postgresMetadataTable(PostgresMetadataCollectionsTable))).
		Bind(dbx.Params{"id": collection.Id}).
		Execute()
	if err != nil {
		return err
	}

	_, err = app.PostgresNonconcurrentDB().NewQuery(fmt.Sprintf(`
		DELETE FROM %s WHERE "collectionId" = {:id}
	`, app.postgresMetadataTable(PostgresMetadataTableSchemasTable))).
		Bind(dbx.Params{"id": collection.Id}).
		Execute()

	return err
}

func (app *BaseApp) shouldMirrorCollectionMetadata(collection *Collection) bool {
	return app.IsPostgresBacked(collection)
}

func (row *postgresCollectionMetadataRow) ToCollection(app App) (*Collection, error) {
	return row.toCollection(app)
}

func (row *postgresCollectionMetadataRow) toCollection(app App) (*Collection, error) {
	data := map[string]any{
		"id":         row.Id,
		"system":     row.System,
		"type":       row.Type,
		"name":       row.Name,
		"fields":     json.RawMessage(row.Fields),
		"indexes":    json.RawMessage(row.Indexes),
		"listRule":   row.ListRule,
		"viewRule":   row.ViewRule,
		"createRule": row.CreateRule,
		"updateRule": row.UpdateRule,
		"deleteRule": row.DeleteRule,
		"created":    row.Created,
		"updated":    row.Updated,
	}

	if err := mergeCollectionOptionsIntoData(data, row.Options); err != nil {
		return nil, err
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	collection := &Collection{}
	if err := json.Unmarshal(raw, collection); err != nil {
		return nil, err
	}

	collection.RawOptions = row.Options

	return collection, nil
}

func mergeCollectionOptionsIntoData(data map[string]any, options types.JSONRaw) error {
	if len(options) == 0 || string(options) == "null" {
		return nil
	}

	flat, err := parseJSONObject(options)
	if err != nil {
		return err
	}

	if len(flat) == 0 {
		return nil
	}

	for k, v := range flat {
		data[k] = v
	}

	return nil
}

func parseJSONObject(raw types.JSONRaw) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "{}" {
		return map[string]any{}, nil
	}

	flat := map[string]any{}
	if err := json.Unmarshal(raw, &flat); err == nil {
		return flat, nil
	}

	// handle legacy/double-encoded jsonb string values
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, err
	}

	encoded = strings.TrimSpace(encoded)
	if encoded == "" || encoded == "{}" {
		return map[string]any{}, nil
	}

	if err := json.Unmarshal([]byte(encoded), &flat); err != nil {
		return nil, err
	}

	return flat, nil
}

func normalizePostgresMetadataJSON(value any) string {
	switch v := value.(type) {
	case types.JSONRaw:
		if len(v) == 0 {
			return "{}"
		}
		return string(v)
	case string:
		if strings.TrimSpace(v) == "" {
			return "{}"
		}
		return v
	default:
		raw, err := json.Marshal(v)
		if err != nil || len(raw) == 0 {
			return "{}"
		}
		return string(raw)
	}
}

// FindAllPostgresCollectionMetadata returns all mirrored collection metadata rows.
func (app *BaseApp) FindAllPostgresCollectionMetadata() ([]*postgresCollectionMetadataRow, error) {
	if !app.HasPostgres() {
		return nil, nil
	}

	rows := []*postgresCollectionMetadataRow{}
	err := app.PostgresConcurrentDB().NewQuery(fmt.Sprintf(`
		SELECT * FROM %s ORDER BY "name" ASC
	`, app.postgresMetadataTable(PostgresMetadataCollectionsTable))).All(&rows)

	return rows, err
}

// FindPostgresTableSchema returns a cached physical table schema snapshot.
func (app *BaseApp) FindPostgresTableSchema(collectionId string) (*postgresTableSchemaRow, error) {
	if !app.HasPostgres() {
		return nil, fmt.Errorf("postgres is not configured")
	}

	row := &postgresTableSchemaRow{}
	err := app.PostgresConcurrentDB().NewQuery(fmt.Sprintf(`
		SELECT * FROM %s WHERE "collectionId" = {:id}
	`, app.postgresMetadataTable(PostgresMetadataTableSchemasTable))).
		Bind(dbx.Params{"id": collectionId}).
		One(row)
	if err != nil {
		return nil, err
	}

	return row, nil
}

// buildTableSchemaSnapshot introspects a Postgres-backed collection's physical table.
func (app *BaseApp) buildTableSchemaSnapshot(collection *Collection) (*postgresTableSchemaRow, error) {
	if collection == nil || !app.IsPostgresBacked(collection) {
		return nil, fmt.Errorf("collection is not postgres-backed")
	}

	schema := collection.PostgresSchemaName(app)
	table := collection.PostgresTableName(app)

	columns, err := app.IntrospectPostgresTable(schema, table)
	if err != nil {
		return nil, err
	}

	primaryKey, err := app.introspectPostgresPrimaryKey(schema, table)
	if err != nil {
		return nil, err
	}

	indexes, err := app.introspectPostgresIndexes(schema, table)
	if err != nil {
		return nil, err
	}

	columnsJSON, _ := json.Marshal(columns)
	pkJSON, _ := json.Marshal(primaryKey)
	indexesJSON, _ := json.Marshal(indexes)

	return &postgresTableSchemaRow{
		CollectionId: collection.Id,
		Schema:       schema,
		TableName:    table,
		Columns:      types.JSONRaw(columnsJSON),
		PrimaryKey:   types.JSONRaw(pkJSON),
		Indexes:      types.JSONRaw(indexesJSON),
		SyncedAt:     types.NowDateTime(),
	}, nil
}

// UpsertPostgresTableSchema refreshes the physical table schema snapshot in PostgreSQL.
func (app *BaseApp) UpsertPostgresTableSchema(collection *Collection) error {
	if !app.HasPostgres() || collection == nil || !app.IsPostgresBacked(collection) {
		return nil
	}

	snapshot, err := app.buildTableSchemaSnapshot(collection)
	if err != nil {
		return err
	}

	_, err = app.PostgresNonconcurrentDB().NewQuery(fmt.Sprintf(`
		INSERT INTO %s (
			"collectionId", "schema", "tableName", "columns", "primaryKey", "indexes", "syncedAt"
		) VALUES (
			{:collectionId}, {:schema}, {:tableName}, {:columns}::jsonb, {:primaryKey}::jsonb, {:indexes}::jsonb, {:syncedAt}
		)
		ON CONFLICT ("collectionId") DO UPDATE SET
			"schema" = EXCLUDED."schema",
			"tableName" = EXCLUDED."tableName",
			"columns" = EXCLUDED."columns",
			"primaryKey" = EXCLUDED."primaryKey",
			"indexes" = EXCLUDED."indexes",
			"syncedAt" = EXCLUDED."syncedAt"
	`, app.postgresMetadataTable(PostgresMetadataTableSchemasTable))).
		Bind(dbx.Params{
			"collectionId": snapshot.CollectionId,
			"schema":       snapshot.Schema,
			"tableName":    snapshot.TableName,
			"columns":      string(snapshot.Columns),
			"primaryKey":   string(snapshot.PrimaryKey),
			"indexes":      string(snapshot.Indexes),
			"syncedAt":     snapshot.SyncedAt,
		}).
		Execute()

	return err
}

// RefreshPostgresTableSchemaByTable re-introspects and upserts schema for a registered physical table.
func (app *BaseApp) RefreshPostgresTableSchemaByTable(schema, table string) (*Collection, error) {
	collection, err := app.FindPostgresCollectionByTable(schema, table)
	if err != nil {
		return nil, err
	}

	if err := app.UpsertPostgresTableSchema(collection); err != nil {
		return nil, err
	}

	return collection, nil
}
