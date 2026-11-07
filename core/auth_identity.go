package core

import (
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/dbutils"
)

// AuthIdentityExpression uses the same expression as the configured unique
// index, including the optional-email functional index on MySQL.
func AuthIdentityExpression(app App, index dbutils.Index, field, parameter string, value any) dbx.Expression {
	column := "[[" + field + "]]"
	if !strings.EqualFold(index.Columns[0].Collate, "nocase") {
		if app.SQLDialect() == DialectMySQL {
			return dbx.NewExp(column+" = BINARY {:"+parameter+"}", dbx.Params{parameter: value})
		}
		return dbx.NewExp(column+" = {:"+parameter+"}", dbx.Params{parameter: value})
	}
	expression := "LOWER(" + column + ")"
	if app.SQLDialect() == DialectMySQL && strings.TrimSpace(index.Where) != "" {
		expression = "NULLIF(" + expression + ", '')"
	}
	result := dbx.NewExp(expression+" = LOWER({:"+parameter+"})", dbx.Params{parameter: value})
	if app.SQLDialect() == DialectPostgres && index.Where != "" {
		return dbx.And(result, dbx.NewExp(strings.ReplaceAll(index.Where, "`", "\"")))
	}
	return result
}
