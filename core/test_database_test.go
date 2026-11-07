package core

import (
	"github.com/pocketbase/pocketbase/tests/testdb"
	"testing"
)

func newDatabaseTestApp(t testing.TB, cfg BaseAppConfig) *BaseApp {
	t.Helper()
	cfg.DBDriver, cfg.DBDSN = testdb.New(t)
	return NewBaseApp(cfg)
}
