package core

import (
	"strings"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/tools/list"
)

// collectionExternalOptions defines shared PostgreSQL routing options.
type collectionExternalOptions struct {
	External       bool   `form:"external" json:"external,omitempty"`
	PostgresTable  string `form:"postgresTable" json:"postgresTable,omitempty"`
	PostgresSchema string `form:"postgresSchema" json:"postgresSchema,omitempty"`
}

func (o collectionExternalOptions) isZero() bool {
	return !o.External && o.PostgresTable == "" && o.PostgresSchema == ""
}

var postgresSatelliteCollectionNames = []string{
	CollectionNameMFAs,
	CollectionNameOTPs,
	CollectionNameExternalAuths,
	CollectionNameAuthOrigins,
}

func isPostgresSatelliteCollection(name string) bool {
	return list.ExistInSlice(name, postgresSatelliteCollectionNames)
}

// IsExternal reports whether the collection is explicitly marked as external.
func (m *Collection) IsExternal() bool {
	return m != nil && m.collectionExternalOptions.External
}

// PostgresTableName returns the linked PostgreSQL table name.
func (m *Collection) PostgresTableName(app App) string {
	if m == nil {
		return ""
	}

	if table := strings.TrimSpace(m.PostgresTable); table != "" {
		return table
	}

	return m.Name
}

// PostgresSchemaName returns the linked PostgreSQL schema name.
func (m *Collection) PostgresSchemaName(app App) string {
	if m == nil {
		return ""
	}

	if schema := strings.TrimSpace(m.PostgresSchema); schema != "" {
		return schema
	}

	if ba, ok := app.(*BaseApp); ok && ba.postgresConfig.Enabled() {
		return ba.postgresConfig.DefaultSchema
	}

	return "public"
}

func (o *collectionExternalOptions) validateExternal(app App, cv *collectionValidator) error {
	if !o.External {
		return nil
	}

	if ba, ok := app.(*BaseApp); ok && !ba.HasPostgres() {
		return validation.Errors{
			"external": validation.NewError("validation_external_requires_postgres", "External collections require PB_POSTGRES_URL to be configured."),
		}
	}

	return validation.ValidateStruct(o,
		validation.Field(&o.PostgresTable, validation.When(o.PostgresTable != "", validation.Length(1, 255))),
		validation.Field(&o.PostgresSchema, validation.When(o.PostgresSchema != "", validation.Length(1, 255))),
	)
}
