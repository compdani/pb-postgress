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
