package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/types"
)

const (
	// CollectionNamePasskeys stores the registered passkeys (WebAuthn credentials) of the auth records.
	CollectionNamePasskeys = "_passkeys"

	// CollectionNamePasskeySessions stores the pending WebAuthn ceremonies (challenges).
	CollectionNamePasskeySessions = "_passkeySessions"

	PasskeySessionKindRegistration = "registration"
	PasskeySessionKindLogin        = "login"

	// PasskeySessionDuration is how long a WebAuthn ceremony may take.
	PasskeySessionDuration = 5 * time.Minute
)

// HashPasskeyCredentialId returns the indexed representation of a raw WebAuthn credential id.
func HashPasskeyCredentialId(credentialId []byte) string {
	sum := sha256.Sum256(credentialId)
	return hex.EncodeToString(sum[:])
}

// -------------------------------------------------------------------

var (
	_ Model        = (*Passkey)(nil)
	_ PreValidator = (*Passkey)(nil)
	_ RecordProxy  = (*Passkey)(nil)
)

// Passkey defines a Record proxy for working with the _passkeys collection.
//
// The "credential" field holds the serialized WebAuthn credential
// (public key, sign counter, flags, etc.).
type Passkey struct {
	*Record
}

// NewPasskey instantiates and returns a new blank *Passkey model.
func NewPasskey(app App) *Passkey {
	m := &Passkey{}

	c, err := app.FindCachedCollectionByNameOrId(CollectionNamePasskeys)
	if err != nil {
		c = NewBaseCollection("__invalid__")
	}

	m.Record = NewRecord(c)

	return m
}

// PreValidate implements the [PreValidator] interface.
func (m *Passkey) PreValidate(ctx context.Context, app App) error {
	if m.Record == nil || m.Record.Collection().Name != CollectionNamePasskeys {
		return errors.New("missing or invalid passkey ProxyRecord")
	}

	return nil
}

// ProxyRecord returns the proxied Record model.
func (m *Passkey) ProxyRecord() *Record { return m.Record }

// SetProxyRecord loads the specified record model into the current proxy.
func (m *Passkey) SetProxyRecord(record *Record) { m.Record = record }

func (m *Passkey) CollectionRef() string      { return m.GetString("collectionRef") }
func (m *Passkey) SetCollectionRef(id string) { m.Set("collectionRef", id) }
func (m *Passkey) RecordRef() string          { return m.GetString("recordRef") }
func (m *Passkey) SetRecordRef(id string)     { m.Set("recordRef", id) }
func (m *Passkey) Name() string               { return m.GetString("name") }
func (m *Passkey) SetName(name string)        { m.Set("name", name) }
func (m *Passkey) CredentialHash() string     { return m.GetString("credentialHash") }

// SetCredentialId stores the hash of the raw credential id.
func (m *Passkey) SetCredentialId(credentialId []byte) {
	m.Set("credentialHash", HashPasskeyCredentialId(credentialId))
}

// Credential returns the raw serialized WebAuthn credential.
func (m *Passkey) Credential() types.JSONRaw {
	raw, _ := m.GetRaw("credential").(types.JSONRaw)
	return raw
}

// SetCredential stores the serialized WebAuthn credential.
func (m *Passkey) SetCredential(raw []byte) { m.Set("credential", types.JSONRaw(raw)) }

// LastUsed returns the "lastUsed" field value.
func (m *Passkey) LastUsed() types.DateTime { return m.GetDateTime("lastUsed") }

// SetLastUsed updates the "lastUsed" field value.
func (m *Passkey) SetLastUsed(date types.DateTime) { m.Set("lastUsed", date) }

// -------------------------------------------------------------------

var (
	_ Model        = (*PasskeySession)(nil)
	_ PreValidator = (*PasskeySession)(nil)
	_ RecordProxy  = (*PasskeySession)(nil)
)

// PasskeySession defines a Record proxy for working with the _passkeySessions collection.
type PasskeySession struct {
	*Record
}

// NewPasskeySession instantiates and returns a new blank *PasskeySession model.
func NewPasskeySession(app App) *PasskeySession {
	m := &PasskeySession{}

	c, err := app.FindCachedCollectionByNameOrId(CollectionNamePasskeySessions)
	if err != nil {
		c = NewBaseCollection("__invalid__")
	}

	m.Record = NewRecord(c)

	return m
}

