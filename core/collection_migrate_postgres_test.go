package core

import (
	"testing"
)

func TestMigrateCollectionToPostgresValidation(t *testing.T) {
	t.Run("without postgres", func(t *testing.T) {
		app := NewBaseApp(BaseAppConfig{DataDir: t.TempDir()})
		if err := app.Bootstrap(); err != nil {
			t.Fatal(err)
		}

		collection := NewBaseCollection("items")
		if err := app.Save(collection); err != nil {
			t.Fatal(err)
		}

		_, err := app.MigrateCollectionToPostgres(collection, CollectionPostgresMigrationConfig{})
		if err == nil {
			t.Fatal("expected error when postgres is not configured")
		}
	})

	t.Run("view collection", func(t *testing.T) {
		app := NewBaseApp(BaseAppConfig{DataDir: t.TempDir()})
		if err := app.Bootstrap(); err != nil {
			t.Fatal(err)
		}

		view := NewViewCollection("stats")
		view.ViewQuery = "SELECT 1 AS id"
		if err := app.Save(view); err != nil {
			t.Fatal(err)
		}

		app.postgresConfig = PostgresConfig{DefaultSchema: "public"}
		db, err := DefaultDBConnect(app.DataDir() + "/postgres.db")
		if err != nil {
			t.Fatal(err)
		}
		app.postgresConcurrentDB = db
		app.postgresNonconcurrentDB = db

		_, err = app.MigrateCollectionToPostgres(view, CollectionPostgresMigrationConfig{})
		if err == nil {
			t.Fatal("expected error for view collection")
		}
	})

	t.Run("already postgres-backed", func(t *testing.T) {
		app := NewBaseApp(BaseAppConfig{DataDir: t.TempDir()})
		if err := app.Bootstrap(); err != nil {
			t.Fatal(err)
		}

		app.postgresConfig = PostgresConfig{DefaultSchema: "public"}
		db, err := DefaultDBConnect(app.DataDir() + "/postgres.db")
		if err != nil {
			t.Fatal(err)
		}
		app.postgresConcurrentDB = db
		app.postgresNonconcurrentDB = db

		collection := NewBaseCollection("managed")
		collection.PostgresRecords = true

		_, err = app.MigrateCollectionToPostgres(collection, CollectionPostgresMigrationConfig{})
		if err == nil {
			t.Fatal("expected error for already postgres-backed collection")
		}
	})
}

func TestAllowPostgresRecordsMigrationValidation(t *testing.T) {
	app := NewBaseApp(BaseAppConfig{DataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}

	app.postgresConfig = PostgresConfig{DefaultSchema: "public"}
	db, err := DefaultDBConnect(app.DataDir() + "/postgres.db")
	if err != nil {
		t.Fatal(err)
	}
	app.postgresConcurrentDB = db
	app.postgresNonconcurrentDB = db

	collection := NewBaseCollection("legacy")
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}

	original := *collection
	collection.PostgresRecords = true

	validator := &collectionValidator{
		app:      app,
		new:      collection,
		original: &original,
		ctx:      t.Context(),
	}

	err = collection.collectionExternalOptions.validateExternal(app, validator)
	if err == nil {
		t.Fatal("expected validation error without migration flag")
	}

	collection.AllowPostgresRecordsMigration(true)
	err = collection.collectionExternalOptions.validateExternal(app, validator)
	if err != nil {
		t.Fatalf("expected no validation error with migration flag, got %v", err)
	}
}
