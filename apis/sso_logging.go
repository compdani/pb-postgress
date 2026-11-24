package apis

import (
	"errors"
	"log/slog"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// Record only curated reasons. Raw errors and request data can contain OAuth
// credentials, provider responses, or submitted passwords.
func logFailedSSOError(e *core.RequestEvent, collection *core.Collection, stage, provider string, err error) {
	if err == nil {
		return
	}
	reason := "An internal error prevented sign-in."
	var apiErr *router.ApiError
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		reason = apiErr.Message
	}
	logFailedSSO(e, collection, stage, provider, reason)
}

func logFailedSSO(e *core.RequestEvent, collection *core.Collection, stage, provider, reason string) {
	settings := e.App.Settings().Logs
	if !settings.LogFailedSSO || settings.MaxDays == 0 || e.Get("__failedSSOLogged") != nil {
		return
	}
	e.Set("__failedSSOLogged", true)
	attrs := []any{
		slog.String("type", "sso_login_failure"),
		slog.String("stage", stage),
		slog.String("reason", cutStr(reason, 500)),
		slog.String("method", e.Request.Method),
	}
	if collection != nil {
		attrs = append(attrs, slog.String("collection", collection.Name))
	}
	if provider != "" {
		attrs = append(attrs, slog.String("provider", cutStr(provider, 100)))
	}
	if settings.LogIP {
		attrs = append(attrs, slog.String("userIP", e.RealIP()), slog.String("remoteIP", e.RemoteIP()))
	}
	e.App.Logger().Warn("SSO login failed", attrs...)
}
