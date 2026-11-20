package core

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
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
	// SetupPending is set while this instance has connected to a database
	// that was already set up, and its role (replica or new instance) has not
	// been chosen yet. The installer keeps being served until it is cleared.
	SetupPending bool `json:"setupPending,omitempty"`
	// Driver is always "postgres" (an empty value means the same).
	Driver string `json:"driver"`
	DSN    string `json:"dsn"`
}

func (c DatabaseConfig) Validate() error {
	if c.Driver != "" && c.Driver != "postgres" {
		return errors.New("dbDriver must be postgres (PostgreSQL is the only supported database)")
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
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("open postgres database failed")
	}
	// Server-side cached statements break "SELECT table.*" queries
	// after a column is dropped or added ("cached plan must not
	// change result type"), and collection schema changes do that
	// by design. Describe each query on execution instead of caching,
	// unless the DSN explicitly sets the mode.
	if !strings.Contains(dsn, "default_query_exec_mode") {
		cfg.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	}
	db := dbx.NewFromDB(stdlib.OpenDB(*cfg), "pgx")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.DB().PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("connect to postgres database failed")
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
	app.config.DBDriver, app.config.DBDSN = "postgres", c.DSN
	return nil
}

// SaveDatabaseConfig atomically persists credentials with owner-only permissions.
func SaveDatabaseConfig(dataDir string, c DatabaseConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	c.Driver = "postgres" // an empty driver means the same
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

// InstanceSetupPending reports whether this instance still has to choose
// how it joins an already set up database (see [DatabaseConfig.SetupPending]).
func (app *BaseApp) InstanceSetupPending() bool {
	data, err := os.ReadFile(filepath.Join(app.DataDir(), "db.json"))
	if err != nil {
		return false
	}
	var c DatabaseConfig
	return json.Unmarshal(data, &c) == nil && c.SetupPending
}

// SetInstanceSetupPending sets or clears [DatabaseConfig.SetupPending].
func (app *BaseApp) SetInstanceSetupPending(pending bool) error {
	data, err := os.ReadFile(filepath.Join(app.DataDir(), "db.json"))
	if err != nil {
		return err
	}
	var c DatabaseConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return err
	}
	c.SetupPending = pending
	return SaveDatabaseConfig(app.DataDir(), c)
}
