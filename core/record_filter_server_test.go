package core_test

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/search"
	"testing"
)

func TestRecordFilterRequestEachExecution(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()
	c, err := app.FindCollectionByNameOrId("demo1")
	if err != nil {
		t.Fatal(err)
	}
	resolver := core.NewRecordFieldResolver(app, c, &core.RequestInfo{Body: map[string]any{"select_many": []string{"a"}}}, true)
	expr, err := search.FilterData("@request.body.select_many:each = @request.body.select_many:each").BuildExpr(resolver)
	if err != nil {
		t.Fatal(err)
	}
	q := app.RecordQuery(c).AndWhere(expr).Limit(1)
	if err := resolver.UpdateQuery(q); err != nil {
		t.Fatal(err)
	}
	var record core.Record
	if err := q.One(&record); err != nil {
		t.Fatal(err)
	}
}
