package apis

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

type ssoLoggingApp struct {
	core.App
	logger *slog.Logger
}

func (a *ssoLoggingApp) Logger() *slog.Logger { return a.logger }

func TestFailedSSOLogging(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		enabled, retained, ip bool
	}{
		{"disabled", false, true, false},
		{"no retention", true, false, false},
		{"enabled", true, true, false},
		{"with IP", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			app := &ssoLoggingApp{App: core.NewBaseApp(core.BaseAppConfig{}), logger: slog.New(slog.NewJSONHandler(&output, nil))}
			app.Settings().Logs.LogFailedSSO = tc.enabled
			app.Settings().Logs.MaxDays = 0
			if tc.retained {
				app.Settings().Logs.MaxDays = 7
			}
			app.Settings().Logs.LogIP = tc.ip
			e := &core.RequestEvent{App: app}
			e.Request = httptest.NewRequest("POST", "/?code=secret-code", strings.NewReader("password=secret-password"))
			e.Request.RemoteAddr = "192.0.2.1:1234"
			logFailedSSOError(e, nil, "provider_callback", "oidc", router.NewBadRequestError("Failed to fetch OAuth2 token.", errors.New("secret-token")))
			logFailedSSO(e, nil, "fallback", "", "duplicate failure")
			got := output.String()
			if !tc.enabled || !tc.retained {
				if got != "" {
					t.Fatalf("unexpected log: %s", got)
				}
				return
			}
			for _, want := range []string{`"type":"sso_login_failure"`, `"level":"WARN"`, `"stage":"provider_callback"`, `"provider":"oidc"`, `"reason":"Failed to fetch OAuth2 token."`} {
				if !strings.Contains(got, want) {
					t.Errorf("missing %s in %s", want, got)
				}
			}
			for _, secret := range []string{"secret-code", "secret-password", "secret-token", "duplicate failure"} {
				if strings.Contains(got, secret) {
					t.Errorf("unexpected %q in log", secret)
				}
			}
			if strings.Contains(got, `"userIP"`) != tc.ip {
				t.Errorf("IP logging mismatch: %s", got)
			}
		})
	}
}
