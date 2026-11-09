package apis

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/pocketbase/pocketbase/tools/types"
)

// Usernameless passkey (WebAuthn discoverable credential) authentication.
//
// Registration (the record must be authenticated):
//
//	POST /api/collections/{collection}/request-passkey-registration -> {sessionId, options}
//	POST /api/collections/{collection}/confirm-passkey-registration <- {sessionId, name, credential}
//
// Login (no email/username, the authenticator returns the user handle):
//
//	POST /api/collections/{collection}/request-passkey -> {sessionId, options}
//	POST /api/collections/{collection}/auth-with-passkey <- {sessionId, credential}
//
// The "options" value is passed to navigator.credentials.create()/get() and
// "credential" is the resulting PublicKeyCredential serialized with toJSON().
//
// The owner can list and delete their passkeys through the records api of
// the "_passkeys" system collection.

func bindRecordAuthPasskeyApi(sub *router.RouterGroup[*core.RequestEvent]) {
	sub.POST("/request-passkey-registration", recordRequestPasskeyRegistration).Bind(
		collectionPathRateLimit("", "requestPasskeyRegistration"),
		RequireSameCollectionContextAuth(""),
	)
	sub.POST("/confirm-passkey-registration", recordConfirmPasskeyRegistration).Bind(
		collectionPathRateLimit("", "confirmPasskeyRegistration"),
	)
	sub.POST("/request-passkey", recordRequestPasskey).Bind(
		collectionPathRateLimit("", "requestPasskey"),
	)
	sub.POST("/auth-with-passkey", recordAuthWithPasskey).Bind(
		collectionPathRateLimit("", "authWithPasskey", "auth"),
	)
}

var errPasskeyFailed = errors.New("passkey verification failed")

func findPasskeyCollection(e *core.RequestEvent) (*core.Collection, error) {
	collection, err := findAuthCollection(e)
	if err != nil {
		return nil, err
	}

	if !collection.Passkey.Enabled || collection.Name == core.CollectionNameSuperusers {
		return nil, e.ForbiddenError("The collection is not configured to allow passkey authentication.", nil)
	}

	return collection, nil
}

// newPasskeyWebAuthn creates the WebAuthn relying party for the collection.
func newPasskeyWebAuthn(app core.App, collection *core.Collection) (*webauthn.WebAuthn, error) {
	config := collection.Passkey

	appURL, _ := url.Parse(app.Settings().Meta.AppURL)

	rpId := config.RPID
	if rpId == "" && appURL != nil {
		rpId = appURL.Hostname()
	}
	if rpId == "" {
		return nil, errors.New("missing passkey relying party id (set the collection passkey rpId or the application URL setting)")
	}

	origins := config.Origins
	if len(origins) == 0 && appURL != nil && appURL.Host != "" {
		origins = []string{appURL.Scheme + "://" + appURL.Host}
	}
	if len(origins) == 0 {
		return nil, errors.New("missing passkey origins (set the collection passkey origins or the application URL setting)")
	}

	rpName := config.RPName
	if rpName == "" {
		rpName = app.Settings().Meta.AppName
	}
	if rpName == "" {
		rpName = rpId
	}

	return webauthn.New(&webauthn.Config{
		RPID:          rpId,
		RPDisplayName: rpName,
		RPOrigins:     origins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		},
	})
}

// passkeyUser adapts an auth record to the [webauthn.User] interface.
//
// The user handle is the record id (it is checked against the
// passkey record on login).
type passkeyUser struct {
	record      *core.Record
	credentials []webauthn.Credential
}

func (u *passkeyUser) WebAuthnID() []byte { return []byte(u.record.Id) }

func (u *passkeyUser) WebAuthnName() string {
	if email := u.record.Email(); email != "" {
		return email
	}
	if username := u.record.GetString("username"); username != "" {
		return username
	}
	return u.record.Id
}

func (u *passkeyUser) WebAuthnDisplayName() string {
	if name := u.record.GetString("name"); name != "" {
		return name
	}
	return u.WebAuthnName()
}

func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func decodePasskeyCredential(passkey *core.Passkey) (webauthn.Credential, error) {
	var credential webauthn.Credential
	err := json.Unmarshal(passkey.Credential(), &credential)
	return credential, err
}

func loadPasskeyUser(app core.App, record *core.Record) (*passkeyUser, error) {
	passkeys, err := app.FindAllPasskeysByRecord(record)
	if err != nil {
		return nil, err
	}

	user := &passkeyUser{record: record, credentials: make([]webauthn.Credential, 0, len(passkeys))}
	for _, passkey := range passkeys {
		credential, err := decodePasskeyCredential(passkey)
		if err != nil {
			app.Logger().Warn("Invalid stored passkey credential", "error", err, "passkeyId", passkey.Id)
			continue
		}
		user.credentials = append(user.credentials, credential)
	}

	return user, nil
}

