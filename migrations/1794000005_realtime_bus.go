package migrations

import "github.com/pocketbase/pocketbase/core"

func init() {
	core.SystemMigrations.Register(core.InitRealtimeBus, func(app core.App) error {
		if _, err := app.DB().DropTable("_realtimeEvents").Execute(); err != nil {
			return err
		}
		_, err := app.DB().DropTable("_realtimeBoots").Execute()
		return err
	})
}
