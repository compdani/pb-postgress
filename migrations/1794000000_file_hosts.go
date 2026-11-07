package migrations

import "github.com/pocketbase/pocketbase/core"

func init() {
	core.SystemMigrations.Register(core.InitFileTables, func(app core.App) error {
		if _, err := app.DB().DropTable("_fileChanges").Execute(); err != nil {
			return err
		}
		_, err := app.DB().DropTable("_fileHosts").Execute()
		return err
	})
}
