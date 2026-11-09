package migrations

import (
	"github.com/pocketbase/pocketbase/core"
)

// creates the system collection that stores the pending authorization
// codes of the auth collections configured as OAuth2 servers
func init() {
	core.SystemMigrations.Register(func(txApp core.App) error {
		if _, err := txApp.FindCollectionByNameOrId(core.CollectionNameOAuth2Codes); err == nil {
			return nil // already created
		}

		col := core.NewBaseCollection(core.CollectionNameOAuth2Codes)
		col.System = true

		// note: explicit max lengths because on MySQL a text field
		// without max is stored as VARCHAR(255)
		for _, name := range []string{"collectionRef", "recordRef", "clientId", "codeHash"} {
			col.Fields.Add(&core.TextField{
				Name:     name,
				System:   true,
				Required: true,
				Hidden:   name == "codeHash",
			})
		}
		col.Fields.Add(&core.TextField{
			Name:     "redirectURI",
			System:   true,
			Required: true,
			Max:      2048,
		})
		col.Fields.Add(&core.TextField{Name: "scope", System: true, Max: 1000})
		col.Fields.Add(&core.TextField{Name: "nonce", System: true, Max: 1000})
		col.Fields.Add(&core.TextField{Name: "codeChallenge", System: true})
		col.Fields.Add(&core.TextField{Name: "codeChallengeMethod", System: true})

		col.Fields.Add(&core.AutodateField{
			Name:     "created",
			System:   true,
			OnCreate: true,
		})
		col.Fields.Add(&core.AutodateField{
			Name:     "updated",
			System:   true,
			OnCreate: true,
			OnUpdate: true,
		})

		col.AddIndex("idx_oauth2Codes_codeHash", true, "codeHash", "")
		col.AddIndex("idx_oauth2Codes_collectionRef_recordRef", false, "collectionRef,recordRef", "")

		return txApp.Save(col)
	}, func(txApp core.App) error {
		col, err := txApp.FindCollectionByNameOrId(core.CollectionNameOAuth2Codes)
		if err != nil {
			return nil
		}

		col.System = false

		return txApp.Delete(col)
	})
}
