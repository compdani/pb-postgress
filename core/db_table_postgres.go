package core

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/types"
)

type PostgresTableInfo struct {
	Schema      string `json:"schema"`
	Table       string `json:"table"`
	Registered  bool   `json:"registered"`
	CollectionId string `json:"collectionId"`
	CollectionName string `json:"collectionName"`
}

type PostgresColumnInfo struct {
	Name     string `json:"name"`
	DataType string `json:"dataType"`
	IsNullable bool `json:"isNullable"`
}

// ListPostgresTables returns PostgreSQL tables with registration status.
func (app *BaseApp) ListPostgresTables(search string) ([]PostgresTableInfo, error) {
	if !app.HasPostgres() {
		return nil, fmt.Errorf("postgres is not configured")
	}

	schema := app.postgresConfig.DefaultSchema
	rows := []struct {
		Schema string `db:"table_schema"`
		Table  string `db:"table_name"`
	}{}

	query := app.PostgresConcurrentDB().NewQuery(`
		SELECT table_schema AS "table_schema", table_name AS "table_name"
		FROM information_schema.tables
		WHERE table_type = 'BASE TABLE'
		  AND table_schema = {:schema}
		  AND table_name NOT IN ({:metaCollections}, {:metaSchemas})
		ORDER BY table_name ASC
	`).Bind(dbx.Params{
		"schema":         schema,
		"metaCollections": PostgresMetadataCollectionsTable,
		"metaSchemas":    PostgresMetadataTableSchemasTable,
	})

	if err := query.All(&rows); err != nil {
		return nil, err
	}

	registered := map[string]postgresCollectionMetadataRow{}
	metaRows, err := app.FindAllPostgresCollectionMetadata()
	if err != nil {
		return nil, err
	}
	for _, row := range metaRows {
		imported, err := row.toCollection(app)
		if err != nil {
			continue
		}
		key := imported.PostgresSchemaName(app) + "." + imported.PostgresTableName(app)
		registered[key] = *row
	}

	result := make([]PostgresTableInfo, 0, len(rows))
	for _, row := range rows {
		if search != "" && !strings.Contains(strings.ToLower(row.Table), strings.ToLower(search)) {
			continue
		}

		key := row.Schema + "." + row.Table
		info := PostgresTableInfo{
			Schema: row.Schema,
			Table:  row.Table,
		}

		if meta, ok := registered[key]; ok {
			info.Registered = true
			info.CollectionId = meta.Id
			info.CollectionName = meta.Name
		} else if meta, ok := registered[schema+"."+row.Table]; ok {
			info.Registered = true
			info.CollectionId = meta.Id
			info.CollectionName = meta.Name
		}

		result = append(result, info)
	}

	return result, nil
}

// IntrospectPostgresTable returns live column metadata for a PostgreSQL table.
func (app *BaseApp) IntrospectPostgresTable(schema, table string) ([]PostgresColumnInfo, error) {
	if !app.HasPostgres() {
		return nil, fmt.Errorf("postgres is not configured")
	}

	if schema == "" {
		schema = app.postgresConfig.DefaultSchema
	}

	columns := []PostgresColumnInfo{}
	err := app.PostgresConcurrentDB().NewQuery(`
		SELECT column_name AS "name", data_type AS "dataType", is_nullable = 'YES' AS "isNullable"
		FROM information_schema.columns
		WHERE table_schema = {:schema} AND table_name = {:table}
		ORDER BY ordinal_position ASC
	`).Bind(dbx.Params{
		"schema": schema,
		"table":  table,
	}).All(&columns)

	return columns, err
}

type PostgresIndexInfo struct {
	Name string `json:"name"`
	Def  string `json:"def"`
}

func (app *BaseApp) introspectPostgresPrimaryKey(schema, table string) ([]string, error) {
	if schema == "" {
		schema = app.postgresConfig.DefaultSchema
	}

	cols := []string{}
	err := app.PostgresConcurrentDB().NewQuery(`
		SELECT kcu.column_name AS "name"
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name
		 AND tc.table_schema = kcu.table_schema
		WHERE tc.constraint_type = 'PRIMARY KEY'
		  AND tc.table_schema = {:schema}
		  AND tc.table_name = {:table}
		ORDER BY kcu.ordinal_position ASC
	`).Bind(dbx.Params{
		"schema": schema,
		"table":  table,
	}).Column(&cols)

	return cols, err
}

