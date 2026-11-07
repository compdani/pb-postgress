package core

import (
	"fmt"
	"github.com/pocketbase/pocketbase/tools/dbutils"
	"github.com/pocketbase/pocketbase/tools/security"
	"strings"
)

func fieldColumnType(app App, typ string) string {
	ba := asBaseApp(app)
	if ba == nil || ba.SQLDialect() == DialectPostgres {
		return strings.ReplaceAll(typ, "JSON", "JSONB")
	}
	typ = strings.ReplaceAll(typ, "TEXT DEFAULT", "VARCHAR(255) DEFAULT")
	typ = strings.ReplaceAll(typ, "JSON DEFAULT '[]'", "JSON DEFAULT ('[]')")
	typ = strings.ReplaceAll(typ, "JSON DEFAULT '{}'", "JSON DEFAULT ('{}')")
	return typ
}

// ExecuteSchema executes individual DDL statements without enabling multiStatements on MySQL connections.
func ExecuteSchema(app App, sql string) error {
	ba := asBaseApp(app)
	if ba != nil && ba.SQLDialect() == DialectPostgres {
		sql = strings.ReplaceAll(sql, "JSON", "JSONB")
	}
	for _, statement := range strings.Split(sql, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if ba != nil && ba.SQLDialect() == DialectMySQL {
			statement = strings.ReplaceAll(statement, "INDEX IF NOT EXISTS", "INDEX")
		}
		if _, err := app.DB().NewQuery(statement).Execute(); err != nil {
			return err
		}
	}
	return nil
}

func indexDropSQL(app App, table, name string) string {
	ba := asBaseApp(app)
	sql := "DROP INDEX "
	if ba.SQLDialect() == DialectPostgres {
		sql += "IF EXISTS "
	}
	sql += ba.SQLDialect().Quote(name)
	if ba.SQLDialect() == DialectMySQL {
		sql += " ON " + ba.SQLDialect().Quote(table)
	}
	return sql
}

func indexCreateSQL(app App, idx dbutils.Index) (string, error) {
	d := asBaseApp(app).SQLDialect()
	if d == DialectMySQL && idx.Where != "" {
		if len(idx.Columns) == 1 && idx.Unique {
			col := strings.Trim(idx.Columns[0].Name, "`\"")
			normalized := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(idx.Where), "`", ""), "\"", "")
			if normalized == col+" != ''" {
				return "CREATE UNIQUE INDEX " + d.Quote(idx.IndexName) + " ON " + d.Quote(idx.TableName) + " ((NULLIF(" + d.Quote(col) + ", '')))", nil
			}
		}
		return "", fmt.Errorf("MySQL does not support this partial index predicate")
	}
	idx.Optional = false
	raw := idx.Build()
	if d == DialectPostgres {
		raw = strings.ReplaceAll(raw, "`", `"`)
	}
	return raw, nil
}

func appJSONEach(app App, column string) string {
	if ba := asBaseApp(app); ba != nil && ba.SQLDialect() == DialectMySQL {
		return dbutils.JSONEachMySQL(column)
	}
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
			if d == DialectMySQL {
				expression = "CASE WHEN " + column + " = '' OR " + column + " IS NULL THEN JSON_ARRAY() ELSE JSON_ARRAY(" + column + ") END"
			} else {
				expression = "CASE WHEN " + column + " = '' OR " + column + " IS NULL THEN '[]'::jsonb ELSE jsonb_build_array(" + column + ") END"
			}
		} else {
			if d == DialectMySQL {
				expression = "COALESCE(JSON_UNQUOTE(JSON_EXTRACT(" + column + ", '$[last]')), '')"
			} else {
				expression = "COALESCE(" + column + "::jsonb ->> -1, '')"
			}
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
