package dbutils_test

import (
	"github.com/pocketbase/pocketbase/tools/dbutils"
	"strings"
	"testing"
)

func TestJSONServerDialects(t *testing.T) {
	for _, column := range []string{"field", "a.b"} {
		if got := dbutils.JSONEach(column); got != dbutils.JSONEachPostgres(column) {
			t.Fatal("default JSON dialect must be postgres")
		}
		if got := dbutils.JSONArrayLength(column); !strings.Contains(got, "jsonb_array_length") {
			t.Fatal(got)
		}
		if got := dbutils.JSONEachMySQL(column); !strings.Contains(got, "JSON_TABLE(") {
			t.Fatal(got)
		}
		if got := dbutils.JSONArrayLengthMySQL(column); !strings.Contains(got, "JSON_LENGTH(") {
			t.Fatal(got)
		}
	}
	got := dbutils.JSONExtractPostgres("field", "[1].a[2]")
	if !strings.Contains(got, "ARRAY['1','a','2']") {
		t.Fatal(got)
	}
	got = dbutils.JSONExtractMySQL("field", "a.b")
	if !strings.Contains(got, "$.a.b") {
		t.Fatal(got)
	}
}
