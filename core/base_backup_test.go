package core_test

import (
	"context"
	"github.com/pocketbase/pocketbase/core"
	"testing"
)

func TestServerDatabaseBackupRequiresNativeTools(t *testing.T) {
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	if app.CreateBackup(context.Background(), "test.zip") == nil {
		t.Fatal("expected native backup requirement")
	}
	if app.RestoreBackup(context.Background(), "test.zip") == nil {
		t.Fatal("expected native restore requirement")
	}
}
