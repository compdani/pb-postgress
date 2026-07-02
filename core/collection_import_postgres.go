package core

import (
	"errors"
	"fmt"
)

type PostgresImportConfig struct {
	Schema         string
	Table          string
	CollectionName string
	Type           string
	DryRun         bool
	S3Files        *bool
}

// ImportPostgresTable registers an existing PostgreSQL table as an external PocketBase collection.
func (app *BaseApp) ImportPostgresTable(config PostgresImportConfig) (*Collection, error) {
	if !app.HasPostgres() {
		return nil, errors.New("postgres is not configured")
	}

	if config.Schema == "" {
		config.Schema = app.postgresConfig.DefaultSchema
	}
	if config.Table == "" {
		return nil, errors.New("table name is required")
	}
	if config.CollectionName == "" {
		config.CollectionName = config.Table
	}
	if config.Type == "" {
		config.Type = CollectionTypeBase
	}

	if err := app.ensurePostgresTableUnregistered(config.Schema, config.Table); err != nil {
		return nil, err
	}

	columns, err := app.IntrospectPostgresTable(config.Schema, config.Table)
	if err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("table %s.%s was not found or has no columns", config.Schema, config.Table)
	}

	collection := NewCollection(config.Type, config.CollectionName)
	collection.External = true
	collection.PostgresTable = config.Table
	collection.PostgresSchema = config.Schema

	if config.S3Files != nil {
		collection.S3Files = config.S3Files
	} else {
		s3Files := true
		collection.S3Files = &s3Files
	}

	hasId := false
	for _, col := range columns {
		if col.Name == FieldNameId {
			hasId = true
		}

		field, err := newFieldFromPostgresColumn(col)
		if err != nil {
			return nil, err
		}
		collection.Fields.Add(field)
	}

	if !hasId {
		return nil, errors.New("table must contain an id column to be imported")
	}

	if config.Type == CollectionTypeAuth {
		collection.initDefaultFields()
	}

	if config.DryRun {
		return collection, nil
	}

	if err := app.Save(collection); err != nil {
		return nil, err
	}

	return collection, nil
}

func (app *BaseApp) ensurePostgresTableUnregistered(schema, table string) error {
	rows, err := app.FindAllPostgresCollectionMetadata()
	if err != nil {
		return err
	}

	for _, row := range rows {
		imported, err := row.toCollection(app)
		if err != nil {
			return err
		}

		if imported.PostgresSchemaName(app) == schema && imported.PostgresTableName(app) == table {
			return fmt.Errorf("table %s.%s is already registered as collection %q", schema, table, imported.Name)
		}
	}

	return nil
}

func newFieldFromPostgresColumn(col PostgresColumnInfo) (Field, error) {
	fieldType := postgresFieldTypeFromSQL(col.DataType)

	factory, ok := Fields[fieldType]
	if !ok {
		return nil, fmt.Errorf("unsupported postgres type %q for column %q", col.DataType, col.Name)
	}

	field := factory()
	field.SetName(col.Name)
	if col.Name == FieldNameId {
		field.SetSystem(true)
		if textField, ok := field.(*TextField); ok {
			textField.PrimaryKey = true
			textField.Required = true
		}
	}

	return field, nil
}
