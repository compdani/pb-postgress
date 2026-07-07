package core

import (
	"errors"
	"fmt"
)

const defaultPostgresMigrationBatchSize = 500

// CollectionPostgresMigrationConfig defines options for migrating a SQLite-backed
// collection's records to a PocketBase-managed PostgreSQL table.
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
// into a new PocketBase-managed PostgreSQL table and updates the collection metadata
// to route future reads and writes to PostgreSQL.
func (app *BaseApp) MigrateCollectionToPostgres(collectionModelOrIdentifier any, config CollectionPostgresMigrationConfig) (*CollectionPostgresMigrationResult, error) {
	if !app.HasPostgres() {
		return nil, errors.New("postgres is not configured")
	}

	collection, err := getCollectionByModelOrIdentifier(app, collectionModelOrIdentifier)
	if err != nil {
		return nil, err
	}

	if err := validateCollectionPostgresMigration(app, collection); err != nil {
		return nil, err
	}

	deleteSQLiteData := true
	if config.DeleteSQLiteData != nil {
		deleteSQLiteData = *config.DeleteSQLiteData
	}

	batchSize := config.BatchSize
	if batchSize <= 0 {
		batchSize = defaultPostgresMigrationBatchSize
	}

	target := NewCollection(collection.Type, collection.Name)
	target.Fields = collection.Fields
	target.PostgresRecords = true
	if config.PostgresSchema != "" {
		target.PostgresSchema = config.PostgresSchema
	}
	if config.PostgresTable != "" {
		target.PostgresTable = config.PostgresTable
	}
	if config.S3Files != nil {
		target.S3Files = config.S3Files
	}

	if app.postgresTableExists(target) {
		return nil, fmt.Errorf("postgres table %s already exists", app.RecordTable(target))
	}

	total, err := app.countCollectionRecords(collection)
	if err != nil {
		return nil, fmt.Errorf("failed to count collection records: %w", err)
	}

	result := &CollectionPostgresMigrationResult{
		CollectionId:   collection.Id,
		CollectionName: collection.Name,
		MigratedCount:  total,
		DryRun:         config.DryRun,
	}

	if config.DryRun {
		return result, nil
	}

	if err := app.SyncPostgresRecordTableSchema(target, nil); err != nil {
		return nil, fmt.Errorf("failed to create postgres table: %w", err)
	}

	migratedCount, err := app.copyCollectionRecordsToPostgres(collection, target, batchSize)
	if err != nil {
		_, _ = app.PostgresNonconcurrentDB().NewQuery("DROP TABLE IF EXISTS " + app.RecordTable(target)).Execute()
		return nil, err
	}
	result.MigratedCount = migratedCount

	collection.PostgresRecords = true
	collection.PostgresSchema = target.PostgresSchema
	collection.PostgresTable = target.PostgresTable
	if config.S3Files != nil {
		collection.S3Files = config.S3Files
	}
	collection.AllowPostgresRecordsMigration(true)

	txErr := app.RunInTransaction(func(txApp App) error {
		if err := txApp.Save(collection); err != nil {
			return err
		}

		if deleteSQLiteData && txApp.HasTable(collection.Name) {
			if err := txApp.DeleteTable(collection.Name); err != nil {
				return err
			}
		}

		return nil
	})
	if txErr != nil {
		_, _ = app.PostgresNonconcurrentDB().NewQuery("DROP TABLE IF EXISTS " + app.RecordTable(target)).Execute()
		return nil, txErr
	}

	if err := app.ReloadCachedCollections(); err != nil {
		return nil, err
	}

	updated, err := app.FindCachedCollectionByNameOrId(collection.Id)
	if err != nil {
		return nil, err
	}

	if err := app.UpsertPostgresCollectionMetadata(updated); err != nil {
		app.Logger().Warn("Failed to mirror collection metadata to postgres", "collection", updated.Name, "error", err)
	}
	if err := app.UpsertPostgresTableSchema(updated); err != nil {
		app.Logger().Warn("Failed to mirror table schema to postgres", "collection", updated.Name, "error", err)
	}

	return result, nil
}

func validateCollectionPostgresMigration(app *BaseApp, collection *Collection) error {
	if collection.IsView() {
		return errors.New("view collections cannot be migrated to postgres")
	}

	if collection.IsExternal() {
		return errors.New("external collections are already stored in postgres")
	}

	if collection.UsesPostgresRecords() {
		return errors.New("collection records are already stored in postgres")
	}

	if app.IsPostgresBacked(collection) {
		return errors.New("collection records are already stored in postgres")
	}

	return nil
}

func (app *BaseApp) countCollectionRecords(collection *Collection) (int, error) {
	var total int
	err := app.RecordQuery(collection).
		Select("COUNT(*)").
		Row(&total)
	return total, err
}

func (app *BaseApp) copyCollectionRecordsToPostgres(sourceCollection, targetCollection *Collection, batchSize int) (int, error) {
	tableRef := app.RecordTable(targetCollection)
	total := 0
	offset := 0

	for {
		records, err := app.FindRecordsByFilter(sourceCollection, "", "id", batchSize, offset)
		if err != nil {
			return total, fmt.Errorf("failed to read sqlite records: %w", err)
		}

		if len(records) == 0 {
			return total, nil
		}

		for _, record := range records {
			data, err := record.DBExport(app)
			if err != nil {
				return total, fmt.Errorf("failed to export record %q: %w", record.Id, err)
			}

			if _, err := app.PostgresNonconcurrentDB().Insert(tableRef, data).Execute(); err != nil {
				return total, fmt.Errorf("failed to insert record %q into postgres: %w", record.Id, err)
			}

			total++
		}

		if len(records) < batchSize {
			return total, nil
		}

		offset += len(records)
	}
}
