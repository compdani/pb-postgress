// Package testdb allocates isolated server databases for integration tests.
package testdb

import (
	"database/sql"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pocketbase/pocketbase/tools/security"
	"net/url"
	"os"
	"strings"
	"testing"
)

func New(t testing.TB) (driver, dsn string) {
	t.Helper()
	driver = os.Getenv("PB_TEST_DB_DRIVER")
	if driver == "" {
		driver = "postgres"
	}
	dsn = os.Getenv("PB_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("database test requires PB_TEST_DB_DSN")
	}
	var db *sql.DB
	var err error
	name := "pb_test_" + security.RandomStringWithAlphabet(20, "abcdefghijklmnopqrstuvwxyz")
	if driver == "mysql" {
		c, e := mysql.ParseDSN(dsn)
		if e != nil {
			t.Fatal(e)
		}
		c.ParseTime = true
		db, err = sql.Open("mysql", c.FormatDSN())
		c.DBName = name
		dsn = c.FormatDSN()
	} else {
		c, e := pgx.ParseConfig(dsn)
		if e != nil {
			t.Fatal(e)
		}
		db = stdlib.OpenDB(*c)
		if u, e := url.Parse(dsn); e == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
			u.Path = "/" + name
			dsn = u.String()
		} else {
			dsn += " dbname='" + name + "'"
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE DATABASE " + name); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer db.Close()
		suffix := ""
		if driver != "mysql" {
			suffix = " WITH (FORCE)"
		}
		if _, err := db.Exec("DROP DATABASE " + name + suffix); err != nil && !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("drop test database: %v", err)
		}
	})
	return driver, dsn
}
