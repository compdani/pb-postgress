//go:build !no_default_driver

package core

import (
 "os"
 "github.com/pocketbase/dbx"
)

// DefaultDBConnect opens the configured server database. Local database files are unsupported.
func DefaultDBConnect(dsn string) (*dbx.DB, error) {
 return ConnectDatabase(os.Getenv("PB_DB_DRIVER"), dsn)
}
