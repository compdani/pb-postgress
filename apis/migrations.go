package apis

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// bindMigrationsApi registers the endpoints a superuser uses to review and
// apply the app migrations that were not applied yet.
func bindMigrationsApi(app core.App, rg *router.RouterGroup[*core.RequestEvent]) {
	sub := rg.Group("/migrations").Bind(RequireSuperuserAuth())
	sub.GET("/pending", migrationsPending)
	sub.POST("/preview", migrationsPreview)
	sub.POST("/apply", migrationsApply)
}

// migrationsPending lists the pending migration files without running them.
func migrationsPending(e *core.RequestEvent) error {
	ba := core.AsBaseApp(e.App)
	if ba == nil {
		return e.JSON(http.StatusOK, map[string]any{"files": []string{}})
	}
	files, err := ba.PendingAppMigrations()
	if err != nil {
		return e.InternalServerError("Failed to list the pending migrations.", err)
	}
	return e.JSON(http.StatusOK, map[string]any{"files": files})
}

// migrationsPreview dry-runs the pending migrations and reports the
// collections each one changes.
func migrationsPreview(e *core.RequestEvent) error {
	ba := core.AsBaseApp(e.App)
	if ba == nil {
		return e.BadRequestError("Migrations cannot be previewed.", nil)
	}
	preview, err := ba.PreviewPendingMigrations()
	if err != nil {
		return e.BadRequestError("Failed to preview the migrations: "+err.Error(), nil)
	}
	return e.JSON(http.StatusOK, preview)
}

func migrationsApply(e *core.RequestEvent) error {
	ba := core.AsBaseApp(e.App)
	if ba == nil {
		return e.BadRequestError("Migrations cannot be applied.", nil)
	}
	body := struct {
		// Ignore lists, per migration file, the collections (id or name)
		// whose changes are skipped.
		Ignore map[string][]string `json:"ignore"`
	}{}
	if err := e.BindBody(&body); err != nil {
		return e.BadRequestError("Invalid body.", err)
	}
	applied, err := ba.ApplyPendingMigrations(body.Ignore)
	if err != nil {
		return e.BadRequestError("Failed to apply the migrations: "+err.Error(), nil)
	}
	return e.JSON(http.StatusOK, map[string]any{"applied": applied})
}
