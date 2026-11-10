package core_test

import (
	"net/url"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/search"
)

func listRowidDesc(t *testing.T, app core.App, name string) error {
	c, err := app.FindCollectionByNameOrId(name)
	if err != nil {
		return err
	}
	r := core.NewRecordFieldResolver(app, c, &core.RequestInfo{Context: core.RequestInfoContextDefault}, true)
	q := url.Values{"page": {"1"}, "perPage": {"40"}, "skipTotal": {"1"}, "sort": {"-@rowid"}}
	recs := []*core.Record{}
	_, err = search.NewProvider(r).Query(app.RecordQuery(c)).ParseAndExec(q.Encode(), &recs)
	t.Log("records:", len(recs))
	return err
}

// Regression: dropping a column must not break cached "SELECT table.*" queries
// ("cached plan must not change result type") on PostgreSQL.
func TestListAfterDroppingColumns(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	c := core.NewAuthCollection("members")
	c.Fields.Add(
		&core.TextField{Name: "username", Required: true},
		&core.BoolField{Name: "current"},
		&core.SelectField{Name: "type", MaxSelect: 1, Values: []string{"a", "b"}},
		&core.TextField{Name: "first_name"},
		&core.FileField{Name: "avatar", MaxSelect: 1, MaxSize: 100000},
		&core.AutodateField{Name: "created", OnCreate: true},
		&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
	)
	c.Indexes = []string{"CREATE UNIQUE INDEX `idx_u` ON `members` (username COLLATE NOCASE)"}
	if err := app.Save(c); err != nil {
		t.Fatal("create:", err)
	}
	rec := core.NewRecord(c)
	rec.Set("email", "a@b.co")
	rec.Set("username", "bob")
	rec.SetPassword("1234567890")
	if err := app.Save(rec); err != nil {
		t.Fatal("rec:", err)
	}
	if err := listRowidDesc(t, app, "members"); err != nil {
		t.Fatal("list before:", err)
	}
	for _, n := range []string{"current", "type", "first_name", "avatar"} {
		c, _ = app.FindCollectionByNameOrId("members")
		c.Fields.RemoveByName(n)
		if err := app.Save(c); err != nil {
			t.Errorf("drop %s: %v", n, err)
			continue
		}
		if err := listRowidDesc(t, app, "members"); err != nil {
			t.Errorf("list after drop %s: %v", n, err)
		}
	}
}
