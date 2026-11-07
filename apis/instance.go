package apis

import (
	"database/sql"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"net/http"
)

func scopedCollection(app core.App, name string) (*core.Collection, error) {
	c, err := app.FindCachedCollectionByNameOrId(name)
	if err != nil {
		return nil, err
	}
	if ba := core.AsBaseApp(app); ba != nil && !ba.CollectionVisible(c) {
		return nil, sql.ErrNoRows
	}
	return c, nil
}
func bindInstanceAPI(app core.App, rg *router.RouterGroup[*core.RequestEvent]) {
	rg.GET("/instance/scope", func(e *core.RequestEvent) error {
		scope, err := core.AsBaseApp(e.App).InstanceScope()
		if err != nil {
			return err
		}
		return e.JSON(http.StatusOK, scope)
	}).Bind(RequireSuperuserAuth())
	rg.PUT("/instance/scope", func(e *core.RequestEvent) error {
		var scope core.InstanceScope
		if err := e.BindBody(&scope); err != nil {
			return e.BadRequestError("Invalid scope", err)
		}
		if err := core.AsBaseApp(e.App).SetInstanceScope(scope); err != nil {
			return e.BadRequestError("Invalid scope", err)
		}
		return e.JSON(http.StatusOK, scope)
	}).Bind(RequireSuperuserAuth())
	rg.GET("/instances", func(e *core.RequestEvent) error {
		rows, err := core.AsBaseApp(e.App).Instances()
		if err != nil {
			return err
		}
		return e.JSON(http.StatusOK, rows)
	}).Bind(RequireSuperuserAuth())
}
