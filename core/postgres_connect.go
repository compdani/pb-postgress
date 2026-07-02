package core

import (
	"fmt"

	"github.com/pocketbase/dbx"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// DefaultPostgresConnect opens a PostgreSQL connection using dbx.
func DefaultPostgresConnect(dsn string) (*dbx.DB, error) {
	db, err := dbx.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}

	if err := db.DB().Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres ping failed: %w", err)
	}

	return db, nil
}
