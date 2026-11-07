package apis_test

import (
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestInstallerStatusIgnoresTemporaryInstallerSuperuser(t *testing.T) {
	t.Parallel()

	scenarios := []tests.ApiScenario{
		{
			Name:   "only the temporary installer superuser exists",
			Method: http.MethodGet,
			URL:    "/api/installer/status",
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				if _, err := app.DB().NewQuery("DELETE FROM {{" + core.CollectionNameSuperusers + "}}").Execute(); err != nil {
					t.Fatal(err)
				}
				c, err := app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
				if err != nil {
					t.Fatal(err)
				}
				r := core.NewRecord(c)
				r.SetEmail(core.DefaultInstallerEmail)
				r.SetRandomPassword()
				if err := app.Save(r); err != nil {
					t.Fatal(err)
				}
			},
			ExpectedStatus:     200,
			ExpectedContent:    []string{`"installed":false`},
			NotExpectedContent: []string{`"fileServer"`, `"fileMode"`},
			ExpectedEvents:     map[string]int{"*": 0},
		},
		{
			Name:            "a real superuser exists",
			Method:          http.MethodGet,
			URL:             "/api/installer/status",
			ExpectedStatus:  200,
			ExpectedContent: []string{`"installed":true`},
			ExpectedEvents:  map[string]int{"*": 0},
		},
	}

	for _, scenario := range scenarios {
		scenario.Test(t)
	}
}
