package apis

import (
	"fmt"
	"net/http"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/dbutils"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/pocketbase/pocketbase/tools/search"
)

// bindLogsApi registers the request logs api endpoints.
func bindLogsApi(app core.App, rg *router.RouterGroup[*core.RequestEvent]) {
	sub := rg.Group("/logs").Bind(
		RequireSuperuserAuth(),
		SkipSuccessActivityLog(),
	)

	sub.GET("", logsList)
	sub.DELETE("", logsTruncate)
	sub.GET("/stats", logsStats)
	sub.GET("/{id}", logsView)
}

var logFilterFields = []string{
	"id", "created", "level", "message", "data",
	`^data\.[\w\.\:]*\w+$`,
}

// logFieldResolver adds a portable "@rowid" sort for the logs table
// (PostgreSQL and MySQL have no SQLite-style _rowid_ column).
type logFieldResolver struct {
	*search.SimpleFieldResolver
}

// RowidSortExpr implements [search.RowidSortResolver].
func (r *logFieldResolver) RowidSortExpr(direction string) (string, error) {
	return fmt.Sprintf("[[created]] %s, [[id]] %s", direction, direction), nil
}

// resolveLogColumn resolves a logs field. The level (integer) and the data (JSON)
// are not text columns, and PostgreSQL has no LIKE for them unless they are cast
// (the admin UI searches level, message and data at once).
func resolveLogColumn(resolver *search.SimpleFieldResolver, field string) (*search.ResolverResult, error) {
	result, err := resolver.Resolve(field)
	if err == nil && (field == "level" || field == "data") {
		result.LikeAsText = true
	}
	return result, err
}

// Resolve implements [search.FieldResolver].
func (r *logFieldResolver) Resolve(field string) (*search.ResolverResult, error) {
	return resolveLogColumn(r.SimpleFieldResolver, field)
}

// logStatsFieldResolver is the field resolver of the logs stats.
type logStatsFieldResolver struct {
	*search.SimpleFieldResolver
}

// Resolve implements [search.FieldResolver].
func (r *logStatsFieldResolver) Resolve(field string) (*search.ResolverResult, error) {
	return resolveLogColumn(r.SimpleFieldResolver, field)
}

func logsList(e *core.RequestEvent) error {
	fieldResolver := &logFieldResolver{search.NewSimpleFieldResolver(logFilterFields...)}
	if e.App.SQLDialect() == core.DialectMySQL {
		fieldResolver.JSONExtract = dbutils.JSONExtractMySQL
	}

	result, err := search.NewProvider(fieldResolver).
		Query(e.App.AuxModelQuery(&core.Log{})).
		ParseAndExec(e.Request.URL.Query().Encode(), &[]*core.Log{})

	if err != nil {
		return e.BadRequestError("", err)
	}

	return e.JSON(http.StatusOK, result)
}

func logsStats(e *core.RequestEvent) error {
	base := search.NewSimpleFieldResolver(logFilterFields...)
	if e.App.SQLDialect() == core.DialectMySQL {
		base.JSONExtract = dbutils.JSONExtractMySQL
	}
	fieldResolver := &logStatsFieldResolver{base}

	filter := e.Request.URL.Query().Get(search.FilterQueryParam)

	var expr dbx.Expression
	if filter != "" {
		var err error
		expr, err = search.FilterData(filter).BuildExpr(fieldResolver)
		if err != nil {
			return e.BadRequestError("Invalid filter format.", err)
		}
	}

	stats, err := e.App.LogsStats(expr)
	if err != nil {
		return e.BadRequestError("Failed to generate logs stats.", err)
	}

	return e.JSON(http.StatusOK, stats)
}

func logsView(e *core.RequestEvent) error {
	id := e.Request.PathValue("id")
	if id == "" {
		return e.NotFoundError("", nil)
	}

	log, err := e.App.FindLogById(id)
	if err != nil || log == nil {
		return e.NotFoundError("", err)
	}

	return e.JSON(http.StatusOK, log)
}

func logsTruncate(e *core.RequestEvent) error {
	// delete all rows directly (aka. no model hooks will be fired)
	_, err := e.App.AuxNonconcurrentDB().Delete((&core.Log{}).TableName(), nil).Execute()
	if err != nil {
		return e.InternalServerError("Failed to truncate all logs.", err)
	}

	// try to free the unused disk space
	err = e.App.AuxVacuum()
	if err != nil {
		e.App.Logger().Warn("Failed to VACUUM aux database", "error", err)
	}

	return e.NoContent(http.StatusNoContent)
}
