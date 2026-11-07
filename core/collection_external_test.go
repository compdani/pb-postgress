package core

import (
	"testing"
)

func TestAsBaseApp(t *testing.T) {
	baseApp := NewBaseApp(BaseAppConfig{DataDir: t.TempDir()})
	if got := AsBaseApp(baseApp); got != baseApp {
		t.Fatal("expected direct BaseApp unwrap")
	}

	wrapper := struct {
		App
	}{App: baseApp}
	if got := AsBaseApp(wrapper); got != nil {
		t.Fatal("expected nil for non-accessor wrapper")
	}

	accessor := struct {
		App
	}{App: baseApp}
	if got := AsBaseApp(accessor); got != nil {
		t.Fatal("expected nil for embedded App without accessor")
	}
}

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

	collection2 := NewBaseCollection("orders")
	collection2.PostgresRecords = true
	if app.IsPostgresBacked(collection2) {
		t.Fatal("expected false for postgresRecords without postgres connection")
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

func TestRestoreImmutableExternalOptions(t *testing.T) {
	s3True := true
	original := collectionExternalOptions{
		External:        true,
		PostgresRecords: true,
		PostgresTable:   "orders",
		PostgresSchema:  "public",
		S3Files:         &s3True,
	}

	updated := collectionExternalOptions{}
	updated.restoreImmutableExternalOptions(original)

	if !updated.External || !updated.PostgresRecords {
		t.Fatal("expected external postgres options to be restored")
	}
	if updated.PostgresTable != "orders" || updated.PostgresSchema != "public" {
		t.Fatal("expected postgres table/schema to be restored")
	}
	if updated.S3Files == nil || !*updated.S3Files {
		t.Fatal("expected omitted s3Files to be restored")
	}

	s3False := false
	updated.S3Files = &s3False
	updated.restoreImmutableExternalOptions(original)
	if updated.S3Files == nil || *updated.S3Files {
		t.Fatal("expected explicit s3Files value to be preserved")
	}
}
