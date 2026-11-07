package core_test

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"testing"
)

func TestScopeHiddenCollectionHooksAndCascades(t *testing.T) {
	for _, cascade := range []bool{false, true} {
		t.Run(map[bool]string{false: "unset", true: "delete"}[cascade], func(t *testing.T) {
			app, _ := tests.NewTestApp(t)
			target := core.NewBaseCollection("scope_target")
			if err := app.Save(target); err != nil {
				t.Fatal(err)
			}
			hidden := core.NewBaseCollection("scope_hidden")
			hidden.Fields.Add(&core.RelationField{Name: "ref", CollectionId: target.Id, MaxSelect: 1, CascadeDelete: cascade})
			if err := app.Save(hidden); err != nil {
				t.Fatal(err)
			}
			r := core.NewRecord(target)
			if err := app.Save(r); err != nil {
				t.Fatal(err)
			}
			ref := core.NewRecord(hidden)
			ref.Set("ref", r.Id)
			if err := app.Save(ref); err != nil {
				t.Fatal(err)
			}
			if err := app.SetInstanceScope(core.InstanceScope{Mode: "deny", Collections: []string{hidden.Name}}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			app.OnCollectionUpdate().BindFunc(func(e *core.CollectionEvent) error { calls++; return e.Next() })
			hidden.Name = "scope_renamed"
			if err := app.Save(hidden); err == nil {
				t.Fatal("hidden metadata write accepted")
			}
			hidden.Name = "scope_hidden"
			if calls != 0 {
				t.Fatalf("hidden collection hook called %d times", calls)
			}
			if err := app.Delete(r); err != nil {
				t.Fatal(err)
			}
			got, err := app.FindRecordById(hidden, ref.Id)
			if cascade {
				if err == nil {
					t.Fatal("hidden referencing record survived cascade")
				}
			} else if err != nil || got.GetString("ref") != "" {
				t.Fatalf("hidden relation not cleared: %v %v", got, err)
			}
		})
	}
}
