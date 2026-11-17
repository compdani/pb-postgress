package migrations

import "github.com/pocketbase/pocketbase/core"

func init() {
	core.SystemMigrations.Register(func(app core.App) error {
		if err := core.ExecuteSchema(app, "CREATE TABLE IF NOT EXISTS {{_oauth2ServerRoot}} ([[slot]] INTEGER PRIMARY KEY, [[owner]] VARCHAR(255) NOT NULL DEFAULT '')"); err != nil {
			return err
		}
		if _, err := app.DB().NewQuery("INSERT INTO {{_oauth2ServerRoot}} ([[slot]],[[owner]]) SELECT 1,'' WHERE NOT EXISTS (SELECT 1 FROM {{_oauth2ServerRoot}} WHERE [[slot]]=1)").Execute(); err != nil {
			return err
		}
		if _, err := app.FindCollectionByNameOrId("_oauth2ServerClients"); err == nil {
			return nil
		}
		col := core.NewBaseCollection("_oauth2ServerClients")
		col.System = true
		col.Fields.Add(&core.TextField{Name: "collectionRef", System: true, Required: true})
		col.Fields.Add(&core.TextField{Name: "clientId", System: true, Required: true})
		col.Fields.Add(&core.TextField{Name: "name", System: true, Max: 255})
		col.Fields.Add(&core.JSONField{Name: "redirectURIs", System: true, Required: true, MaxSize: 16384})
		col.Fields.Add(&core.BoolField{Name: "revoked", System: true})
		col.Fields.Add(&core.AutodateField{Name: "created", System: true, OnCreate: true})
		col.AddIndex("idx_oauth2ServerClients_clientId", true, "clientId", "")
		return app.Save(col)
	}, nil)
}
