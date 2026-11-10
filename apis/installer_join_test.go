package apis_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestInstallerJoinExistingDatabase(t *testing.T) {
	t.Parallel()

	pend := func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
		if _, err := app.InstanceID(); err != nil {
			t.Fatal(err)
		}
		if err := app.SetInstanceSetupPending(true); err != nil {
			t.Fatal(err)
		}
	}
	headers := map[string]string{"X-Installer-Token": apis.InstallerToken()}

	scenarios := []tests.ApiScenario{
		{
			Name:            "pending setup reports the existing database",
			Method:          http.MethodGet,
			URL:             "/api/installer/status",
			Headers:         headers,
			BeforeTestFunc:  pend,
			ExpectedStatus:  200,
			ExpectedContent: []string{`"existing":true`, `"joinPending":true`, `"installed":false`, `"instances":[]`},
			ExpectedEvents:  map[string]int{"*": 0},
		},
		{
			Name:            "join as a clean new instance",
			Method:          http.MethodPost,
			URL:             "/api/installer/join",
			Headers:         headers,
			Body:            strings.NewReader(`{"mode":"new","start":"clean","name":"events","url":"http://events.test"}`),
			BeforeTestFunc:  pend,
			ExpectedStatus:  200,
			ExpectedContent: []string{`"ok":true`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if app.InstanceSetupPending() {
					t.Fatal("expected the pending setup to be cleared")
				}
				scope, err := app.InstanceScope()
				if err != nil || scope.Mode != "allow" || len(scope.Collections) != 0 {
					t.Fatalf("unexpected scope %+v %v", scope, err)
				}
			},
		},
		{
			Name:            "join without a pending setup is rejected",
			Method:          http.MethodPost,
			URL:             "/api/installer/join",
			Headers:         headers,
			Body:            strings.NewReader(`{"mode":"new","start":"clean"}`),
			ExpectedStatus:  403,
			ExpectedContent: []string{`"ok":false`},
			ExpectedEvents:  map[string]int{"*": 0},
		},
	}

	for _, scenario := range scenarios {
		scenario.Test(t)
	}
}
