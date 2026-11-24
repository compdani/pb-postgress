package migrations

import "github.com/pocketbase/pocketbase/core"

func init() {
	core.SystemMigrations.Register(func(app core.App) error {
		// Historical rows cannot be attributed reliably to an instance.
		return core.ExecuteSchema(app, `
   ALTER TABLE {{_logs}} ADD COLUMN [[instanceId]] TEXT NOT NULL DEFAULT '';
   CREATE INDEX idx_logs_instance_created ON {{_logs}} ([[instanceId]], [[created]]);
  `)
	}, func(app core.App) error {
		return core.ExecuteSchema(app, `
   DROP INDEX IF EXISTS idx_logs_instance_created;
   ALTER TABLE {{_logs}} DROP COLUMN [[instanceId]];
  `)
	})
}