func (app *BaseApp) introspectPostgresIndexes(schema, table string) ([]PostgresIndexInfo, error) {
	if schema == "" {
		schema = app.postgresConfig.DefaultSchema
	}

	indexes := []PostgresIndexInfo{}
	err := app.PostgresConcurrentDB().NewQuery(`
		SELECT indexname AS "name", indexdef AS "def"
		FROM pg_indexes
		WHERE schemaname = {:schema} AND tablename = {:table}
		ORDER BY indexname ASC
	`).Bind(dbx.Params{
		"schema": schema,
		"table":  table,
	}).All(&indexes)

	return indexes, err
}

// GetPostgresTablePreview returns cached or live table preview data.
func (app *BaseApp) GetPostgresTablePreview(schema, table string, live bool) (map[string]any, error) {
	if !app.HasPostgres() {
		return nil, fmt.Errorf("postgres is not configured")
	}

	if schema == "" {
		schema = app.postgresConfig.DefaultSchema
	}

	registered := false
	var collectionId string
	var syncedAt types.DateTime

	if collection, err := app.FindPostgresCollectionByTable(schema, table); err == nil && collection != nil {
		registered = true
		collectionId = collection.Id

		if !live {
			if snapshot, err := app.FindPostgresTableSchema(collection.Id); err == nil {
				syncedAt = snapshot.SyncedAt
				columns := []PostgresColumnInfo{}
				_ = json.Unmarshal(snapshot.Columns, &columns)

				inferred := make([]map[string]any, len(columns))
				for i, col := range columns {
					inferred[i] = map[string]any{
						"column":    col.Name,
						"dataType":  col.DataType,
						"fieldType": postgresFieldTypeFromSQL(col.DataType),
					}
				}

				return map[string]any{
					"schema":       schema,
					"table":        table,
					"registered":   registered,
					"collectionId": collectionId,
					"syncedAt":     syncedAt,
					"columns":      columns,
					"inferred":     inferred,
					"cached":       true,
				}, nil
			}
		}
	}

	columns, err := app.IntrospectPostgresTable(schema, table)
	if err != nil {
		return nil, err
	}

	inferred := make([]map[string]any, len(columns))
	for i, col := range columns {
		inferred[i] = map[string]any{
			"column":    col.Name,
			"dataType":  col.DataType,
			"fieldType": postgresFieldTypeFromSQL(col.DataType),
		}
	}

	return map[string]any{
		"schema":       schema,
		"table":        table,
		"registered":   registered,
		"collectionId": collectionId,
		"syncedAt":     syncedAt,
		"columns":      columns,
		"inferred":     inferred,
		"cached":       false,
	}, nil
}

// FindPostgresCollectionByTable returns a registered collection for the given physical table.
func (app *BaseApp) FindPostgresCollectionByTable(schema, table string) (*Collection, error) {
	if !app.HasPostgres() {
		return nil, fmt.Errorf("postgres is not configured")
	}

	if schema == "" {
		schema = app.postgresConfig.DefaultSchema
	}

	rows, err := app.FindAllPostgresCollectionMetadata()
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		collection, err := row.toCollection(app)
		if err != nil {
			continue
		}
		if collection.PostgresSchemaName(app) == schema && collection.PostgresTableName(app) == table {
			return collection, nil
		}
	}

	return nil, fmt.Errorf("no registered collection for table %s.%s", schema, table)
}

func postgresFieldTypeFromSQL(dataType string) string {
	switch strings.ToLower(dataType) {
	case "boolean", "bool":
		return FieldTypeBool
	case "integer", "bigint", "smallint", "numeric", "double precision", "real", "decimal":
		return FieldTypeNumber
	case "json", "jsonb":
		return FieldTypeJSON
	case "timestamp without time zone", "timestamp with time zone", "timestamptz", "date", "time without time zone":
		return FieldTypeDate
	default:
		return FieldTypeText
	}
}
