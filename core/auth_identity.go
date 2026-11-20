package core

import (
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/dbutils"
)

// AuthIdentityExpression uses the same expression as the configured unique
// index.
func AuthIdentityExpression(app App, index dbutils.Index, field, parameter string, value any) dbx.Expression {
	column := "[[" + field + "]]"
	if !strings.EqualFold(index.Columns[0].Collate, "nocase") {
		return dbx.NewExp(column+" = {:"+parameter+"}", dbx.Params{parameter: value})
	}
	result := dbx.NewExp("LOWER("+column+") = LOWER({:"+parameter+"})", dbx.Params{parameter: value})
	if index.Where != "" {
		return dbx.And(result, dbx.NewExp(strings.ReplaceAll(index.Where, "`", "\"")))
	}
	return result
}
