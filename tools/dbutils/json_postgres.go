package dbutils

import (
	"fmt"
	"regexp"
	"strings"
)

func postgresJSONArray(column string) string {
	c := "[[" + column + "]]"
	value := "CASE WHEN " + c + "::text ~ '^\\s*[\\[{]' THEN " + c + "::text::jsonb ELSE to_jsonb(" + c + ") END"
	return "CASE WHEN " + c + " IS NULL OR " + c + "::text = '' THEN '[]'::jsonb WHEN jsonb_typeof(" + value + ") = 'array' THEN " + value + " ELSE jsonb_build_array(" + c + ") END"
}
func JSONEachPostgres(column string) string {
	return "LATERAL (SELECT jsonb_array_elements_text(" + postgresJSONArray(column) + ") AS value)"
}
func JSONArrayLengthPostgres(column string) string {
	return "jsonb_array_length(" + postgresJSONArray(column) + ")"
}

var jsonPathPart = regexp.MustCompile(`[^.\[\]]+`)

func JSONExtractPostgres(column, path string) string {
	c := "[[" + column + "]]"
	if path == "" {
		return "to_jsonb(" + c + ")"
	}
	parts := jsonPathPart.FindAllString(path, -1)
	for i, p := range parts {
		parts[i] = "'" + strings.ReplaceAll(p, "'", "''") + "'"
	}
	return fmt.Sprintf("(CASE WHEN %s::text ~ '^\\s*[\\[{]' THEN %s::text::jsonb ELSE to_jsonb(%s) END #>> ARRAY[%s])", c, c, c, strings.Join(parts, ","))
}
