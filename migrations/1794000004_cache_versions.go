package migrations

import "github.com/pocketbase/pocketbase/core"

func init() {
	core.SystemMigrations.Register(core.InitCacheVersions, func(app core.App) error {
		_, err := app.DB().DropTable("_cacheVersions").Execute()
		return err
	})
}
