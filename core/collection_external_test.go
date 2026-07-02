package core

import (
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
