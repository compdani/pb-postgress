package core_test

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestNewCollectionInAllowScope(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	if err := app.SetInstanceScope(core.InstanceScope{Mode: "allow", Collections: []string{"demo1"}}); err != nil {
		t.Fatal(err)
	}

	// regular save
	c := core.NewBaseCollection("fresh_one")
	c.Fields.Add(&core.TextField{Name: "title"})
	if err := app.Save(c); err != nil {
		t.Fatalf("save: %v", err)
	}

	// import path (as used by migrations/snapshots)
	data := []map[string]any{{"name": "fresh_two", "type": "base", "fields": []map[string]any{{"name": "title", "type": "text"}}}}
	if err := app.ImportCollections(data, false); err != nil {
		t.Fatalf("import: %v", err)
	}

	scope, _ := app.InstanceScope()
	t.Log(scope)
	for _, n := range []string{"fresh_one", "fresh_two"} {
		if !app.HasTable(n) {
			t.Fatalf("table %s missing", n)
		}
	}
}

func TestRenameKeepsOtherInstanceScope(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	// another instance sharing the database, which sees demo1 as well
	if _, err := app.DB().NewQuery(`INSERT INTO _instances (id, name, url, [[scopeMode]], [[scopeCollections]], created_at) VALUES ('other','other','', 'deny', '["demo1"]', '2020-01-01T00:00:00Z')`).Execute(); err != nil {
		t.Fatal(err)
	}
	c, err := app.FindCollectionByNameOrId("demo1")
	if err != nil {
		t.Fatal(err)
	}
	c.Name = "demo1_renamed"
	if err := app.Save(c); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := app.DB().NewQuery(`SELECT [[scopeCollections]] FROM _instances WHERE id='other'`).Row(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != `["demo1_renamed"]` {
		t.Fatalf("other scope: %s", raw)
	}
}
