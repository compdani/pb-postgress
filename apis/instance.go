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
	// Lists every collection in the shared database, including the ones this
	// instance cannot see, so the scope selector can offer them.
	rg.GET("/instance/collections", func(e *core.RequestEvent) error {
		all, err := e.App.FindAllCollections()
		if err != nil {
			return err
		}
		type item struct {
			Id     string `json:"id"`
			Name   string `json:"name"`
			Type   string `json:"type"`
			System bool   `json:"system"`
		}
		items := make([]item, 0, len(all))
		for _, c := range all {
			items = append(items, item{c.Id, c.Name, c.Type, c.System})
		}
		return e.JSON(http.StatusOK, items)
	}).Bind(RequireSuperuserAuth())
	rg.PUT("/instance/scope", func(e *core.RequestEvent) error {
		var scope core.InstanceScope
		if err := e.BindBody(&scope); err != nil {
			return e.BadRequestError("Invalid scope", err)
		}
		ba := core.AsBaseApp(e.App)
		if err := ba.SetInstanceScope(scope); err != nil {
			return e.BadRequestError("Invalid scope: "+err.Error(), err)
		}
		// the stored scope can differ from the request (stale names are dropped)
		saved, err := ba.InstanceScope()
		if err != nil {
			return err
		}
		return e.JSON(http.StatusOK, saved)
	}).Bind(RequireSuperuserAuth())
	rg.POST("/instance/settings", func(e *core.RequestEvent) error {
		body := struct {
			CopyFrom string `json:"copyFrom"`
		}{}
		if err := e.BindBody(&body); err != nil {
			return e.BadRequestError("Invalid body", err)
		}
		if err := e.App.EnableInstanceSettings(body.CopyFrom); err != nil {
			return e.BadRequestError("Failed to create the instance settings", err)
		}
		return e.NoContent(http.StatusNoContent)
	}).Bind(RequireSuperuserAuth())
	rg.DELETE("/instance/settings", func(e *core.RequestEvent) error {
		if err := e.App.DisableInstanceSettings(); err != nil {
			return e.BadRequestError("Failed to remove the instance settings", err)
		}
		return e.NoContent(http.StatusNoContent)
	}).Bind(RequireSuperuserAuth())
	rg.GET("/instances", func(e *core.RequestEvent) error {
		rows, err := core.AsBaseApp(e.App).Instances()
		if err != nil {
			return err
		}
		return e.JSON(http.StatusOK, rows)
	}).Bind(RequireSuperuserAuth())
}
