package core_test

import (
	"context"
	"errors"
	"github.com/pocketbase/pocketbase/core"
	"testing"
)

func TestServerDatabaseArchiveUnsupported(t *testing.T) {
	for _, driver := range []string{"postgres", "mysql"} {
		t.Run(driver, func(t *testing.T) {
			app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir(), DBDriver: driver})
			for _, call := range []func(context.Context, string) error{app.CreateBackup, app.RestoreBackup} {
				if err := call(context.Background(), "backup.zip"); !errors.Is(err, core.ErrServerDatabaseBackupUnsupported) {
					t.Fatalf("expected unsupported archive error, got %v", err)
				}
			}
		})
	}
}
