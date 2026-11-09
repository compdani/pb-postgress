package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// creates the system collections for the usernameless passkey authentication
func init() {
	core.SystemMigrations.Register(func(txApp core.App) error {
		if err := createPasskeysCollection(txApp); err != nil {
			return err
		}

		return createPasskeySessionsCollection(txApp)
	}, func(txApp core.App) error {
		for _, name := range []string{core.CollectionNamePasskeySessions, core.CollectionNamePasskeys} {
			col, err := txApp.FindCollectionByNameOrId(name)
			if err != nil {
				continue
			}

			col.System = false
			if err := txApp.Delete(col); err != nil {
				return err
			}
		}

		return nil
	})
}

func createPasskeysCollection(txApp core.App) error {
	if _, err := txApp.FindCollectionByNameOrId(core.CollectionNamePasskeys); err == nil {
		return nil // already created
	}

	col := core.NewBaseCollection(core.CollectionNamePasskeys)
	col.System = true

	// the owner can list and remove their passkeys through the regular records api
	ownerRule := "@request.auth.id != '' && recordRef = @request.auth.id && collectionRef = @request.auth.collectionId"
	col.ListRule = types.Pointer(ownerRule)
	col.ViewRule = types.Pointer(ownerRule)
	col.DeleteRule = types.Pointer(ownerRule)

	col.Fields.Add(&core.TextField{Name: "collectionRef", System: true, Required: true})
	col.Fields.Add(&core.TextField{Name: "recordRef", System: true, Required: true})
	col.Fields.Add(&core.TextField{Name: "credentialHash", System: true, Required: true, Hidden: true})
	col.Fields.Add(&core.JSONField{Name: "credential", System: true, Required: true, Hidden: true, MaxSize: 64 << 10})
	col.Fields.Add(&core.TextField{Name: "name", System: true, Max: 255})
	col.Fields.Add(&core.DateField{Name: "lastUsed", System: true})
	col.Fields.Add(&core.AutodateField{Name: "created", System: true, OnCreate: true})
	col.Fields.Add(&core.AutodateField{Name: "updated", System: true, OnCreate: true, OnUpdate: true})

	col.AddIndex("idx_passkeys_credentialHash", true, "credentialHash", "")
	col.AddIndex("idx_passkeys_collectionRef_recordRef", false, "collectionRef,recordRef", "")

	return txApp.Save(col)
}

func createPasskeySessionsCollection(txApp core.App) error {
	if _, err := txApp.FindCollectionByNameOrId(core.CollectionNamePasskeySessions); err == nil {
		return nil // already created
	}

	col := core.NewBaseCollection(core.CollectionNamePasskeySessions)
	col.System = true

	col.Fields.Add(&core.TextField{Name: "collectionRef", System: true, Required: true})
	col.Fields.Add(&core.TextField{Name: "recordRef", System: true})
	col.Fields.Add(&core.TextField{Name: "kind", System: true, Required: true})
	col.Fields.Add(&core.JSONField{Name: "data", System: true, Required: true, Hidden: true, MaxSize: 64 << 10})
	col.Fields.Add(&core.AutodateField{Name: "created", System: true, OnCreate: true})
	col.Fields.Add(&core.AutodateField{Name: "updated", System: true, OnCreate: true, OnUpdate: true})

	return txApp.Save(col)
}