func savePasskeySession(app core.App, collection *core.Collection, recordId string, kind string, data *webauthn.SessionData) (string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	session := core.NewPasskeySession(app)
	session.SetCollectionRef(collection.Id)
	session.SetRecordRef(recordId)
	session.SetKind(kind)
	session.SetData(raw)
	if err := app.Save(session); err != nil {
		return "", err
	}

	return session.Id, nil
}

// consumePasskeySession loads and deletes (single use) the specified ceremony session.
func consumePasskeySession(app core.App, collection *core.Collection, sessionId string, kind string) (*core.PasskeySession, webauthn.SessionData, error) {
	var data webauthn.SessionData

	if sessionId == "" {
		return nil, data, errPasskeyFailed
	}

	session, err := app.FindPasskeySessionById(sessionId)
	if err != nil {
		return nil, data, errPasskeyFailed
	}

	consumed, err := app.ConsumePasskeySession(session)
	if err != nil {
		return nil, data, err
	}

	if !consumed || session.HasExpired() || session.Kind() != kind || session.CollectionRef() != collection.Id {
		return nil, data, errPasskeyFailed
	}

	if err := json.Unmarshal(session.Data(), &data); err != nil {
		return nil, data, errPasskeyFailed
	}

	return session, data, nil
}

// -------------------------------------------------------------------
// Registration
// -------------------------------------------------------------------

type passkeyOptionsResponse struct {
	SessionId string `json:"sessionId"`
	Options   any    `json:"options"`
}

// beginPasskeyRegistration starts a registration ceremony for the provided auth record.
func beginPasskeyRegistration(app core.App, collection *core.Collection, record *core.Record) (*passkeyOptionsResponse, error) {
	wa, err := newPasskeyWebAuthn(app, collection)
	if err != nil {
		return nil, err
	}

	user, err := loadPasskeyUser(app, record)
	if err != nil {
		return nil, err
	}

	exclusions := make([]protocol.CredentialDescriptor, 0, len(user.credentials))
	for _, credential := range user.credentials {
		exclusions = append(exclusions, credential.Descriptor())
	}

	creation, data, err := wa.BeginRegistration(
		user,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(exclusions),
	)
	if err != nil {
		return nil, err
	}

	sessionId, err := savePasskeySession(app, collection, record.Id, core.PasskeySessionKindRegistration, data)
	if err != nil {
		return nil, err
	}

	return &passkeyOptionsResponse{SessionId: sessionId, Options: creation}, nil
}

func recordRequestPasskeyRegistration(e *core.RequestEvent) error {
	collection, err := findPasskeyCollection(e)
	if err != nil {
		return err
	}

	result, err := beginPasskeyRegistration(e.App, collection, e.Auth)
	if err != nil {
		return e.InternalServerError("Failed to start the passkey registration.", err)
	}

	return e.JSON(http.StatusOK, result)
}

type passkeyCredentialForm struct {
	SessionId  string          `json:"sessionId" form:"sessionId"`
	Name       string          `json:"name" form:"name"`
	Credential json.RawMessage `json:"credential" form:"credential"`
}

// finishPasskeyRegistration verifies the registration response and stores the new passkey.
//
// The ceremony session is bound to the record that started it, so the
// registration can be completed without auth (eg. from the hosted sign-in page).
func finishPasskeyRegistration(e *core.RequestEvent, collection *core.Collection, form *passkeyCredentialForm) (*core.Passkey, error) {
	session, data, err := consumePasskeySession(e.App, collection, form.SessionId, core.PasskeySessionKindRegistration)
	if err != nil {
		return nil, err
	}

	if e.Auth != nil && (e.Auth.Collection().Id != collection.Id || e.Auth.Id != session.RecordRef()) {
		return nil, errPasskeyFailed
	}

	record, err := e.App.FindRecordById(collection, session.RecordRef())
	if err != nil {
		return nil, errPasskeyFailed
	}

	parsed, err := protocol.ParseCredentialCreationResponseBytes(form.Credential)
	if err != nil {
		return nil, errors.Join(errPasskeyFailed, err)
	}

	wa, err := newPasskeyWebAuthn(e.App, collection)
	if err != nil {
		return nil, err
	}

	user, err := loadPasskeyUser(e.App, record)
	if err != nil {
		return nil, err
	}

	credential, err := wa.CreateCredential(user, data, parsed)
	if err != nil {
		return nil, errors.Join(errPasskeyFailed, err)
	}

	if _, err := e.App.FindPasskeyByCredentialId(credential.ID); err == nil {
		return nil, errors.Join(errPasskeyFailed, errors.New("the passkey is already registered"))
	}

	rawCredential, err := json.Marshal(credential)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(form.Name)
	if name == "" {
		name = "Passkey"
	}
	if len(name) > 255 {
		name = name[:255]
	}

	passkey := core.NewPasskey(e.App)
	passkey.SetCollectionRef(collection.Id)
	passkey.SetRecordRef(record.Id)
	passkey.SetName(name)
	passkey.SetCredentialId(credential.ID)
	passkey.SetCredential(rawCredential)
	if err := e.App.Save(passkey); err != nil {
		return nil, err
	}

	return passkey, nil
}

