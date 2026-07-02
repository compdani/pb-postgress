package apis

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

func bindCollectionPostgresApi(app core.App, rg *router.RouterGroup[*core.RequestEvent]) {
	subGroup := rg.Group("/collections").Bind(RequireSuperuserAuth())
	subGroup.GET("/postgres/tables", postgresTablesList)
	subGroup.GET("/postgres/tables/{schema}/{table}", postgresTableView)
	subGroup.POST("/import/postgres", postgresTableImport)
	subGroup.POST("/import/postgres/refresh", postgresTableRefresh)
}

func postgresTablesList(e *core.RequestEvent) error {
	baseApp, ok := e.App.(*core.BaseApp)
	if !ok || !baseApp.HasPostgres() {
		return e.BadRequestError("Postgres is not configured.", nil)
	}

	tables, err := baseApp.ListPostgresTables(e.Request.URL.Query().Get("search"))
	if err != nil {
		return e.BadRequestError("Failed to list postgres tables.", err)
	}

	return e.JSON(http.StatusOK, map[string]any{"tables": tables})
}

func postgresTableView(e *core.RequestEvent) error {
	baseApp, ok := e.App.(*core.BaseApp)
	if !ok || !baseApp.HasPostgres() {
		return e.BadRequestError("Postgres is not configured.", nil)
	}

	schema := e.Request.PathValue("schema")
	table := e.Request.PathValue("table")
	live := e.Request.URL.Query().Get("live") == "true"

	preview, err := baseApp.GetPostgresTablePreview(schema, table, live)
	if err != nil {
		return e.BadRequestError("Failed to introspect postgres table.", err)
	}

	return e.JSON(http.StatusOK, preview)
}

func postgresTableRefresh(e *core.RequestEvent) error {
	baseApp, ok := e.App.(*core.BaseApp)
	if !ok || !baseApp.HasPostgres() {
		return e.BadRequestError("Postgres is not configured.", nil)
	}

	form := struct {
		Schema string `form:"schema" json:"schema"`
		Table  string `form:"table" json:"table"`
	}{}

	if err := e.BindBody(&form); err != nil {
		return e.BadRequestError("Invalid refresh payload.", err)
	}

	collection, err := baseApp.RefreshPostgresTableSchemaByTable(form.Schema, form.Table)
	if err != nil {
		return e.BadRequestError("Failed to refresh postgres table schema.", err)
	}

	return e.JSON(http.StatusOK, map[string]any{
		"collectionId":   collection.Id,
		"collectionName": collection.Name,
	})
}

func postgresTableImport(e *core.RequestEvent) error {
	baseApp, ok := e.App.(*core.BaseApp)
	if !ok || !baseApp.HasPostgres() {
		return e.BadRequestError("Postgres is not configured.", nil)
	}

	form := struct {
		Schema         string `form:"schema" json:"schema"`
		Table          string `form:"table" json:"table"`
		CollectionName string `form:"collectionName" json:"collectionName"`
		Type           string `form:"type" json:"type"`
		DryRun         bool   `form:"dryRun" json:"dryRun"`
		S3Files        *bool  `form:"s3Files" json:"s3Files"`
	}{}

	if err := e.BindBody(&form); err != nil {
		return e.BadRequestError("Invalid import payload.", err)
	}

	collection, err := baseApp.ImportPostgresTable(core.PostgresImportConfig{
		Schema:         form.Schema,
		Table:          form.Table,
		CollectionName: form.CollectionName,
		Type:           form.Type,
		DryRun:         form.DryRun,
		S3Files:        form.S3Files,
	})
	if err != nil {
		return e.BadRequestError("Failed to import postgres table.", err)
	}

	return e.JSON(http.StatusOK, collection)
}
