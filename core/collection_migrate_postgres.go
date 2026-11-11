package core

import (
	"errors"
)

const defaultPostgresMigrationBatchSize = 500

// CollectionPostgresMigrationConfig defines options for migrating a SQLite-backed
// collection's records to a PgBase-managed PostgreSQL table.
type CollectionPostgresMigrationConfig struct {
	// DryRun validates the migration and returns a preview without making changes.
	DryRun bool

	// DeleteSQLiteData drops the old SQLite record table after a successful migration.
	// Defaults to true when unset.
	DeleteSQLiteData *bool

	// BatchSize controls how many records are copied per batch.
	// Defaults to 500 when zero or negative.
	BatchSize int

	// PostgresSchema overrides the destination PostgreSQL schema.
	// Defaults to PB_POSTGRES_SCHEMA or "public".
	PostgresSchema string

	// PostgresTable overrides the destination PostgreSQL table name.
	// Defaults to the collection name.
	PostgresTable string

	// S3Files enables per-collection S3 file storage after migration.
	S3Files *bool
}

// CollectionPostgresMigrationResult summarizes a migration run.
type CollectionPostgresMigrationResult struct {
	CollectionId   string `json:"collectionId"`
	CollectionName string `json:"collectionName"`
	MigratedCount  int    `json:"migratedCount"`
	DryRun         bool   `json:"dryRun"`
}

// MigrateCollectionToPostgres copies all records from a SQLite-backed collection
// into a new PgBase-managed PostgreSQL table and updates the collection metadata
// to route future reads and writes to PostgreSQL.
func (app *BaseApp) MigrateCollectionToPostgres(collectionModelOrIdentifier any, config CollectionPostgresMigrationConfig) (*CollectionPostgresMigrationResult, error) {
	return nil, errors.New("cross-engine collection migration is unsupported: the instance uses one server database; migrate with native database tools")
}
