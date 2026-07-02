package core

type SQLDialect int

const (
	DialectSQLite SQLDialect = iota
	DialectPostgres
)

func (app *BaseApp) CollectionDialect(c *Collection) SQLDialect {
	if app.IsPostgresBacked(c) {
		return DialectPostgres
	}
	return DialectSQLite
}
