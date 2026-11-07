package tests

import (
	"embed"
	"encoding/json"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"net/url"
	"os"
	"strings"
	"testing"
)

//go:embed data/fixtures.json
var fixtureFS embed.FS

// Each app owns an entire database, including migrations and auxiliary tables.
// The configured user must have CREATE DATABASE permission on a test server.
func newTestDatabase(tb testing.TB) (string, string, func(), error) {
	driver := os.Getenv("PB_TEST_DB_DRIVER")
	if driver == "" {
		driver = "postgres"
	}
	dsn := os.Getenv("PB_TEST_DB_DSN")
	admin, err := core.ConnectDatabase(driver, dsn)
	if err != nil {
		return "", "", nil, err
	}
	name := "pb_test_" + security.RandomStringWithAlphabet(20, "abcdefghijklmnopqrstuvwxyz")
	if _, err = admin.NewQuery("CREATE DATABASE " + name).Execute(); err != nil {
		admin.Close()
		return "", "", nil, err
	}
	cleanup := func() {
		defer admin.Close()
		if driver == "postgres" {
			_, err = admin.NewQuery("DROP DATABASE " + name + " WITH (FORCE)").Execute()
		} else {
			_, err = admin.NewQuery("DROP DATABASE " + name).Execute()
		}
		if err != nil {
			tb.Errorf("drop test database %s: %v", name, err)
		}
	}
	if driver == "mysql" {
		c, e := mysql.ParseDSN(dsn)
		if e != nil {
			cleanup()
			return "", "", nil, e
		}
		c.DBName = name
		dsn = c.FormatDSN()
	} else {
		c, e := pgx.ParseConfig(dsn)
		if e != nil {
			cleanup()
			return "", "", nil, e
		}
		c.Database = name
		if u, e := url.Parse(dsn); e == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
			u.Path = "/" + name
			dsn = u.String()
		} else {
			dsn += " dbname='" + name + "'"
		}
	}
	return dsn, driver, cleanup, nil
}

func escapeConn(s string) string { return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) }

func seedFixtures(app *core.BaseApp) error {
	raw, _ := fixtureFS.ReadFile("data/fixtures.json")
	var data map[string][]map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	// Install fixture metadata directly to preserve historical field IDs/rules.
	for _, row := range data["_collections"] {
		row["system"] = row["system"] == float64(1)
		if _, err := app.DB().Delete("_collections", dbx.HashExp{"id": row["id"]}).Execute(); err != nil {
			return err
		}
		if _, err := app.DB().Insert("_collections", dbx.Params(row)).Execute(); err != nil {
			return fmt.Errorf("fixture collection: %w", err)
		}
	}
	if err := app.ReloadCachedCollections(); err != nil {
		return err
	}
	collections, err := app.FindAllCollections()
	if err != nil {
		return err
	}
	for _, c := range collections {
		if c.IsView() {
			continue
		}
		// Recreate bootstrap tables with exactly the fixture fields.
		if app.HasTable(c.Name) {
			if _, err := app.DB().DropTable(c.Name).Execute(); err != nil {
				return err
			}
		}
		if err := app.SyncRecordTableSchema(c, nil); err != nil {
			return fmt.Errorf("fixture schema %s: %w", c.Name, err)
		}
	}
	for table, rows := range data {
		var bools []string
		for _, c := range collections {
			if c.Name == table {
				for _, f := range c.Fields {
					if f.Type() == core.FieldTypeBool {
						bools = append(bools, f.GetName())
					}
				}
			}
		}
		if table == "_collections" {
			continue
		}
		if _, err := app.DB().Delete(table, nil).Execute(); err != nil {
			return err
		}
		for _, row := range rows {
			for _, name := range bools {
				row[name] = row[name] == float64(1)
			}
			if _, err := app.DB().Insert(table, dbx.Params(row)).Execute(); err != nil {
				return fmt.Errorf("fixture %s: %w", table, err)
			}
		}
	}
	for _, name := range []string{"view1", "view2", "numeric_id_view"} {
		c, err := app.FindCachedCollectionByNameOrId(name)
		if err != nil {
			continue
		}
		if _, err := app.DB().NewQuery("CREATE VIEW " + app.DB().QuoteSimpleTableName(c.Name) + " AS " + c.ViewQuery).Execute(); err != nil {
			return fmt.Errorf("fixture view %s: %w", name, err)
		}
	}
	return app.ReloadSettings()
}

// NewDatabaseConfig allocates an empty isolated database and drops it at test cleanup.
func NewDatabaseConfig(tb testing.TB) core.BaseAppConfig {
	tb.Helper()
	if os.Getenv("PB_TEST_DB_DSN") == "" {
		tb.Skip("database test requires PB_TEST_DB_DSN")
	}
	dsn, driver, cleanup, err := newTestDatabase(tb)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(cleanup)
	return core.BaseAppConfig{DataDir: tb.TempDir(), DBDSN: dsn, DBDriver: driver}
}