func recordConfirmPasskeyRegistration(e *core.RequestEvent) error {
	collection, err := findPasskeyCollection(e)
	if err != nil {
		return err
	}

	form := &passkeyCredentialForm{}
	if err := e.BindBody(form); err != nil {
		return e.BadRequestError("An error occurred while loading the submitted data.", err)
	}

	passkey, err := finishPasskeyRegistration(e, collection, form)
	if err != nil {
		if errors.Is(err, errPasskeyFailed) {
			return e.BadRequestError("Failed to register the passkey.", err)
		}
		return e.InternalServerError("Failed to register the passkey.", err)
	}

	return e.JSON(http.StatusOK, passkey.Record)
}

// -------------------------------------------------------------------
// Login
// -------------------------------------------------------------------

// beginPasskeyLogin starts a usernameless (discoverable credential) login ceremony.
func beginPasskeyLogin(app core.App, collection *core.Collection) (*passkeyOptionsResponse, error) {
	wa, err := newPasskeyWebAuthn(app, collection)
	if err != nil {
		return nil, err
	}

	assertion, data, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, err
	}

	sessionId, err := savePasskeySession(app, collection, "", core.PasskeySessionKindLogin, data)
	if err != nil {
		return nil, err
	}

	return &passkeyOptionsResponse{SessionId: sessionId, Options: assertion}, nil
}

func recordRequestPasskey(e *core.RequestEvent) error {
	collection, err := findPasskeyCollection(e)
	if err != nil {
		return err
	}

	result, err := beginPasskeyLogin(e.App, collection)
	if err != nil {
		return e.InternalServerError("Failed to start the passkey authentication.", err)
	}

	return e.JSON(http.StatusOK, result)
}

// verifyPasskeyLogin verifies the assertion and returns the matching auth record.
func verifyPasskeyLogin(e *core.RequestEvent, collection *core.Collection, form *passkeyCredentialForm) (*core.Record, error) {
	_, data, err := consumePasskeySession(e.App, collection, form.SessionId, core.PasskeySessionKindLogin)
	if err != nil {
		return nil, err
	}

	parsed, err := protocol.ParseCredentialRequestResponseBytes(form.Credential)
	if err != nil {
		return nil, errors.Join(errPasskeyFailed, err)
	}

	wa, err := newPasskeyWebAuthn(e.App, collection)
	if err != nil {
		return nil, err
	}

	var passkey *core.Passkey
	var record *core.Record

	// resolve the user from the credential id and the returned user handle
	handler := func(rawId, userHandle []byte) (webauthn.User, error) {
		found, err := e.App.FindPasskeyByCredentialId(rawId)
		if err != nil {
			return nil, errPasskeyFailed
		}

		if found.CollectionRef() != collection.Id || found.RecordRef() != string(userHandle) {
			return nil, errPasskeyFailed
		}

		foundRecord, err := e.App.FindRecordById(collection, found.RecordRef())
		if err != nil {
			return nil, errPasskeyFailed
		}

		credential, err := decodePasskeyCredential(found)
		if err != nil {
			return nil, errPasskeyFailed
		}

		passkey, record = found, foundRecord

		return &passkeyUser{record: foundRecord, credentials: []webauthn.Credential{credential}}, nil
	}

	credential, err := wa.ValidateDiscoverableLogin(handler, data, parsed)
	if err != nil {
		return nil, errors.Join(errPasskeyFailed, err)
	}

	if credential.Authenticator.CloneWarning {
		return nil, errors.Join(errPasskeyFailed, errors.New("possibly cloned authenticator (sign counter went backwards)"))
	}

	// persist the updated sign counter and flags
	rawCredential, err := json.Marshal(credential)
	if err != nil {
		return nil, err
	}
	passkey.SetCredential(rawCredential)
	passkey.SetLastUsed(types.NowDateTime())
	if err := e.App.Save(passkey); err != nil {
		return nil, err
	}

	return record, nil
}

func recordAuthWithPasskey(e *core.RequestEvent) error {
	collection, err := findPasskeyCollection(e)
	if err != nil {
		return err
	}

	form := &passkeyCredentialForm{}
	if err := e.BindBody(form); err != nil {
		return e.BadRequestError("An error occurred while loading the submitted data.", err)
	}

	e.Set(core.RequestEventKeyInfoContext, core.RequestInfoContextPasskey)

	record, err := verifyPasskeyLogin(e, collection, form)
	if err != nil {
		if errors.Is(err, errPasskeyFailed) {
			return e.BadRequestError("Failed to authenticate.", err)
		}
		return e.InternalServerError("Failed to authenticate.", err)
	}

	return RecordAuthResponse(e, record, core.MFAMethodPasskey, nil)
}
