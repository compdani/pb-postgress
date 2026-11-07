package dbutils

import (
	"fmt"
	"strings"
)

func mysqlJSONArray(column string) string {
	c := "[[" + column + "]]"
	return "CASE WHEN JSON_VALID(" + c + ") AND JSON_TYPE(CAST(" + c + " AS JSON)) = 'ARRAY' THEN CAST(" + c + " AS JSON) WHEN " + c + " IS NULL OR " + c + " = '' THEN JSON_ARRAY() ELSE JSON_ARRAY(" + c + ") END"
}

// JSONEachMySQL requires MySQL 8.0's JSON_TABLE.
func JSONEachMySQL(column string) string {
	return "JSON_TABLE(" + mysqlJSONArray(column) + ", '$[*]' COLUMNS(value VARCHAR(4096) PATH '$'))"
}
func JSONArrayLengthMySQL(column string) string { return "JSON_LENGTH(" + mysqlJSONArray(column) + ")" }
func JSONExtractMySQL(column, path string) string {
	if path != "" && !strings.HasPrefix(path, "[") {
		path = "." + path
	}
	path = strings.ReplaceAll(path, "'", "''")
	return fmt.Sprintf("JSON_UNQUOTE(JSON_EXTRACT(CASE WHEN JSON_VALID([[%s]]) THEN CAST([[%s]] AS JSON) ELSE JSON_OBJECT('pb', [[%s]]) END, '$%s'))", column, column, column, path)
}
