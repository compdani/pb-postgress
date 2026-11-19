package migrations

import (
	"encoding/json"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// Existing plaintext client credentials become one-way hashes. Back up credentials
// before upgrading; clients continue using their original secret at the token endpoint.
func init() {
	core.SystemMigrations.Register(func(app core.App) error {
		cols, err := app.FindAllCollections()
		if err != nil {
			return err
		}
		for _, col := range cols {
			if !col.IsAuth() {
				continue
			}
			if err := col.OAuth2Server.PrepareSecrets(); err != nil {
				return err
			}
			var options map[string]any
			if err := json.Unmarshal(col.RawOptions, &options); err != nil {
				return err
			}
			options["oauth2Server"] = col.OAuth2Server
			raw, err := json.Marshal(options)
			if err != nil {
				return err
			}
			if _, err := app.DB().NewQuery("UPDATE {{_collections}} SET [[options]]={:options} WHERE [[id]]={:id}").Bind(dbx.Params{"options": string(raw), "id": col.Id}).Execute(); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}
