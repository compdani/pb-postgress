package core

import (
	"errors"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/types"
)

// FindOAuth2CodeByCode returns the pending authorization code model matching the raw code.
func (app *BaseApp) FindOAuth2CodeByCode(code string) (*OAuth2Code, error) {
	result := &OAuth2Code{}

	err := app.RecordQuery(CollectionNameOAuth2Codes).
		AndWhere(dbx.HashExp{"codeHash": HashOAuth2Code(code)}).
		Limit(1).
		One(result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// ConsumeOAuth2Code atomically deletes the provided code model.
//
// It returns false (without an error) if the code was already consumed,
// which makes concurrent redemptions of the same code fail.
func (app *BaseApp) ConsumeOAuth2Code(m *OAuth2Code) (bool, error) {
	result, err := app.NonconcurrentDB().Delete(m.TableName(), dbx.HashExp{"id": m.Id}).Execute()
	if err != nil {
		return false, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	return affected == 1, nil
}

// DeleteAllOAuth2CodesByRecord deletes all pending codes issued for the provided auth record.
func (app *BaseApp) DeleteAllOAuth2CodesByRecord(authRecord *Record) error {
	items := []*Record{}

	err := app.RecordQuery(CollectionNameOAuth2Codes).
		AndWhere(dbx.HashExp{
			"collectionRef": authRecord.Collection().Id,
			"recordRef":     authRecord.Id,
		}).
		All(&items)
	if err != nil {
		return err
	}

	var errs []error
	for _, item := range items {
		if err := app.Delete(item); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// DeleteExpiredOAuth2Codes deletes the expired authorization codes for all auth collections.
func (app *BaseApp) DeleteExpiredOAuth2Codes() error {
	authCollections, err := app.FindAllCollections(CollectionTypeAuth)
	if err != nil {
		return err
	}

	for _, collection := range authCollections {
		minValidDate, err := types.ParseDateTime(time.Now().Add(-1 * collection.OAuth2Server.CodeDurationTime()))
		if err != nil {
			return err
		}

		items := []*Record{}

		err = app.RecordQuery(CollectionNameOAuth2Codes).
			AndWhere(dbx.HashExp{"collectionRef": collection.Id}).
			AndWhere(dbx.NewExp("[[created]] < {:date}", dbx.Params{"date": minValidDate})).
			All(&items)
		if err != nil {
			return err
		}

		for _, item := range items {
			if err := app.Delete(item); err != nil {
				return err
			}
		}
	}

	return nil
}
