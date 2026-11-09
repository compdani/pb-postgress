package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/types"
)

// CollectionNameOAuth2Codes is the system collection that stores the
// pending authorization codes issued by the collection OAuth2 servers.
const CollectionNameOAuth2Codes = "_oauth2Codes"

var (
	_ Model        = (*OAuth2Code)(nil)
	_ PreValidator = (*OAuth2Code)(nil)
	_ RecordProxy  = (*OAuth2Code)(nil)
)

// OAuth2Code defines a Record proxy for working with the _oauth2Codes collection.
//
// Only the SHA-256 hash of the authorization code is stored.
type OAuth2Code struct {
	*Record
}

// NewOAuth2Code instantiates and returns a new blank *OAuth2Code model.
func NewOAuth2Code(app App) *OAuth2Code {
	m := &OAuth2Code{}

	c, err := app.FindCachedCollectionByNameOrId(CollectionNameOAuth2Codes)
	if err != nil {
		// this is just to make tests easier since it is a system collection that is expected to be always accessible
		// (note: the loaded record is further checked on OAuth2Code.PreValidate())
		c = NewBaseCollection("__invalid__")
	}

	m.Record = NewRecord(c)

	return m
}

// HashOAuth2Code returns the stored representation of a raw authorization code.
func HashOAuth2Code(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// PreValidate implements the [PreValidator] interface and checks
// whether the proxy is properly loaded.
func (m *OAuth2Code) PreValidate(ctx context.Context, app App) error {
	if m.Record == nil || m.Record.Collection().Name != CollectionNameOAuth2Codes {
		return errors.New("missing or invalid oauth2 code ProxyRecord")
	}

	return nil
}

// ProxyRecord returns the proxied Record model.
func (m *OAuth2Code) ProxyRecord() *Record {
	return m.Record
}

// SetProxyRecord loads the specified record model into the current proxy.
func (m *OAuth2Code) SetProxyRecord(record *Record) {
	m.Record = record
}

func (m *OAuth2Code) CollectionRef() string       { return m.GetString("collectionRef") }
func (m *OAuth2Code) SetCollectionRef(id string)  { m.Set("collectionRef", id) }
func (m *OAuth2Code) RecordRef() string           { return m.GetString("recordRef") }
func (m *OAuth2Code) SetRecordRef(id string)      { m.Set("recordRef", id) }
func (m *OAuth2Code) ClientId() string            { return m.GetString("clientId") }
func (m *OAuth2Code) SetClientId(id string)       { m.Set("clientId", id) }
func (m *OAuth2Code) CodeHash() string            { return m.GetString("codeHash") }
func (m *OAuth2Code) RedirectURI() string         { return m.GetString("redirectURI") }
func (m *OAuth2Code) SetRedirectURI(uri string)   { m.Set("redirectURI", uri) }
func (m *OAuth2Code) Scope() string               { return m.GetString("scope") }
func (m *OAuth2Code) SetScope(scope string)       { m.Set("scope", scope) }
func (m *OAuth2Code) Nonce() string               { return m.GetString("nonce") }
func (m *OAuth2Code) SetNonce(nonce string)       { m.Set("nonce", nonce) }
func (m *OAuth2Code) CodeChallenge() string       { return m.GetString("codeChallenge") }
func (m *OAuth2Code) SetCodeChallenge(v string)   { m.Set("codeChallenge", v) }
func (m *OAuth2Code) CodeChallengeMethod() string { return m.GetString("codeChallengeMethod") }
func (m *OAuth2Code) SetCodeChallengeMethod(v string) {
	m.Set("codeChallengeMethod", v)
}
func (m *OAuth2Code) Created() types.DateTime { return m.GetDateTime("created") }

// SetCode stores the hash of the provided raw authorization code.
func (m *OAuth2Code) SetCode(code string) {
	m.Set("codeHash", HashOAuth2Code(code))
}

// HasExpired checks whether more than maxElapsed has passed since the code creation.
func (m *OAuth2Code) HasExpired(maxElapsed time.Duration) bool {
	return time.Since(m.Created().Time()) > maxElapsed
}

func (app *BaseApp) registerOAuth2CodeHooks() {
	recordRefHooks[*OAuth2Code](app, CollectionNameOAuth2Codes, CollectionTypeAuth)

	// run every hour to cleanup expired codes
	app.Cron().Add("__pbOAuth2CodesCleanup__", "15 * * * *", func() {
		if err := app.DeleteExpiredOAuth2Codes(); err != nil {
			app.Logger().Warn("Failed to delete expired OAuth2 codes", "error", err)
		}
	})

	// invalidate pending codes on tokenKey change (password change, logout everywhere, etc.)
	app.OnRecordUpdateExecute().Bind(&hook.Handler[*RecordEvent]{
		Func: func(e *RecordEvent) error {
			err := e.Next()
			if err != nil || !e.Record.Collection().IsAuth() {
				return err
			}

			if !e.Record.Original().IsNew() && e.Record.Original().TokenKey() != e.Record.TokenKey() {
				if err := e.App.DeleteAllOAuth2CodesByRecord(e.Record); err != nil {
					return fmt.Errorf(
						"[%s] failed to delete all pending OAuth2 codes for record %q: %w",
						e.Record.Collection().Name,
						e.Record.Id,
						err,
					)
				}
			}

			return nil
		},
		Priority: 99,
	})
}
