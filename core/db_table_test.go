package core_test

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestHasTable(t *testing.T) {
	t.Parallel()

	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	scenarios := []struct {
		tableName string
		expected  bool
	}{
		{"", false},
		{"test", false},
		{core.CollectionNameSuperusers, true},
		{"demo3", true},
		{"DEMO3", true}, // table names are case insensitives by default
		{"view1", true}, // view
	}

	for _, s := range scenarios {
		t.Run(s.tableName, func(t *testing.T) {
			result := app.HasTable(s.tableName)
			if result != s.expected {
				t.Fatalf("Expected %v, got %v", s.expected, result)
			}
		})
	}
}

func TestAuxHasTable(t *testing.T) {
	t.Parallel()

	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	scenarios := []struct {
		tableName string
		expected  bool
	}{
		{"", false},
		{"test", false},
		{"_lOGS", true}, // table names are case insensitives by default
	}

	for _, s := range scenarios {
		t.Run(s.tableName, func(t *testing.T) {
			result := app.AuxHasTable(s.tableName)
			if result != s.expected {
				t.Fatalf("Expected %v, got %v", s.expected, result)
			}
		})
	}
}

func TestTableColumns(t *testing.T) {
	t.Parallel()

	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	scenarios := []struct {
		tableName string
		expected  []string
	}{
		{"", nil},
		{"_params", []string{"id", "value", "created", "updated"}},
	}

	for i, s := range scenarios {
		t.Run(fmt.Sprintf("%d_%s", i, s.tableName), func(t *testing.T) {
			columns, _ := app.TableColumns(s.tableName)

			if len(columns) != len(s.expected) {
				t.Fatalf("Expected columns %v, got %v", s.expected, columns)
			}

			for _, c := range columns {
				if !slices.Contains(s.expected, c) {
					t.Errorf("Didn't expect column %s", c)
				}
			}
		})
	}
}

func TestTableInfo(t *testing.T) {
	t.Parallel()
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()
	for _, name := range []string{"", "missing"} {
		rows, err := app.TableInfo(name)
		if err == nil || len(rows) != 0 {
			t.Fatalf("expected missing table error for %q: %v, %v", name, rows, err)
		}
	}
	rows, err := app.TableInfo("_params")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("expected 4 columns, got %v", rows)
	}
	for i, row := range rows {
		if row.Index != i || row.Type == "" {
			t.Fatalf("invalid column metadata: %+v", row)
		}
		switch row.Name {
		case "id":
			if row.PK != 1 || !row.NotNull {
				t.Fatalf("invalid primary key: %+v", row)
			}
		case "value":
			if row.NotNull {
				t.Fatal("value must be nullable")
			}
		case "created", "updated":
			if !row.NotNull || !row.DefaultValue.Valid {
				t.Fatalf("invalid timestamp: %+v", row)
			}
		default:
			t.Fatalf("unexpected column: %+v", row)
		}
	}
}

func TestTableIndexes(t *testing.T) {
	t.Parallel()

	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	scenarios := []struct {
		tableName string
		expected  []string
	}{
		{"", nil},
		{"missing", nil},
		{
			core.CollectionNameSuperusers,
			[]string{"idx_email__pbc_3323866339", "idx_tokenKey__pbc_3323866339"},
		},
	}

	for i, s := range scenarios {
		t.Run(fmt.Sprintf("%d_%s", i, s.tableName), func(t *testing.T) {
			indexes, _ := app.TableIndexes(s.tableName)

			if len(indexes) != len(s.expected) {
				t.Fatalf("Expected %d indexes, got %d\n%v", len(s.expected), len(indexes), indexes)
			}

			for _, name := range s.expected {
				if v, ok := indexes[name]; !ok || v == "" {
					t.Fatalf("Expected non-empty index %q in \n%v", name, indexes)
				}
			}
		})
	}
}

func TestDeleteTable(t *testing.T) {
	t.Parallel()

	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	scenarios := []struct {
		tableName   string
		expectError bool
	}{
		{"", true},
		{"test", false}, // missing tables are ignored
		{"_admins", false},
		{"demo3", false},
	}

	for i, s := range scenarios {
		t.Run(fmt.Sprintf("%d_%s", i, s.tableName), func(t *testing.T) {
			err := app.DeleteTable(s.tableName)

			hasErr := err != nil
			if hasErr != s.expectError {
				t.Fatalf("Expected hasErr %v, got %v", s.expectError, hasErr)
			}
		})
	}
}

func TestVacuum(t *testing.T) {
	t.Parallel()

	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	calledQueries := []string{}
	app.NonconcurrentDB().(*dbx.DB).QueryLogFunc = func(ctx context.Context, t time.Duration, sql string, rows *sql.Rows, err error) {
		calledQueries = append(calledQueries, sql)
	}
	app.NonconcurrentDB().(*dbx.DB).ExecLogFunc = func(ctx context.Context, t time.Duration, sql string, result sql.Result, err error) {
		calledQueries = append(calledQueries, sql)
	}

	if err := app.Vacuum(); err != nil {
		t.Fatal(err)
	}

	if total := len(calledQueries); total != 1 {
		t.Fatalf("Expected 1 query, got %d", total)
	}

	if calledQueries[0] != "VACUUM" {
		t.Fatalf("Expected VACUUM query, got %s", calledQueries[0])
	}
}

func TestAuxVacuum(t *testing.T) {
	t.Parallel()

	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	calledQueries := []string{}
	app.AuxNonconcurrentDB().(*dbx.DB).QueryLogFunc = func(ctx context.Context, t time.Duration, sql string, rows *sql.Rows, err error) {
		calledQueries = append(calledQueries, sql)
	}
	app.AuxNonconcurrentDB().(*dbx.DB).ExecLogFunc = func(ctx context.Context, t time.Duration, sql string, result sql.Result, err error) {
		calledQueries = append(calledQueries, sql)
	}

	if err := app.AuxVacuum(); err != nil {
		t.Fatal(err)
	}

	if total := len(calledQueries); total != 1 {
		t.Fatalf("Expected 1 query, got %d", total)
	}

	if calledQueries[0] != "VACUUM" {
		t.Fatalf("Expected VACUUM query, got %s", calledQueries[0])
	}
}
