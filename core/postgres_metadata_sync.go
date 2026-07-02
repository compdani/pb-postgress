package core

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/pocketbase/pocketbase/tools/security"
)

// SyncCollectionsFromPostgres merges mirrored collection metadata from PostgreSQL into local SQLite.
func (app *BaseApp) SyncCollectionsFromPostgres() error {
	if !app.HasPostgres() {
		return nil
	}

	rows, err := app.FindAllPostgresCollectionMetadata()
	if err != nil {
		return err
	}

	for _, row := range rows {
		imported, err := row.toCollection(app)
		if err != nil {
			return err
		}

		existing, findErr := app.FindCollectionByNameOrId(imported.Id)
		if findErr != nil && !errors.Is(findErr, sql.ErrNoRows) {
			return findErr
		}

		if existing != nil {
			imported.MarkAsNotNew()
		}

		if err := app.SaveNoValidate(imported); err != nil {
			return fmt.Errorf("failed to sync postgres collection %q: %w", imported.Name, err)
		}
	}

	return nil
}

func (app *BaseApp) initPostgresInstanceId() {
	if app.store.Get("postgresInstanceId") == nil {
		app.store.Set("postgresInstanceId", "@"+security.PseudorandomString(10))
	}
}
