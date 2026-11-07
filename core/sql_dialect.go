package core

import "strings"

type SQLDialect int

const (
	DialectPostgres SQLDialect = iota
	DialectMySQL
)

func (app *BaseApp) CollectionDialect(c *Collection) SQLDialect { return app.SQLDialect() }
func (app *BaseApp) SQLDialect() SQLDialect {
	if app.config.DBDriver == "mysql" {
		return DialectMySQL
	}
	return DialectPostgres
}
func (d SQLDialect) Quote(name string) string {
	q := "\""
	if d == DialectMySQL {
		q = "`"
	}
	return q + strings.ReplaceAll(name, q, q+q) + q
}