// PreValidate implements the [PreValidator] interface.
func (m *PasskeySession) PreValidate(ctx context.Context, app App) error {
	if m.Record == nil || m.Record.Collection().Name != CollectionNamePasskeySessions {
		return errors.New("missing or invalid passkey session ProxyRecord")
	}

	return nil
}

// ProxyRecord returns the proxied Record model.
func (m *PasskeySession) ProxyRecord() *Record { return m.Record }

// SetProxyRecord loads the specified record model into the current proxy.
func (m *PasskeySession) SetProxyRecord(record *Record) { m.Record = record }

func (m *PasskeySession) CollectionRef() string      { return m.GetString("collectionRef") }
func (m *PasskeySession) SetCollectionRef(id string) { m.Set("collectionRef", id) }

// RecordRef returns the auth record that started a registration ceremony (empty for login).
func (m *PasskeySession) RecordRef() string      { return m.GetString("recordRef") }
func (m *PasskeySession) SetRecordRef(id string) { m.Set("recordRef", id) }
func (m *PasskeySession) Kind() string           { return m.GetString("kind") }
func (m *PasskeySession) SetKind(kind string)    { m.Set("kind", kind) }

// Data returns the serialized WebAuthn session data (challenge, etc.).
func (m *PasskeySession) Data() types.JSONRaw {
	raw, _ := m.GetRaw("data").(types.JSONRaw)
	return raw
}

// SetData stores the serialized WebAuthn session data.
func (m *PasskeySession) SetData(raw []byte)      { m.Set("data", types.JSONRaw(raw)) }
func (m *PasskeySession) Created() types.DateTime { return m.GetDateTime("created") }

// HasExpired checks whether the ceremony took longer than [PasskeySessionDuration].
func (m *PasskeySession) HasExpired() bool {
	return time.Since(m.Created().Time()) > PasskeySessionDuration
}

// -------------------------------------------------------------------

// FindPasskeyByCredentialId returns the passkey with the provided raw credential id.
func (app *BaseApp) FindPasskeyByCredentialId(credentialId []byte) (*Passkey, error) {
	result := &Passkey{}

	err := app.RecordQuery(CollectionNamePasskeys).
		AndWhere(dbx.HashExp{"credentialHash": HashPasskeyCredentialId(credentialId)}).
		Limit(1).
		One(result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// FindAllPasskeysByRecord returns all passkeys registered for the provided auth record.
func (app *BaseApp) FindAllPasskeysByRecord(authRecord *Record) ([]*Passkey, error) {
	result := []*Passkey{}

	err := app.RecordQuery(CollectionNamePasskeys).
		AndWhere(dbx.HashExp{
			"collectionRef": authRecord.Collection().Id,
			"recordRef":     authRecord.Id,
		}).
		OrderBy("created DESC").
		All(&result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// FindPasskeySessionById returns a single pending passkey ceremony by its id.
func (app *BaseApp) FindPasskeySessionById(id string) (*PasskeySession, error) {
	result := &PasskeySession{}

	err := app.RecordQuery(CollectionNamePasskeySessions).
		AndWhere(dbx.HashExp{"id": id}).
		Limit(1).
		One(result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// ConsumePasskeySession atomically deletes the provided session and
// reports whether this call was the one that consumed it (single use challenges).
func (app *BaseApp) ConsumePasskeySession(m *PasskeySession) (bool, error) {
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

// DeleteExpiredPasskeySessions deletes the abandoned passkey ceremonies.
func (app *BaseApp) DeleteExpiredPasskeySessions() error {
	minValidDate, err := types.ParseDateTime(time.Now().Add(-PasskeySessionDuration))
	if err != nil {
		return err
	}

	items := []*Record{}

	err = app.RecordQuery(CollectionNamePasskeySessions).
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

	return nil
}

func (app *BaseApp) registerPasskeyHooks() {
	// validate the refs and delete the passkeys together with their auth record or collection
	recordRefHooks[*Passkey](app, CollectionNamePasskeys, CollectionTypeAuth)

	// pending ceremonies are short lived and are validated on use
	app.Cron().Add("__pbPasskeySessionsCleanup__", "30 * * * *", func() {
		if err := app.DeleteExpiredPasskeySessions(); err != nil {
			app.Logger().Warn("Failed to delete expired passkey sessions", "error", err)
		}
	})
}
