package core_test

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"testing"
)

func TestSaveViewPreservesDependencies(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()
	if app.SQLDialect() != core.DialectPostgres {
		t.Skip("transactional PostgreSQL view dependency handling")
	}
	if err := app.SaveView("dependency_source", "select id from demo1"); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveView("dependency_child", "select id from dependency_source"); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveView("dependency_source", "select id from demo1 where id <> ''"); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := app.DB().Select("count(*)").From("dependency_child").Row(&before); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveView("dependency_source", "select 1 as different_column"); err == nil {
		t.Fatal("expected incompatible dependent view error")
	}
	var after int
	if err := app.DB().Select("count(*)").From("dependency_child").Row(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("failed replacement changed dependent view: %d -> %d", before, after)
	}
}
