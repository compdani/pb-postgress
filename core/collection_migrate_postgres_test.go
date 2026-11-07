package core

import "testing"

func TestMigrateCollectionToPostgresUnsupported(t *testing.T) {
	app := NewBaseApp(BaseAppConfig{DataDir: t.TempDir()})
	if _, err := app.MigrateCollectionToPostgres(NewBaseCollection("items"), CollectionPostgresMigrationConfig{}); err == nil {
		t.Fatal("cross-engine migration must use external tools")
	}
}
