package migrations

import (
	"github.com/pocketbase/pocketbase/core"
)

func init() {
	core.SystemMigrations.Add(&core.Migration{
		Up: func(txApp core.App) error {
			execErr := core.ExecuteSchema(txApp, `
				CREATE TABLE IF NOT EXISTS {{_logs}} (
					[[id]]      VARCHAR(255) PRIMARY KEY NOT NULL,
					[[level]]   INTEGER DEFAULT 0 NOT NULL,
					[[message]] VARCHAR(255) DEFAULT '' NOT NULL,
					[[data]]    JSON DEFAULT ('{}') NOT NULL,
					[[created]] TEXT DEFAULT '' NOT NULL
				);

				CREATE INDEX IF NOT EXISTS idx_logs_level on {{_logs}} ([[level]]);
				CREATE INDEX IF NOT EXISTS idx_logs_message on {{_logs}} ([[message]]);
				CREATE INDEX IF NOT EXISTS idx_logs_created_hour on {{_logs}} ([[created]]);
			`)

			return execErr
		},
		Down: func(txApp core.App) error {
			_, err := txApp.AuxDB().DropTable("_logs").Execute()
			return err
		},
		ReapplyCondition: func(txApp core.App, runner *core.MigrationsRunner, fileName string) (bool, error) {
			// reapply only if the _logs table doesn't exist
			exists := txApp.AuxHasTable("_logs")
			return !exists, nil
		},
	})
}
