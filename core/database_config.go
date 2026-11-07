package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
)

var ErrDatabaseUnconfigured = errors.New("database configuration required")

// DatabaseConfig is the local bootstrap configuration. The DSN is a secret.
type DatabaseConfig struct {
	InstanceID string `json:"instanceId,omitempty"`
	Driver     string `json:"driver"`
	DSN        string `json:"dsn"`
}

func (c DatabaseConfig) Validate() error {
	if c.Driver != "postgres" && c.Driver != "mysql" {
		return errors.New("dbDriver must be postgres or mysql")
	}
	if strings.TrimSpace(c.DSN) == "" {
		return errors.New("dbDSN is required")
	}
	return nil
}

// ConnectDatabase opens and tests exactly one supported database engine.
func ConnectDatabase(driver, dsn string) (*dbx.DB, error) {
	if err := (DatabaseConfig{Driver: driver, DSN: dsn}).Validate(); err != nil {
		return nil, err
	}
	sqlDriver := driver
	if driver == "postgres" {
		sqlDriver = "pgx"
	}
	var db *dbx.DB
	var err error
	if driver == "postgres" {
		db, err = dbx.Open(sqlDriver, dsn)
	} else {
		var cfg *mysql.Config
		cfg, err = mysql.ParseDSN(dsn)
		if err == nil {
			cfg.ParseTime = true
			var raw *sql.DB
			raw, err = sql.Open(sqlDriver, cfg.FormatDSN())
			if err == nil {
				db = dbx.NewFromDB(raw, sqlDriver)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("open %s database failed", driver)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.DB().PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to %s database failed", driver)
	}
	return db, nil
}

func (app *BaseApp) loadDatabaseConfig() error {
	c := DatabaseConfig{Driver: app.config.DBDriver, DSN: app.config.DBDSN}
	if c.Driver == "" {
		c.Driver = os.Getenv("PB_DB_DRIVER")
	}
	if c.DSN == "" {
		c.DSN = os.Getenv("PB_DB_DSN")
	}
	if c.Driver == "" && c.DSN == "" {
		data, err := os.ReadFile(filepath.Join(app.DataDir(), "db.json"))
		if err != nil {
			if os.IsNotExist(err) {
				return ErrDatabaseUnconfigured
			}
			return err
		}
		if err = json.Unmarshal(data, &c); err != nil {
			return errors.New("invalid db.json")
		}
	}
	if err := c.Validate(); err != nil {
		return err
	}
	app.config.DBDriver, app.config.DBDSN = c.Driver, c.DSN
	return nil
}

// SaveDatabaseConfig atomically persists credentials with owner-only permissions.
func SaveDatabaseConfig(dataDir string, c DatabaseConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	if c.InstanceID == "" {
		if old, err := os.ReadFile(filepath.Join(dataDir, "db.json")); err == nil {
			var previous DatabaseConfig
			if json.Unmarshal(old, &previous) == nil {
				c.InstanceID = previous.InstanceID
			}
		}
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dataDir, ".db-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dataDir, "db.json"))
}
