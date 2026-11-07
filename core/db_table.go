package core

import (
	"database/sql"
	"fmt"
	"github.com/pocketbase/dbx"
)

type TableInfoRow struct {
	PK           int
	Index        int            `db:"cid"`
	Name         string         `db:"name"`
	Type         string         `db:"type"`
	NotNull      bool           `db:"notnull"`
	DefaultValue sql.NullString `db:"dflt_value"`
}

func (app *BaseApp) schemaExpr() string {
	if app.SQLDialect() == DialectMySQL {
		return "DATABASE()"
	}
	return "current_schema()"
}
func (app *BaseApp) TableColumns(name string) ([]string, error) {
	rows, err := app.TableInfo(name)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(rows))
	for i, r := range rows {
		result[i] = r.Name
	}
	return result, nil
}
func (app *BaseApp) TableInfo(name string) ([]*TableInfoRow, error) {
	rows := []*TableInfoRow{}
	query := `SELECT ordinal_position AS cid, column_name AS name, data_type AS type, (is_nullable = 'NO') AS "notnull", column_default AS dflt_value FROM information_schema.columns WHERE table_schema = ` + app.schemaExpr() + ` AND table_name = {:name} ORDER BY ordinal_position`
	if app.SQLDialect() == DialectMySQL {
		query = `SELECT ordinal_position AS cid, column_name AS name, data_type AS type, (is_nullable = 'NO') AS ` + "`notnull`" + `, column_default AS dflt_value, (column_key = 'PRI') AS pk FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = {:name} ORDER BY ordinal_position`
	}
	err := app.ConcurrentDB().NewQuery(query).Bind(dbx.Params{"name": name}).All(&rows)
	if err == nil && len(rows) == 0 {
		err = fmt.Errorf("table %q not found", name)
	}
	return rows, err
}
func (app *BaseApp) TableIndexes(name string) (map[string]string, error) {
	rows := []struct {
		Name string
		SQL  string
	}{}
	query := `SELECT indexname AS name, indexdef AS sql FROM pg_indexes WHERE schemaname = current_schema() AND tablename = {:name}`
	if app.SQLDialect() == DialectMySQL {
		query = "SELECT index_name AS name, concat('CREATE ', IF(min(non_unique)=0, 'UNIQUE ', ''), 'INDEX `', index_name, '` ON `', table_name, '` (', group_concat(concat('`',column_name,'`') ORDER BY seq_in_index), ')') AS `sql` FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = {:name} AND index_name <> 'PRIMARY' GROUP BY index_name,table_name"
	}
	err := app.ConcurrentDB().NewQuery(query).Bind(dbx.Params{"name": name}).All(&rows)
	result := map[string]string{}
	for _, r := range rows {
		result[r.Name] = r.SQL
	}
	return result, err
}
func (app *BaseApp) DeleteTable(name string) error {
	_, err := app.DB().DropTable(name).Execute()
	return err
}
func (app *BaseApp) HasTable(name string) bool    { return app.hasTable(app.ConcurrentDB(), name) }
func (app *BaseApp) AuxHasTable(name string) bool { return app.hasTable(app.AuxConcurrentDB(), name) }
func (app *BaseApp) hasTable(db dbx.Builder, name string) bool {
	var count int
	err := db.NewQuery("SELECT count(*) FROM information_schema.tables WHERE table_schema = " + app.schemaExpr() + " AND lower(table_name) = lower({:name})").Bind(dbx.Params{"name": name}).Row(&count)
	return err == nil && count > 0
}
func (app *BaseApp) Vacuum() error    { return app.vacuum(app.NonconcurrentDB()) }
func (app *BaseApp) AuxVacuum() error { return app.vacuum(app.AuxNonconcurrentDB()) }
func (app *BaseApp) vacuum(db dbx.Builder) error {
	if app.SQLDialect() == DialectMySQL {
		return fmt.Errorf("use OPTIMIZE TABLE for individual MySQL tables")
	}
	_, err := db.NewQuery("VACUUM").Execute()
	return err
}
