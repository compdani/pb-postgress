package migrations

import "github.com/pocketbase/pocketbase/core"

func init() {
	core.SystemMigrations.Register(func(app core.App) error {
		return core.ExecuteSchema(app, `CREATE TABLE IF NOT EXISTS {{_instances}} ([[id]] VARCHAR(255) PRIMARY KEY, [[name]] VARCHAR(255) NOT NULL, [[url]] VARCHAR(2048) NOT NULL, [[scopeMode]] VARCHAR(8) NOT NULL, [[scopeCollections]] JSON NOT NULL, [[created_at]] VARCHAR(64) NOT NULL);
 CREATE TABLE IF NOT EXISTS {{_fileConfig}} ([[id]] VARCHAR(32) PRIMARY KEY, [[mode]] VARCHAR(16) NOT NULL);`)
	}, nil)
}
