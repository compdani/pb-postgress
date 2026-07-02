package dbutils

import (
	"fmt"
	"strings"
)

// JSONEachPostgres returns a jsonb_array_elements expression with
// normalizations for non-json columns.
func JSONEachPostgres(column string) string {
	return fmt.Sprintf(
		`jsonb_array_elements(CASE WHEN jsonb_typeof(([[%s]])::jsonb) = 'array' THEN ([[%s]])::jsonb ELSE jsonb_build_array([[%s]]) END)`,
		column, column, column,
	)
}

// JSONArrayLengthPostgres returns jsonb_array_length expression
// with normalizations for non-json columns.
func JSONArrayLengthPostgres(column string) string {
	return fmt.Sprintf(
		`jsonb_array_length(CASE WHEN jsonb_typeof(([[%s]])::jsonb) = 'array' THEN ([[%s]])::jsonb WHEN [[%s]] = '' OR [[%s]] IS NULL THEN '[]'::jsonb ELSE jsonb_build_array([[%s]]) END)`,
		column, column, column, column, column,
	)
}

// JSONExtractPostgres returns a JSON path extract expression with
// normalizations for non-json columns.
func JSONExtractPostgres(column string, path string) string {
	if path != "" && !strings.HasPrefix(path, "[") {
		path = "." + path
	}

	if path == "" {
		return fmt.Sprintf(`([[%s]])::jsonb`, column)
	}

	// convert SQLite-style $.path to Postgres #>> '{path,parts}'
	path = strings.TrimPrefix(path, ".")
	pathParts := strings.Split(path, ".")
	pgPath := "{" + strings.Join(pathParts, ",") + "}"

	return fmt.Sprintf(
		`(CASE WHEN ([[%s]])::text ~ '^[\[\{]' THEN ([[%s]])::jsonb #>> '%s' ELSE ([[%s]])::text END)`,
		column,
		column,
		pgPath,
		column,
	)
}
