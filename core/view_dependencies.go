package core

import "github.com/pocketbase/dbx"

// detachDependentViews preserves PostgreSQL view definitions before replacing
// a view or altering its source columns. The caller must restore inside the
// same transaction, so incompatible definitions roll back the schema change.
func detachDependentViews(app App, name string) (func() error, error) {
	if app.SQLDialect() != DialectPostgres {
		return func() error { return nil }, nil
	}
	var views []struct {
		Name       string
		Definition string
		Depth      int
	}
	err := app.DB().NewQuery(`WITH RECURSIVE dependencies AS (
 SELECT DISTINCT c.oid, c.relname, 1 AS depth FROM pg_depend d
 JOIN pg_rewrite r ON r.oid=d.objid JOIN pg_class c ON c.oid=r.ev_class
 WHERE d.refobjid=to_regclass({:name}) AND c.oid<>d.refobjid AND c.relkind='v'
 UNION ALL
 SELECT DISTINCT c.oid,c.relname,p.depth+1 FROM dependencies p
 JOIN pg_depend d ON d.refobjid=p.oid JOIN pg_rewrite r ON r.oid=d.objid
 JOIN pg_class c ON c.oid=r.ev_class WHERE c.oid<>p.oid AND c.relkind='v' AND p.depth<32
 ) SELECT relname AS name, pg_get_viewdef(oid,true) AS definition, max(depth) AS depth
 FROM dependencies GROUP BY oid,relname ORDER BY depth,relname`).Bind(dbx.Params{"name": app.SQLDialect().Quote(name)}).All(&views)
	if err != nil {
		return nil, err
	}
	for i := len(views) - 1; i >= 0; i-- {
		if _, err = app.DB().NewQuery("DROP VIEW IF EXISTS " + app.SQLDialect().Quote(views[i].Name)).Execute(); err != nil {
			return nil, err
		}
	}
	return func() error {
		for _, v := range views {
			if _, err := app.DB().NewQuery("CREATE VIEW " + app.SQLDialect().Quote(v.Name) + " AS " + v.Definition).Execute(); err != nil {
				return err
			}
		}
		return nil
	}, nil
}
