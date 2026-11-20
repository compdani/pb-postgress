// Package testdb allocates isolated server databases for integration tests.
package testdb

import (
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pocketbase/pocketbase/tools/security"
	"net/url"
	"os"
	"strings"
	"testing"
)

// CreateDatabaseOptions makes a test database UTF8 like a production one,
// whatever the server default is (an initdb with the C locale defaults to
// SQL_ASCII, which accepts invalid UTF-8 and hides encoding errors).
const CreateDatabaseOptions = " ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0"

// New creates an empty PostgreSQL database for the test (dropped on cleanup)
// and returns its driver name ("postgres") and DSN.
func New(t testing.TB) (driver, dsn string) {
	t.Helper()
	dsn = os.Getenv("PB_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("database test requires PB_TEST_DB_DSN")
	}
	name := "pb_test_" + security.RandomStringWithAlphabet(20, "abcdefghijklmnopqrstuvwxyz")
	c, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*c)
	if u, e := url.Parse(dsn); e == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		u.Path = "/" + name
		dsn = u.String()
	} else {
		dsn += " dbname='" + name + "'"
	}
	if _, err := db.Exec("CREATE DATABASE " + name + CreateDatabaseOptions); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer db.Close()
		if _, err := db.Exec("DROP DATABASE " + name + " WITH (FORCE)"); err != nil && !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("drop test database: %v", err)
		}
	})
	return "postgres", dsn
}
