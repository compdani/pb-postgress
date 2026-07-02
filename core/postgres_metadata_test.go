package core

import (
	"testing"

	"github.com/pocketbase/pocketbase/tools/types"
)

func TestPostgresCollectionMetadataRowToCollectionPreservesOptions(t *testing.T) {
	row := &postgresCollectionMetadataRow{
		Id:      "pbc_test",
		Name:    "orders",
		Type:    CollectionTypeBase,
		Fields:  types.JSONRaw(`[{"name":"id","type":"text"}]`),
		Indexes: types.JSONRaw(`[]`),
		Options: types.JSONRaw(`{"postgresRecords":true}`),
	}

	collection, err := row.toCollection(nil)
	if err != nil {
		t.Fatal(err)
	}

	if !collection.UsesPostgresRecords() {
		t.Fatal("expected postgresRecords option to be loaded from mirrored metadata")
	}
}

func TestMergeCollectionOptionsIntoData(t *testing.T) {
	data := map[string]any{"name": "orders"}

	if err := mergeCollectionOptionsIntoData(data, types.JSONRaw(`{"postgresRecords":true,"postgresSchema":"public"}`)); err != nil {
		t.Fatal(err)
	}

	if data["postgresRecords"] != true {
		t.Fatalf("expected flattened postgresRecords, got %#v", data["postgresRecords"])
	}
	if data["postgresSchema"] != "public" {
		t.Fatalf("expected flattened postgresSchema, got %#v", data["postgresSchema"])
	}
}

func TestMergeCollectionOptionsIntoDataDoubleEncoded(t *testing.T) {
	data := map[string]any{"name": "orders"}

	// simulates jsonb values that were stored as a quoted JSON string
	if err := mergeCollectionOptionsIntoData(data, types.JSONRaw(`"{\"postgresRecords\":true}"`)); err != nil {
		t.Fatal(err)
	}

	if data["postgresRecords"] != true {
		t.Fatalf("expected flattened postgresRecords from double-encoded options, got %#v", data["postgresRecords"])
	}
}
