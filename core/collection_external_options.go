package core

import (
	"strings"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/tools/list"
)

// collectionExternalOptions defines shared PostgreSQL routing options.
type collectionExternalOptions struct {
	External        bool   `form:"external" json:"external,omitempty"`
	PostgresRecords bool   `form:"postgresRecords" json:"postgresRecords,omitempty"`
	PostgresTable   string `form:"postgresTable" json:"postgresTable,omitempty"`
	PostgresSchema  string `form:"postgresSchema" json:"postgresSchema,omitempty"`
	S3Files         *bool  `form:"s3Files" json:"s3Files,omitempty"`
}

// restoreImmutableExternalOptions re-applies stored PostgreSQL routing options after
// a partial JSON update. Omitted json fields would otherwise reset to their zero value.
func (o *collectionExternalOptions) restoreImmutableExternalOptions(original collectionExternalOptions) {
	o.External = original.External
	o.PostgresRecords = original.PostgresRecords
	o.PostgresTable = original.PostgresTable
	o.PostgresSchema = original.PostgresSchema
	if o.S3Files == nil {
		o.S3Files = original.S3Files
	}
}

// RestoreImmutableExternalOptions re-applies stored PostgreSQL routing options after
// a partial update request body was bound onto the collection.
func (m *Collection) RestoreImmutableExternalOptions(original Collection) {
	if m == nil {
		return
	}
	m.collectionExternalOptions.restoreImmutableExternalOptions(original.collectionExternalOptions)
}

func (o collectionExternalOptions) isZero() bool {
	return !o.External && !o.PostgresRecords && o.PostgresTable == "" && o.PostgresSchema == "" && o.S3Files == nil
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

// UsesPostgresRecords reports whether the collection stores records in a PocketBase-managed PostgreSQL table.
func (m *Collection) UsesPostgresRecords() bool {
	return m != nil && m.collectionExternalOptions.PostgresRecords
}

// UsesS3Files reports whether the collection should store uploaded files in S3.
func (m *Collection) UsesS3Files(app App) bool {
	if m == nil || app == nil || app.Settings() == nil {
		return false
	}

	settings := app.Settings().S3
	if settings.NormalizedScope() == S3ScopePerCollection {
		return m.S3Files != nil && *m.S3Files && settings.Enabled
	}

	return settings.Enabled
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
	if o.External {
		if ba, ok := app.(*BaseApp); ok && !ba.HasPostgres() {
			return validation.Errors{
				"external": validation.NewError("validation_external_requires_postgres", "External collections require PB_POSTGRES_URL to be configured."),
			}
		}
	}

	if o.PostgresRecords {
		if cv.new.IsView() {
			return validation.Errors{
				"postgresRecords": validation.NewError("validation_postgres_records_view", "View collections cannot store records in PostgreSQL."),
			}
		}

		if o.External {
			return validation.Errors{
				"postgresRecords": validation.NewError("validation_postgres_records_external", "postgresRecords cannot be used together with external collections."),
			}
		}

		if ba, ok := app.(*BaseApp); ok && !ba.HasPostgres() {
			return validation.Errors{
				"postgresRecords": validation.NewError("validation_postgres_records_requires_postgres", "PostgreSQL-backed collections require PB_POSTGRES_URL to be configured."),
			}
		}
	}

	if cv.original != nil && !cv.original.IsNew() {
		if cv.original.PostgresRecords != o.PostgresRecords && !cv.new.allowPostgresRecordsMigration {
			return validation.Errors{
				"postgresRecords": validation.NewError("validation_postgres_records_immutable", "The PostgreSQL records storage option cannot be changed after collection creation."),
			}
		}
	}

	if o.S3Files != nil && *o.S3Files {
		if ba, ok := app.(*BaseApp); ok && !ba.IsPostgresBacked(cv.new) {
			return validation.Errors{
				"s3Files": validation.NewError("validation_s3files_requires_postgres", "S3 file storage can only be enabled for PostgreSQL-backed collections."),
			}
		}

		if !app.Settings().S3.Enabled {
			return validation.Errors{
				"s3Files": validation.NewError("validation_s3files_requires_s3", "S3 file storage requires global S3 to be enabled."),
			}
		}
	}

	return validation.ValidateStruct(o,
		validation.Field(&o.PostgresTable, validation.When(o.PostgresTable != "", validation.Length(1, 255))),
		validation.Field(&o.PostgresSchema, validation.When(o.PostgresSchema != "", validation.Length(1, 255))),
	)
}
