package dbutils

import (
	"regexp"
	"strings"
)

// Regexp for columns and tables (the same as the one in dbx).
var selectRegex = regexp.MustCompile(`(?i:\s+as\s+|\s+)([\w\-_\.]+)$`)

// AliasOrIdentifier returns the alias from a column or table identifier.
// Returns the identifier unmodified if no alias was found.
func AliasOrIdentifier(columnOrTableIdentifier string) string {
	matches := selectRegex.FindStringSubmatch(columnOrTableIdentifier)

	if len(matches) > 0 && matches[1] != "" {
		return matches[1]
	}

	return columnOrTableIdentifier
}

// BracketColumnRef normalizes a table or column reference for use inside [[...]] brackets.
// It strips surrounding quotes and extracts aliases from SELECT expressions.
func BracketColumnRef(columnOrTableIdentifier string) string {
	return strings.ReplaceAll(AliasOrIdentifier(columnOrTableIdentifier), `"`, "")
}
