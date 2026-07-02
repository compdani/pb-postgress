package core

import (
	"context"
	"testing"
)

func TestCollectionIsExternal(t *testing.T) {
	collection := NewBaseCollection("products")
	if collection.IsExternal() {
		t.Fatal("expected non-external collection by default")
	}

	collection.External = true
	if !collection.IsExternal() {
		t.Fatal("expected external collection")
	}
}

func TestIsPostgresBackedWithoutConnection(t *testing.T) {
	app := NewBaseApp(BaseAppConfig{
		DataDir: t.TempDir(),
	})

	collection := NewBaseCollection("products")
	collection.External = true
	if app.IsPostgresBacked(collection) {
		t.Fatal("expected false without postgres connection")
	}
}

func TestPostgresFieldTypeFromSQL(t *testing.T) {
	cases := map[string]string{
		"boolean":     FieldTypeBool,
		"jsonb":       FieldTypeJSON,
		"timestamptz": FieldTypeDate,
		"text":        FieldTypeText,
	}

	for input, expected := range cases {
		if got := postgresFieldTypeFromSQL(input); got != expected {
			t.Fatalf("expected %q for %q, got %q", expected, input, got)
		}
	}
}

func TestRecordTableExternal(t *testing.T) {
	app := NewBaseApp(BaseAppConfig{DataDir: t.TempDir()})
	app.postgresConfig = PostgresConfig{DefaultSchema: "public"}

	db, err := DefaultDBConnect(app.DataDir() + "/data.db")
	if err != nil {
		t.Fatal(err)
	}
	app.postgresNonconcurrentDB = db

	collection := NewBaseCollection("products")
	collection.External = true

	table := app.RecordTable(collection)
	if table != `"public"."products"` {
		t.Fatalf("expected quoted postgres table, got %q", table)
	}
}

func TestCollectionUsesS3Files(t *testing.T) {
	app := NewBaseApp(BaseAppConfig{DataDir: t.TempDir()})
	defer app.ResetBootstrapState()
	app.settings = newDefaultSettings()

	s3True := true
	s3False := false

	collection := NewBaseCollection("products")
	collection.External = true
	collection.S3Files = &s3True

	// scope=all, S3 disabled
	if collection.UsesS3Files(app) {
		t.Fatal("expected false when S3 is disabled with scope=all")
	}

	app.Settings().S3.Enabled = true
	app.Settings().S3.Scope = S3ScopeAll
	if !collection.UsesS3Files(app) {
		t.Fatal("expected true when S3 is enabled with scope=all")
	}

	app.Settings().S3.Scope = S3ScopePerCollection
	if !collection.UsesS3Files(app) {
		t.Fatal("expected true when s3Files=true with scope=perCollection")
	}

	collection.S3Files = &s3False
	if collection.UsesS3Files(app) {
		t.Fatal("expected false when s3Files=false with scope=perCollection")
	}

	collection.S3Files = nil
	if collection.UsesS3Files(app) {
		t.Fatal("expected false when s3Files is unset with scope=perCollection")
	}
}

func TestCollectionS3FilesValidation(t *testing.T) {
	app := NewBaseApp(BaseAppConfig{DataDir: t.TempDir()})
	defer app.ResetBootstrapState()

	app.settings = newDefaultSettings()

	s3True := true
	validator := &collectionValidator{
		app: app,
		new: NewBaseCollection("posts"),
		ctx: context.Background(),
	}

	validator.new.S3Files = &s3True
	err := validator.new.collectionExternalOptions.validateExternal(app, validator)
	if err == nil {
		t.Fatal("expected validation error when s3Files=true on non-postgres collection")
	}

	app.Settings().S3.Enabled = true
	app.Settings().S3.Endpoint = "https://example.com"
	app.Settings().S3.Bucket = "test"
	app.Settings().S3.Region = "test"
	app.Settings().S3.AccessKey = "test"
	app.Settings().S3.Secret = "test"

	err = validator.new.collectionExternalOptions.validateExternal(app, validator)
	if err == nil {
		t.Fatal("expected validation error when s3Files=true on non-postgres collection even with S3 enabled")
	}

	pgCollection := NewBaseCollection("products")
	pgCollection.External = true
	pgCollection.S3Files = &s3True

	pgValidator := &collectionValidator{
		app: app,
		new: pgCollection,
		ctx: context.Background(),
	}

	err = pgCollection.collectionExternalOptions.validateExternal(app, pgValidator)
	if err == nil {
		t.Fatal("expected validation error when external collection requires postgres")
	}

	app.postgresConfig = PostgresConfig{DefaultSchema: "public"}
	db, err := DefaultDBConnect(app.DataDir() + "/data.db")
	if err != nil {
		t.Fatal(err)
	}
	app.postgresNonconcurrentDB = db

	err = pgCollection.collectionExternalOptions.validateExternal(app, pgValidator)
	if err != nil {
		t.Fatalf("expected no validation error for external collection with s3Files and S3 enabled, got %v", err)
	}
}
