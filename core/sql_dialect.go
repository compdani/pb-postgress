package core

import "strings"

// SQLDialect identifies the SQL dialect of the app database.
//
// PostgreSQL is the only supported database, so [DialectPostgres] is the
// only value.
type SQLDialect int

const (
	DialectPostgres SQLDialect = iota
)

func (app *BaseApp) CollectionDialect(c *Collection) SQLDialect { return app.SQLDialect() }
func (app *BaseApp) SQLDialect() SQLDialect                     { return DialectPostgres }

// Quote quotes an identifier.
func (d SQLDialect) Quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// dialectName returns the database driver name.
func (app *BaseApp) dialectName() string {
	return "postgres"
}
