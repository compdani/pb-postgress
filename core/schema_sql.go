package core

import (
	"github.com/pocketbase/pocketbase/tools/dbutils"
	"github.com/pocketbase/pocketbase/tools/security"
	"strings"
)

func fieldColumnType(app App, typ string) string {
	return strings.ReplaceAll(typ, "JSON", "JSONB")
}

// ExecuteSchema executes the semicolon separated DDL statements one by one
// (the extended query protocol runs a single statement per query).
func ExecuteSchema(app App, sql string) error {
	sql = strings.ReplaceAll(sql, "JSON", "JSONB")
	for _, statement := range strings.Split(sql, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := app.DB().NewQuery(statement).Execute(); err != nil {
			return err
		}
	}
	return nil
}

func indexDropSQL(app App, table, name string) string {
	return "DROP INDEX IF EXISTS " + asBaseApp(app).SQLDialect().Quote(name)
}

func indexCreateSQL(app App, idx dbutils.Index) (string, error) {
	d := asBaseApp(app).SQLDialect()
	for i, col := range idx.Columns {
		if strings.EqualFold(col.Collate, "nocase") {
			idx.Columns[i].Collate = ""
			idx.Columns[i].Name = "LOWER(" + d.Quote(strings.Trim(col.Name, "`\"")) + ")"
		}
	}
	idx.Optional = false
	return strings.ReplaceAll(idx.Build(), "`", `"`), nil
}

func appJSONEach(app App, column string) string {
	return dbutils.JSONEachPostgres(column)
}

func normalizeSingleVsMultipleFieldChanges(app App, current, old *Collection) error {
	if old == nil {
		return nil
	}
	for _, field := range current.Fields {
		previous := old.Fields.GetById(field.GetId())
		if previous == nil {
			continue
		}
		multi, ok := field.(MultiValuer)
		if !ok {
			continue
		}
		before, ok := previous.(MultiValuer)
		if !ok || before.IsMultiple() == multi.IsMultiple() {
			continue
		}
		d := asBaseApp(app).SQLDialect()
		name := field.GetName()
		temp := "_pb_old_" + security.RandomString(10)
		if _, err := app.DB().RenameColumn(current.Name, name, temp).Execute(); err != nil {
			return err
		}
		if _, err := app.DB().AddColumn(current.Name, name, field.ColumnType(app)).Execute(); err != nil {
			return err
		}
		column := d.Quote(temp)
		var expression string
		if multi.IsMultiple() {
			expression = "CASE WHEN " + column + " = '' OR " + column + " IS NULL THEN '[]'::jsonb ELSE jsonb_build_array(" + column + ") END"
		} else {
			expression = "COALESCE(" + column + "::jsonb ->> -1, '')"
		}
		if _, err := app.DB().NewQuery("UPDATE " + d.Quote(current.Name) + " SET " + d.Quote(name) + " = " + expression).Execute(); err != nil {
			return err
		}
		if _, err := app.DB().DropColumn(current.Name, temp).Execute(); err != nil {
			return err
		}
	}
	return nil
}
