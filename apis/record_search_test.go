package apis_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/pocketbase/pocketbase/tests"
)

// The admin UI searches a collection with `field?~term` over all its fields. On
// PostgreSQL that needs the non-text columns (booleans, numbers, JSON, arrays, ...)
// cast to text, which the record field resolver marks.
func TestRecordsSearchAcrossAllFieldTypes(t *testing.T) {
	t.Parallel()

	superuserToken := "eyJhbGciOiJIUzI1NiJ9.eyJpZCI6InN5d2JoZWNuaDQ2cmhtMCIsInR5cGUiOiJhdXRoIiwiY29sbGVjdGlvbklkIjoicGJjXzMxNDI2MzU4MjMiLCJleHAiOjI1MjQ2MDQ0NjEsInJlZnJlc2hhYmxlIjp0cnVlfQ.UXgO3j-0BumcugrFjbd7j0M4MQvbrLggLlcu_YNGjoY"

	scenario := func(name, collection, filter string, expectedContent ...string) tests.ApiScenario {
		return tests.ApiScenario{
			Name:            name,
			Method:          http.MethodGet,
			URL:             "/api/collections/" + collection + "/records?filter=" + url.QueryEscape(filter),
			Headers:         map[string]string{"Authorization": superuserToken},
			ExpectedStatus:  200,
			ExpectedContent: append([]string{`"items":[`}, expectedContent...),
		}
	}

	scenarios := []tests.ApiScenario{
		scenario("auth collection: bool fields", "users", "(verified?~'a'||emailVisibility?~'a')"),
		scenario("bool", "demo1", "bool?~'a'"),
		scenario("number", "demo1", "number?~'a'"),
		scenario("json", "demo1", "json?~'a'"),
		scenario("multi select", "demo1", "select_many?~'a'"),
		scenario("multi relation", "demo1", "rel_many?~'a'"),
		scenario("multi file", "demo1", "file_many?~'a'"),
		scenario("geo point", "demo1", "point?~'a'"),
		scenario("not like on a bool", "demo1", "bool!~'zzz'"),
		scenario("a json path compared with a number", "demo1", "json.a~5"),
		scenario("the multi-field search of the admin UI", "demo1",
			"(id?~'a'||text?~'a'||bool?~'a'||url?~'a'||select_one?~'a'||select_many?~'a'||file_one?~'a'||file_many?~'a'||number?~'a'||email?~'a'||datetime?~'a'||json?~'a'||rel_one?~'a'||rel_many?~'a'||point?~'a'||created?~'a'||updated?~'a')"),
		scenario("the array length", "demo1", "select_many:length~1"),
	}

	for _, s := range scenarios {
		s.Test(t)
	}
}
