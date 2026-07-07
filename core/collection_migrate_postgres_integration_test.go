package core_test

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestMigrateCollectionToPostgresIntegration(t *testing.T) {
	testApp, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer testApp.Cleanup()

	if !testApp.HasPostgres() {
		t.Skip("postgres is not configured")
	}

	collection := core.NewBaseCollection("migration_products")
	collection.Fields.Add(&core.TextField{Name: "title", Required: true})
	if err := testApp.Save(collection); err != nil {
		t.Fatal(err)
	}
	defer func() {
		collection.IntegrityChecks(false)
		_ = testApp.Delete(collection)
	}()

	record := core.NewRecord(collection)
	record.Set("title", "Widget")
	if err := testApp.Save(record); err != nil {
		t.Fatal(err)
	}

	result, err := testApp.MigrateCollectionToPostgres(collection, core.CollectionPostgresMigrationConfig{})
	if err != nil {
		t.Fatal(err)
	}

	if result.MigratedCount != 1 {
		t.Fatalf("expected 1 migrated record, got %d", result.MigratedCount)
	}

	updated, err := testApp.FindCachedCollectionByNameOrId(collection.Id)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.UsesPostgresRecords() {
		t.Fatal("expected collection to use postgres records after migration")
	}

	if testApp.HasTable(collection.Name) {
		t.Fatal("expected sqlite record table to be dropped after migration")
	}

	migratedRecord, err := testApp.FindRecordById(updated, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if migratedRecord.GetString("title") != "Widget" {
		t.Fatalf("expected migrated record title %q, got %q", "Widget", migratedRecord.GetString("title"))
	}
}

func TestMigrateCollectionToPostgresDryRunIntegration(t *testing.T) {
	testApp, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer testApp.Cleanup()

	if !testApp.HasPostgres() {
		t.Skip("postgres is not configured")
	}

	collection := core.NewBaseCollection("migration_orders")
	collection.Fields.Add(&core.TextField{Name: "note"})
	if err := testApp.Save(collection); err != nil {
		t.Fatal(err)
	}
	defer func() {
		collection.IntegrityChecks(false)
		_ = testApp.Delete(collection)
	}()

	record := core.NewRecord(collection)
	record.Set("note", "test")
	if err := testApp.Save(record); err != nil {
		t.Fatal(err)
	}

	result, err := testApp.MigrateCollectionToPostgres(collection, core.CollectionPostgresMigrationConfig{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}

	if !result.DryRun || result.MigratedCount != 1 {
		t.Fatalf("expected dry run preview with 1 record, got %+v", result)
	}

	if collection.UsesPostgresRecords() {
		t.Fatal("expected collection to remain sqlite-backed after dry run")
	}
}
