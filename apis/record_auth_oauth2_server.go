package apis

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pocketbase/ozzo-validation/v4/is"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/pocketbase/pocketbase/tools/security"
)

// OAuth2 server endpoints of an auth collection.
//
// The collection acts as an authorization server for the authorization
// code grant (with optional PKCE S256). The issued access_token is the
// regular collection auth token, so it is accepted by every app instance
// that shares the same database. When the "openid" scope is requested,
// an HS256 id_token signed with the client secret is also returned.

const (
	oauth2ServerIdTokenDuration = time.Hour
	oauth2ServerMaxParamLength  = 1000
)

var oauth2ServerPKCERegex = regexp.MustCompile(`^[A-Za-z0-9\-\._~]{43,128}$`)

func bindRecordAuthOAuth2ServerApi(sub *router.RouterGroup[*core.RequestEvent]) {
	sub.GET("/oauth2-server/authorize", oauth2ServerAuthorizePage).Bind(
		collectionPathRateLimit("", "oauth2ServerAuthorize"),
	)
	sub.POST("/oauth2-server/authorize", oauth2ServerAuthorizeSubmit).Bind(
		collectionPathRateLimit("", "oauth2ServerAuthorize", "auth"),
		SkipSuccessActivityLog(), // the redirect url contains the authorization code
	)
	sub.POST("/oauth2-server/token", oauth2ServerToken).Bind(
		collectionPathRateLimit("", "oauth2ServerToken"),
		SkipSuccessActivityLog(),
	)
	sub.GET("/oauth2-server/userinfo", oauth2ServerUserInfo).Bind(
		RequireSameCollectionContextAuth(""),
	)
	sub.GET("/oauth2-server/.well-known/openid-configuration", oauth2ServerDiscovery)
	sub.GET("/oauth2-server/logout", oauth2ServerLogout)
	sub.POST("/oauth2-server/logout", oauth2ServerLogout)

	bindRecordAuthPopupApi(sub)
}

func findOAuth2ServerCollection(e *core.RequestEvent) (*core.Collection, error) {
	collection, err := findAuthCollection(e)
	if err != nil {
		return nil, err
	}

	if !collection.OAuth2Server.Enabled || collection.Name == core.CollectionNameSuperusers {
		return nil, e.NotFoundError("The collection is not configured as an OAuth2 server.", nil)
	}

	return collection, nil
}

// -------------------------------------------------------------------
// Authorization endpoint
// -------------------------------------------------------------------

type oauth2ServerAuthorizeParams struct {
	ResponseType        string `form:"response_type"`
	ClientId            string `form:"client_id"`
	RedirectURI         string `form:"redirect_uri"`
	State               string `form:"state"`
	Scope               string `form:"scope"`
	Nonce               string `form:"nonce"`
	CodeChallenge       string `form:"code_challenge"`
	CodeChallengeMethod string `form:"code_challenge_method"`
	Prompt              string `form:"prompt"`
}

type oauth2ServerAuthorizeForm struct {
	oauth2ServerAuthorizeParams
	Identity string `form:"identity"`
	Password string `form:"password"`

	// usernameless passkey sign-in (see the hosted page script)
	PasskeySession    string `form:"passkey_session"`
	PasskeyCredential string `form:"passkey_credential"`

	// Action is set by the account chooser ("continue" or "signout").
	Action string `form:"action"`

	// Provider is the name of the OAuth2 provider to sign in with.
	Provider string `form:"provider"`
}

func oauth2ServerParamsFromQuery(q url.Values) oauth2ServerAuthorizeParams {
	return oauth2ServerAuthorizeParams{
		ResponseType:        q.Get("response_type"),
		ClientId:            q.Get("client_id"),
		RedirectURI:         q.Get("redirect_uri"),
		State:               q.Get("state"),
		Scope:               q.Get("scope"),
		Nonce:               q.Get("nonce"),
		CodeChallenge:       q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"),
		Prompt:              q.Get("prompt"),
	}
}

// oauth2ServerAuthorizeError is returned for an invalid authorization request.
//
// When redirect is false the client or redirect uri can't be trusted
// and the error must be shown to the user instead of redirecting.
type oauth2ServerAuthorizeError struct {
	code        string
	description string
	redirect    bool
}

func (err *oauth2ServerAuthorizeError) Error() string {
	return err.code + ": " + err.description
}

func (p *oauth2ServerAuthorizeParams) validate(e *core.RequestEvent, collection *core.Collection) (core.OAuth2ServerClient, *oauth2ServerAuthorizeError) {
	var client core.OAuth2ServerClient

	if p.isPopup() {
		// popup sign-in: no registered client, the redirect_uri is the frontend origin
		if !collection.OAuth2Server.Popup.Enabled {
			return client, &oauth2ServerAuthorizeError{code: "invalid_client", description: "The popup sign-in is not enabled for this account type."}
		}

		if !isOAuth2ServerPopupOrigin(e, collection, p.RedirectURI) {
			return client, &oauth2ServerAuthorizeError{code: "invalid_request", description: "This site is not allowed to use the popup sign-in."}
		}

		client = oauth2ServerPopupClient(p.RedirectURI)

		// a silent sign-in would hand the token of the remembered user to the frontend
		// without asking, so the popup always shows the account chooser or the form
		if hasOAuth2ServerPrompt(p.Prompt, "none") {
			return client, &oauth2ServerAuthorizeError{code: "invalid_request", description: "prompt=none is not supported by the popup sign-in.", redirect: true}
		}
	} else {
		var ok bool
		client, ok = collection.OAuth2Server.GetClient(p.ClientId)
		if !ok {
			return client, &oauth2ServerAuthorizeError{code: "invalid_client", description: "Unknown or missing client_id."}
		}

		if !client.HasRedirectURI(p.RedirectURI) {
			return client, &oauth2ServerAuthorizeError{code: "invalid_request", description: "The redirect_uri is not registered for this client."}
		}
	}

	if p.ResponseType != "code" {
		return client, &oauth2ServerAuthorizeError{code: "unsupported_response_type", description: "Only response_type=code is supported.", redirect: true}
	}

	if len(p.State) > oauth2ServerMaxParamLength || len(p.Scope) > oauth2ServerMaxParamLength || len(p.Nonce) > oauth2ServerMaxParamLength {
		return client, &oauth2ServerAuthorizeError{code: "invalid_request", description: "Too long state, scope or nonce parameter.", redirect: true}
	}

	for _, prompt := range strings.Fields(p.Prompt) {
		if prompt != "none" && prompt != "login" && prompt != "consent" && prompt != "select_account" {
			return client, &oauth2ServerAuthorizeError{code: "invalid_request", description: "Unsupported prompt value.", redirect: true}
		}
		if prompt == "none" && len(strings.Fields(p.Prompt)) > 1 {
			return client, &oauth2ServerAuthorizeError{code: "invalid_request", description: "prompt=none can't be combined with other values.", redirect: true}
		}
	}

	// there is no client secret in the popup sign-in, so PKCE is required
	if p.CodeChallenge != "" || p.CodeChallengeMethod != "" || p.isPopup() {
		if p.CodeChallengeMethod != "S256" {
			return client, &oauth2ServerAuthorizeError{code: "invalid_request", description: "Only code_challenge_method=S256 is supported.", redirect: true}
		}
		if !oauth2ServerPKCERegex.MatchString(p.CodeChallenge) {
			return client, &oauth2ServerAuthorizeError{code: "invalid_request", description: "Invalid code_challenge.", redirect: true}
		}
	}

	return client, nil
}

func oauth2ServerRedirectURL(redirectURI string, params map[string]string) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return redirectURI
	}

	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()

	return u.String()
}

func oauth2ServerAuthorizePage(e *core.RequestEvent) error {
	collection, err := findOAuth2ServerCollection(e)
	if err != nil {
		return err
	}

	params := oauth2ServerParamsFromQuery(e.Request.URL.Query())

	client, authErr := params.validate(e, collection)
	if authErr != nil {
		return oauth2ServerAuthorizeFailure(e, collection, params, authErr)
	}

	// remembered sign-in
	//  - prompt=login  -> always show the sign-in form
	//  - prompt=none   -> complete silently (no page can be shown)
	//  - otherwise     -> let the user choose: continue, use another account or sign out
	if !hasOAuth2ServerPrompt(params.Prompt, "login") {
		if record := readOAuth2ServerSession(e, collection); record != nil {
			message, err := checkOAuth2ServerSignIn(e, collection, record)
			if err != nil {
				return err
			}

			if message == "" {
				if hasOAuth2ServerPrompt(params.Prompt, "none") {
					continueURL, err := issueOAuth2ServerCode(e, collection, client, params, record)
					if err != nil {
						return e.InternalServerError("Failed to create authorization code.", err)
					}

					return e.Redirect(http.StatusFound, continueURL)
				}

				return renderOAuth2ServerAccountChooser(e, collection, client, params, record)
			}

			clearOAuth2ServerSession(e, collection)
		}
	}

	if hasOAuth2ServerPrompt(params.Prompt, "none") {
		return oauth2ServerAuthorizeFailure(e, collection, params, &oauth2ServerAuthorizeError{
			code:        "login_required",
			description: "The user is not signed in.",
			redirect:    true,
		})
	}

	return renderOAuth2ServerLoginPage(e, http.StatusOK, collection, client, params, "", "")
}

// isOAuth2ServerSameSitePost reports whether the form post comes from the hosted page.
//
// Browsers send Sec-Fetch-Site (and Origin for posts), other clients send none of them.
func isOAuth2ServerSameSitePost(e *core.RequestEvent) bool {
	// an opaque origin never comes from the hosted page
	if e.Request.Header.Get("Origin") == "null" {
		return false
	}

	if site := e.Request.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}

	if origin := e.Request.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return false // includes the opaque "null" origin
		}

		got := strings.ToLower(u.Scheme + "://" + u.Host)

		if got == strings.ToLower(oauth2ServerBaseURL(e)) {
			return true
		}

		appURL, err := url.Parse(e.App.Settings().Meta.AppURL)

		return err == nil && appURL.Host != "" && got == strings.ToLower(appURL.Scheme+"://"+appURL.Host)
	}

	return true
}

func oauth2ServerAuthorizeSubmit(e *core.RequestEvent) error {
	collection, err := findOAuth2ServerCollection(e)
	if err != nil {
		return err
	}

	// the forms are posted by the hosted page itself; a cross-site post could otherwise
	// start a provider sign-in (and get a code back) without the user seeing the page
	if !isOAuth2ServerSameSitePost(e) {
		return renderOAuth2ServerPage(e, http.StatusForbidden, oauth2ServerPageData{
			AppName:    e.App.Settings().Meta.AppName,
			ErrorTitle: "Invalid sign-in request",
			Error:      "The sign-in form must be submitted from this page.",
		})
	}

	form := &oauth2ServerAuthorizeForm{}
	if err := e.BindBody(form); err != nil {
		return e.BadRequestError("An error occurred while loading the submitted data.", err)
	}

	client, authErr := form.validate(e, collection)
	if authErr != nil {
		return oauth2ServerAuthorizeFailure(e, collection, form.oauth2ServerAuthorizeParams, authErr)
	}

	loginFailed := func(message string) error {
		return renderOAuth2ServerLoginPage(e, http.StatusBadRequest, collection, client, form.oauth2ServerAuthorizeParams, form.Identity, message)
	}

	if form.Provider != "" {
		return oauth2ServerProviderStart(e, collection, client, form.oauth2ServerAuthorizeParams, form.Provider)
	}

	switch form.Action {
	case "continue":
		// continue with the remembered account
		// (the session cookie is SameSite=Lax so it is not sent with cross-site posts)
		record := readOAuth2ServerSession(e, collection)
		if record == nil {
			return loginFailed("Your saved sign-in has expired. Please sign in again.")
		}

		message, err := checkOAuth2ServerSignIn(e, collection, record)
		if err != nil {
			return err
		}
		if message != "" {
			clearOAuth2ServerSession(e, collection)
			return loginFailed(message)
		}

		continueURL, err := issueOAuth2ServerCode(e, collection, client, form.oauth2ServerAuthorizeParams, record)
		if err != nil {
			return e.InternalServerError("Failed to create authorization code.", err)
		}

		return e.Redirect(http.StatusSeeOther, continueURL)
	case "signout":
		clearOAuth2ServerSession(e, collection)
		return renderOAuth2ServerLoginPage(e, http.StatusOK, collection, client, form.oauth2ServerAuthorizeParams, "", "")
	}

	var record *core.Record
	var authMethod string

	if form.PasskeySession != "" {
		// usernameless passkey sign-in
		// ---
		if !collection.Passkey.Enabled {
			return loginFailed("Passkey sign-in is disabled for this account type.")
		}

		e.Set(core.RequestEventKeyInfoContext, core.RequestInfoContextPasskey)

		record, err = verifyPasskeyLogin(e, collection, &passkeyCredentialForm{
			SessionId:  form.PasskeySession,
			Credential: json.RawMessage(form.PasskeyCredential),
		})
		if err != nil {
			if !errors.Is(err, errPasskeyFailed) {
				e.App.Logger().Warn("[oauth2Server] Passkey sign-in error", "error", err)
			}
			return loginFailed("Passkey sign-in failed. Try again or use your password.")
		}

		authMethod = core.MFAMethodPasskey
	} else {
		// identity/password sign-in
		// ---
		if !collection.PasswordAuth.Enabled {
			return loginFailed("Password sign-in is disabled for this account type.")
		}

		if form.Identity == "" || form.Password == "" || len(form.Identity) > 255 || len(form.Password) > 255 {
			return loginFailed("Enter your credentials.")
		}

		e.Set(core.RequestEventKeyInfoContext, core.RequestInfoContextPasswordAuth)

		found, findErr := findAuthRecordByPasswordIdentity(e.App, collection, form.Identity)
		if findErr != nil && !errors.Is(findErr, sql.ErrNoRows) {
			return e.InternalServerError("", findErr)
		}

		if found == nil || !found.ValidatePassword(form.Password) {
			if found == nil {
				dummyPasswordCheck(e.App, collection)
			}
			return loginFailed("Invalid login credentials.")
		}

		record = found
		authMethod = core.MFAMethodPassword
	}

	return oauth2ServerCompleteSignIn(e, collection, client, form.oauth2ServerAuthorizeParams, record, authMethod, form.Identity)
}

// oauth2ServerCompleteSignIn finishes the hosted page sign-in of an
// already authenticated record and sends the code back to the client.
func oauth2ServerCompleteSignIn(
	e *core.RequestEvent,
	collection *core.Collection,
	client core.OAuth2ServerClient,
	params oauth2ServerAuthorizeParams,
	record *core.Record,
	authMethod string,
	identity string,
) error {
	message, err := checkOAuth2ServerSignIn(e, collection, record)
	if err != nil {
		return err
	}
	if message != "" {
		return renderOAuth2ServerLoginPage(e, http.StatusBadRequest, collection, client, params, identity, message)
	}

	if collection.AuthAlert.Enabled {
		if err := authAlert(e, record); err != nil {
			e.App.Logger().Warn("[oauth2Server] Failed to send login alert", "error", err)
		}
	}

	continueURL, err := issueOAuth2ServerCode(e, collection, client, params, record)
	if err != nil {
		return e.InternalServerError("Failed to create authorization code.", err)
	}

	// remember the sign-in for the next authorization requests (single sign-on)
	setOAuth2ServerSession(e, collection, record)

	// offer to create a passkey to users that signed in with a password and don't have one yet
	if authMethod == core.MFAMethodPassword && collection.Passkey.Enabled {
		if page, ok := oauth2ServerPasskeyOffer(e, collection, client, record, continueURL); ok {
			return page
		}
	}

	return e.Redirect(http.StatusSeeOther, continueURL)
}

// oauth2ServerPasskeyOffer renders the "add a passkey" step if the record doesn't have any passkey.
func oauth2ServerPasskeyOffer(e *core.RequestEvent, collection *core.Collection, client core.OAuth2ServerClient, record *core.Record, continueURL string) (error, bool) {
	passkeys, err := e.App.FindAllPasskeysByRecord(record)
	if err != nil || len(passkeys) > 0 {
		return nil, false
	}

	registration, err := beginPasskeyRegistration(e.App, collection, record)
	if err != nil {
		e.App.Logger().Warn("[oauth2Server] Failed to start the passkey registration", "error", err)
		return nil, false
	}

	return renderOAuth2ServerPage(e, http.StatusOK, oauth2ServerPageData{
		AppName:      e.App.Settings().Meta.AppName,
		ClientName:   oauth2ServerClientName(client),
		Mode:         "addPasskey",
		ContinueURL:  template.URL(continueURL),
		Registration: registration,
		PasskeyURL:   oauth2ServerCollectionAPIURL(collection) + "/confirm-passkey-registration",
	}), true
}

func oauth2ServerAuthorizeFailure(e *core.RequestEvent, collection *core.Collection, params oauth2ServerAuthorizeParams, authErr *oauth2ServerAuthorizeError) error {
	if authErr.redirect {
		return e.Redirect(http.StatusSeeOther, oauth2ServerReturnURL(collection, params, map[string]string{
			"error":             authErr.code,
			"error_description": authErr.description,
			"state":             params.State,
		}))
	}

	return renderOAuth2ServerPage(e, http.StatusBadRequest, oauth2ServerPageData{
		AppName:    e.App.Settings().Meta.AppName,
		ErrorTitle: "Invalid authorization request",
		Error:      authErr.description,
	})
}

// findAuthRecordByPasswordIdentity looks up the auth record by the collection
// password identity fields (email is checked first).
func findAuthRecordByPasswordIdentity(app core.App, collection *core.Collection, identity string) (*core.Record, error) {
	identityFields := slices.Clone(collection.PasswordAuth.IdentityFields)
	slices.SortStableFunc(identityFields, func(a, b string) int {
		if a == core.FieldNameEmail {
			return -1
		}
		if b == core.FieldNameEmail {
			return 1
		}
		return 0
	})

	var lastErr error = sql.ErrNoRows
	for _, name := range identityFields {
		if name == core.FieldNameEmail && is.EmailFormat.Validate(identity) != nil {
			continue
		}

		record, err := findRecordByIdentityField(app, collection, name, identity)
		if err == nil {
			return record, nil
		}
		lastErr = err
	}

	return nil, lastErr
}

// -------------------------------------------------------------------
// Token endpoint
// -------------------------------------------------------------------

type oauth2ServerTokenForm struct {
	GrantType    string `form:"grant_type"`
	Code         string `form:"code"`
	RedirectURI  string `form:"redirect_uri"`
	ClientId     string `form:"client_id"`
	ClientSecret string `form:"client_secret"`
	CodeVerifier string `form:"code_verifier"`
}

func oauth2ServerTokenError(e *core.RequestEvent, status int, code string, description string) error {
	return e.JSON(status, map[string]string{
		"error":             code,
		"error_description": description,
	})
}

func oauth2ServerToken(e *core.RequestEvent) error {
	e.Response.Header().Set("Cache-Control", "no-store")
	e.Response.Header().Set("Pragma", "no-cache")

	collection, err := findOAuth2ServerCollection(e)
	if err != nil {
		return err
	}

	form := &oauth2ServerTokenForm{}
	if err := e.BindBody(form); err != nil {
		return oauth2ServerTokenError(e, http.StatusBadRequest, "invalid_request", "Failed to read the request body.")
	}

	// client authentication (client_secret_basic or client_secret_post)
	// ---
	basicId, basicSecret, hasBasic := e.Request.BasicAuth()
	if hasBasic {
		// RFC 6749 section 2.3.1: the credentials are form-urlencoded
		if v, err := url.QueryUnescape(basicId); err == nil {
			basicId = v
		}
		if v, err := url.QueryUnescape(basicSecret); err == nil {
			basicSecret = v
		}

		if form.ClientSecret != "" || (form.ClientId != "" && form.ClientId != basicId) {
			return oauth2ServerTokenError(e, http.StatusBadRequest, "invalid_request", "Use only one client authentication method.")
		}

		form.ClientId = basicId
		form.ClientSecret = basicSecret
	}

	client, ok := collection.OAuth2Server.GetClient(form.ClientId)
	if !ok || !client.ValidateSecret(form.ClientSecret) {
		if hasBasic {
			e.Response.Header().Set("WWW-Authenticate", `Basic realm="oauth2"`)
		}
		return oauth2ServerTokenError(e, http.StatusUnauthorized, "invalid_client", "Client authentication failed.")
	}

	if form.GrantType != "authorization_code" {
		return oauth2ServerTokenError(e, http.StatusBadRequest, "unsupported_grant_type", "Only grant_type=authorization_code is supported.")
	}

	if form.Code == "" {
		return oauth2ServerTokenError(e, http.StatusBadRequest, "invalid_request", "Missing code.")
	}

	// lookup and consume the code (single use)
	// ---
	invalidGrant := func() error {
		return oauth2ServerTokenError(e, http.StatusBadRequest, "invalid_grant", "Invalid, expired or already used authorization code.")
	}

	code, err := e.App.FindOAuth2CodeByCode(form.Code)
	if err != nil {
		return invalidGrant()
	}

	// a code that belongs to another client (or collection) must not be burned by this request
	if code.CollectionRef() != collection.Id || code.ClientId() != client.Id {
		return invalidGrant()
	}

	consumed, err := e.App.ConsumeOAuth2Code(code)
	if err != nil {
		return e.InternalServerError("Failed to consume the authorization code.", err)
	}
	if !consumed {
		return invalidGrant()
	}

	if code.CollectionRef() != collection.Id ||
		code.ClientId() != client.Id ||
		code.HasExpired(collection.OAuth2Server.CodeDurationTime()) ||
		subtle.ConstantTimeCompare([]byte(code.RedirectURI()), []byte(form.RedirectURI)) != 1 {
		return invalidGrant()
	}

	// PKCE
	// ---
	if code.CodeChallenge() != "" {
		if !oauth2ServerPKCERegex.MatchString(form.CodeVerifier) {
			return invalidGrant()
		}

		hash := sha256.Sum256([]byte(form.CodeVerifier))
		challenge := base64.RawURLEncoding.EncodeToString(hash[:])
		if subtle.ConstantTimeCompare([]byte(challenge), []byte(code.CodeChallenge())) != 1 {
			return invalidGrant()
		}
	} else if form.CodeVerifier != "" {
		// prevent PKCE downgrade
		return invalidGrant()
	}

	record, err := e.App.FindRecordById(collection, code.RecordRef())
	if err != nil {
		return invalidGrant()
	}

	accessToken, err := record.NewAuthToken()
	if err != nil {
		return e.InternalServerError("Failed to create auth token.", err)
	}

	result := map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   collection.AuthToken.Duration,
	}

	if code.Scope() != "" {
		result["scope"] = code.Scope()
	}

	if hasOAuth2ServerScope(code.Scope(), "openid") {
		idToken, err := newOAuth2ServerIdToken(e, collection, client, record, code)
		if err != nil {
			return e.InternalServerError("Failed to create id_token.", err)
		}
		result["id_token"] = idToken
	}

	return e.JSON(http.StatusOK, result)
}

func hasOAuth2ServerScope(scope string, name string) bool {
	return slices.Contains(strings.Fields(scope), name)
}

// oauth2ServerProfileClaims returns the standard claims for the
// requested scopes that the record has fields for.
func oauth2ServerProfileClaims(record *core.Record, scope string) map[string]any {
	claims := map[string]any{}

	if hasOAuth2ServerScope(scope, "email") && record.Email() != "" {
		claims["email"] = record.Email()
		claims["email_verified"] = record.Verified()
	}

	if hasOAuth2ServerScope(scope, "profile") {
		fields := record.Collection().Fields
		if fields.GetByName("name") != nil {
			claims["name"] = record.GetString("name")
		}
		if fields.GetByName("username") != nil {
			claims["preferred_username"] = record.GetString("username")
		}
	}

	return claims
}

func newOAuth2ServerIdToken(e *core.RequestEvent, collection *core.Collection, client core.OAuth2ServerClient, record *core.Record, code *core.OAuth2Code) (string, error) {
	claims := jwt.MapClaims{
		"iss":       oauth2ServerIssuer(e, collection),
		"sub":       record.Id,
		"aud":       client.Id,
		"iat":       time.Now().Unix(),
		"auth_time": code.Created().Time().Unix(),
	}

	if code.Nonce() != "" {
		claims["nonce"] = code.Nonce()
	}

	for k, v := range oauth2ServerProfileClaims(record, code.Scope()) {
		claims[k] = v
	}

	return security.NewJWT(claims, client.Secret, oauth2ServerIdTokenDuration)
}

// -------------------------------------------------------------------
// UserInfo and discovery
// -------------------------------------------------------------------

func oauth2ServerUserInfo(e *core.RequestEvent) error {
	if _, err := findOAuth2ServerCollection(e); err != nil {
		return err
	}

	record := e.Auth.Clone()
	record.IgnoreEmailVisibility(true)

	if err := EnrichRecord(e, record); err != nil {
		return e.InternalServerError("Failed to load the auth record.", err)
	}

	result := oauth2ServerProfileClaims(record, "email profile")
	result["sub"] = record.Id
	result["record"] = record

	return e.JSON(http.StatusOK, result)
}

func oauth2ServerIssuer(e *core.RequestEvent, collection *core.Collection) string {
	base := strings.TrimRight(e.App.Settings().Meta.AppURL, "/")
	if base == "" {
		scheme := "http"
		if e.IsTLS() {
			scheme = "https"
		}
		base = scheme + "://" + e.Request.Host
	}

	return base + "/api/collections/" + url.PathEscape(collection.Name) + "/oauth2-server"
}

func oauth2ServerDiscovery(e *core.RequestEvent) error {
	collection, err := findOAuth2ServerCollection(e)
	if err != nil {
		return err
	}

	issuer := oauth2ServerIssuer(e, collection)

	return e.JSON(http.StatusOK, map[string]any{
		"issuer":                                         issuer,
		"authorization_endpoint":                         issuer + "/authorize",
		"token_endpoint":                                 issuer + "/token",
		"userinfo_endpoint":                              issuer + "/userinfo",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code"},
		"subject_types_supported":                        []string{"public"},
		"id_token_signing_alg_values_supported":          []string{"HS256"},
		"token_endpoint_auth_methods_supported":          []string{"client_secret_basic", "client_secret_post"},
		"code_challenge_methods_supported":               []string{"S256"},
		"scopes_supported":                               []string{"openid", "email", "profile"},
		"claims_supported":                               []string{"sub", "iss", "aud", "exp", "iat", "auth_time", "nonce", "email", "email_verified", "name", "preferred_username"},
		"authorization_response_iss_parameter_supported": true,
		"end_session_endpoint":                           issuer + "/logout",
		"prompt_values_supported":                        []string{"none", "login", "consent", "select_account"},
	})
}

// -------------------------------------------------------------------
// Hosted login page
// -------------------------------------------------------------------

type oauth2ServerPageData struct {
	// Mode is one of "" (sign-in form), "chooseAccount", "addPasskey", "popupDone";
	// ErrorTitle switches to an error page.
	Mode string

	AppName       string
	ClientName    string
	IdentityLabel string
	Identity      string
	Error         string
	ErrorTitle    string
	CancelURL     template.URL // already validated against the registered client redirect URIs
	ContinueURL   template.URL // already validated against the registered client redirect URIs
	Params        oauth2ServerAuthorizeParams

	PasswordEnabled bool
	PasskeyEnabled  bool

	// PasskeyURL is the passkey api endpoint used by the page script.
	PasskeyURL string

	// Registration holds the passkey registration options for the "addPasskey" mode.
	Registration *passkeyOptionsResponse

	// remembered account for the "chooseAccount" mode
	AccountName     string
	AccountEmail    string
	AccountInitial  string
	OtherAccountURL template.URL

	// message and target origin for the "popupDone" mode
	PopupMessage map[string]string
	PopupOrigin  string

	// FormAction is the authorize endpoint that the page forms are submitted to.
	FormAction string

	// Providers lists the enabled OAuth2 providers of the sign-in form.
	Providers []oauth2ServerPageProvider

	// ScriptNonce is the CSP nonce of the inline script.
	ScriptNonce string
}

func oauth2ServerClientName(client core.OAuth2ServerClient) string {
	if client.Name != "" {
		return client.Name
	}
	return client.Id
}

func oauth2ServerCollectionAPIURL(collection *core.Collection) string {
	return "/api/collections/" + url.PathEscape(collection.Name)
}

func renderOAuth2ServerLoginPage(
	e *core.RequestEvent,
	status int,
	collection *core.Collection,
	client core.OAuth2ServerClient,
	params oauth2ServerAuthorizeParams,
	identity string,
	errMessage string,
) error {
	return renderOAuth2ServerPage(e, status, oauth2ServerPageData{
		AppName:         e.App.Settings().Meta.AppName,
		ClientName:      oauth2ServerClientName(client),
		IdentityLabel:   oauth2ServerIdentityLabel(collection),
		Identity:        identity,
		Error:           errMessage,
		Params:          params,
		PasswordEnabled: collection.PasswordAuth.Enabled,
		PasskeyEnabled:  collection.Passkey.Enabled,
		PasskeyURL:      oauth2ServerCollectionAPIURL(collection) + "/request-passkey",
		FormAction:      oauth2ServerCollectionAPIURL(collection) + "/oauth2-server/authorize",
		Providers:       oauth2ServerPageProviders(e, collection),
		CancelURL: template.URL(oauth2ServerReturnURL(collection, params, map[string]string{
			"error":             "access_denied",
			"error_description": "The user canceled the sign-in.",
			"state":             params.State,
		})),
	})
}

// renderOAuth2ServerAccountChooser renders the "continue as" page for the remembered account.
func renderOAuth2ServerAccountChooser(
	e *core.RequestEvent,
	collection *core.Collection,
	client core.OAuth2ServerClient,
	params oauth2ServerAuthorizeParams,
	record *core.Record,
) error {
	name := record.GetString("name")
	email := record.Email()
	if name == "" {
		name = record.GetString("username")
	}
	if name == "" {
		name, email = email, ""
	}
	if name == "" {
		name = record.Id
	}

	initial := ""
	for _, r := range name {
		initial = string(r)
		break
	}

	// same authorization request but forcing the sign-in form
	other := oauth2ServerAuthorizeQuery(params)
	other.Set("prompt", "login")

	return renderOAuth2ServerPage(e, http.StatusOK, oauth2ServerPageData{
		Mode:            "chooseAccount",
		FormAction:      oauth2ServerCollectionAPIURL(collection) + "/oauth2-server/authorize",
		AppName:         e.App.Settings().Meta.AppName,
		ClientName:      oauth2ServerClientName(client),
		Params:          params,
		AccountName:     name,
		AccountEmail:    email,
		AccountInitial:  initial,
		OtherAccountURL: template.URL(oauth2ServerCollectionAPIURL(collection) + "/oauth2-server/authorize?" + other.Encode()),
		CancelURL: template.URL(oauth2ServerReturnURL(collection, params, map[string]string{
			"error":             "access_denied",
			"error_description": "The user canceled the sign-in.",
			"state":             params.State,
		})),
	})
}

func oauth2ServerAuthorizeQuery(p oauth2ServerAuthorizeParams) url.Values {
	q := url.Values{}
	for k, v := range map[string]string{
		"response_type":         p.ResponseType,
		"client_id":             p.ClientId,
		"redirect_uri":          p.RedirectURI,
		"state":                 p.State,
		"scope":                 p.Scope,
		"nonce":                 p.Nonce,
		"code_challenge":        p.CodeChallenge,
		"code_challenge_method": p.CodeChallengeMethod,
	} {
		if v != "" {
			q.Set(k, v)
		}
	}
	return q
}

func oauth2ServerIdentityLabel(collection *core.Collection) string {
	if len(collection.PasswordAuth.IdentityFields) == 0 {
		return "Email"
	}

	label := strings.Join(collection.PasswordAuth.IdentityFields, " or ")
	return strings.ToUpper(label[:1]) + label[1:]
}

func renderOAuth2ServerPage(e *core.RequestEvent, status int, data oauth2ServerPageData) error {
	data.ScriptNonce = security.RandomString(24)

	var buf bytes.Buffer
	if err := oauth2ServerPageTemplate.Execute(&buf, data); err != nil {
		return e.InternalServerError("Failed to render the sign-in page.", err)
	}

	header := e.Response.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	// the default same-origin policy would cut the popup from the frontend that opened it
	// (window.opener), which the popup sign-in needs to return the code
	header.Set("Cross-Origin-Opener-Policy", "unsafe-none")
	header.Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+data.ScriptNonce+"'; connect-src 'self'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")

	return e.HTML(status, buf.String())
}

var oauth2ServerPageTemplate = template.Must(template.New("oauth2Server").Parse(`{{define "params"}}<input type="hidden" name="response_type" value="{{.ResponseType}}">
<input type="hidden" name="client_id" value="{{.ClientId}}">
<input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
<input type="hidden" name="state" value="{{.State}}">
<input type="hidden" name="scope" value="{{.Scope}}">
<input type="hidden" name="nonce" value="{{.Nonce}}">
<input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
<input type="hidden" name="code_challenge_method" value="{{.CodeChallengeMethod}}">{{end}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Sign in{{if .AppName}} · {{.AppName}}{{end}}</title>
<style>
:root{--bg:#f4f6f8;--card:#fff;--txt:#16161a;--hint:#666f75;--border:#d6dce1;--accent:#16161a;--accent-txt:#fff;--danger:#e34562;--danger-bg:#fde8ec}
@media (prefers-color-scheme:dark){:root{--bg:#121417;--card:#1c1f23;--txt:#e9ecef;--hint:#9aa3ab;--border:#343a40;--accent:#e9ecef;--accent-txt:#121417;--danger:#ff7a90;--danger-bg:#3a1f26}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:16px;background:var(--bg);color:var(--txt);font:15px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{width:100%;max-width:380px;background:var(--card);border:1px solid var(--border);border-radius:12px;padding:28px 24px}
h1{margin:0 0 4px;font-size:20px}
p.sub{margin:0 0 20px;color:var(--hint)}
label{display:block;margin:0 0 4px;font-weight:600;font-size:13px}
input{width:100%;margin:0 0 14px;padding:10px 12px;border:1px solid var(--border);border-radius:8px;background:transparent;color:inherit;font:inherit}
input:focus{outline:2px solid var(--accent);outline-offset:1px}
button,.btn{display:block;width:100%;padding:11px;border:0;border-radius:8px;background:var(--accent);color:var(--accent-txt);font:inherit;font-weight:600;text-align:center;text-decoration:none;cursor:pointer}
button.secondary,.btn.secondary{background:transparent;color:var(--txt);border:1px solid var(--border)}
button:disabled{opacity:.6;cursor:default}
.error{margin:0 0 16px;padding:10px 12px;border-radius:8px;background:var(--danger-bg);color:var(--danger)}
.divider{display:flex;align-items:center;gap:10px;margin:16px 0;color:var(--hint);font-size:13px}
.divider:before,.divider:after{content:"";flex:1;border-top:1px solid var(--border)}
.cancel{display:block;margin-top:14px;text-align:center;color:var(--hint)}
.stack{display:grid;gap:10px}
[hidden]{display:none!important}
form.account{margin:0 0 14px}
.account-btn{display:flex;align-items:center;gap:12px;text-align:left;background:transparent;color:var(--txt);border:1px solid var(--border);padding:12px}
.account-btn:hover{border-color:var(--accent)}
.avatar{flex:none;width:40px;height:40px;border-radius:50%;display:flex;align-items:center;justify-content:center;background:var(--accent);color:var(--accent-txt);font-weight:700;text-transform:uppercase}
.account-info{display:flex;flex-direction:column;min-width:0}
.account-name{font-weight:600}
.account-email{color:var(--hint);font-weight:400;font-size:13px;overflow:hidden;text-overflow:ellipsis}
</style>
</head>
<body>
<main>
{{if .ErrorTitle}}
<h1>{{.ErrorTitle}}</h1>
<p class="sub">{{.Error}}</p>
{{else if eq .Mode "popupDone"}}
<h1>You can close this window</h1>
<p class="sub">Return to the page that opened it to continue.</p>
<script nonce="{{.ScriptNonce}}">
(function () {
  if (!window.opener) return;
  window.opener.postMessage({{.PopupMessage}}, {{.PopupOrigin}});
  window.close();
})();
</script>
{{else if eq .Mode "chooseAccount"}}
<h1>Choose an account</h1>
<p class="sub">to continue to <strong>{{.ClientName}}</strong></p>
{{if .Error}}<div class="error" role="alert">{{.Error}}</div>{{end}}
<form method="post" action="{{.FormAction}}" class="account">
{{template "params" .Params}}
<input type="hidden" name="action" value="continue">
<button type="submit" class="account-btn">
<span class="avatar" aria-hidden="true">{{.AccountInitial}}</span>
<span class="account-info"><span class="account-name">Continue as {{.AccountName}}</span>{{if .AccountEmail}}<span class="account-email">{{.AccountEmail}}</span>{{end}}</span>
</button>
</form>
<div class="stack">
<a class="btn secondary" href="{{.OtherAccountURL}}">Use another account</a>
<form method="post" action="{{.FormAction}}">
{{template "params" .Params}}
<input type="hidden" name="action" value="signout">
<button type="submit" class="secondary">Sign out</button>
</form>
</div>
<a class="cancel" href="{{.CancelURL}}">Cancel</a>
{{else if eq .Mode "addPasskey"}}
<h1>Sign in faster next time</h1>
<p class="sub">Add a passkey to sign in to <strong>{{.ClientName}}</strong> with your fingerprint, face or screen lock. No email or password needed.</p>
<div class="error" role="alert" id="pk-error" hidden></div>
<div class="stack">
<button type="button" id="pk-create">Add a passkey</button>
<a class="btn secondary" id="pk-skip" href="{{.ContinueURL}}">Not now</a>
</div>
<script nonce="{{.ScriptNonce}}">
(function () {
  var registration = {{.Registration}};
  var endpoint = {{.PasskeyURL}};
  var continueURL = document.getElementById("pk-skip").href;
  var btn = document.getElementById("pk-create");
  var errBox = document.getElementById("pk-error");
  if (!window.PublicKeyCredential) {
    btn.hidden = true;
    return;
  }
  function b64ToBuf(v) {
    v = v.replace(/-/g, "+").replace(/_/g, "/");
    while (v.length % 4) v += "=";
    var s = atob(v), out = new Uint8Array(s.length);
    for (var i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
    return out.buffer;
  }
  function bufToB64(buf) {
    var bytes = new Uint8Array(buf), s = "";
    for (var i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
    return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }
  function creationOptions(pk) {
    if (PublicKeyCredential.parseCreationOptionsFromJSON) {
      return PublicKeyCredential.parseCreationOptionsFromJSON(pk);
    }
    var o = Object.assign({}, pk);
    o.challenge = b64ToBuf(pk.challenge);
    o.user = Object.assign({}, pk.user, { id: b64ToBuf(pk.user.id) });
    o.excludeCredentials = (pk.excludeCredentials || []).map(function (c) {
      return Object.assign({}, c, { id: b64ToBuf(c.id) });
    });
    return o;
  }
  function credentialJSON(cred) {
    if (cred.toJSON) return cred.toJSON();
    return {
      id: cred.id,
      rawId: bufToB64(cred.rawId),
      type: cred.type,
      authenticatorAttachment: cred.authenticatorAttachment,
      clientExtensionResults: cred.getClientExtensionResults(),
      response: {
        clientDataJSON: bufToB64(cred.response.clientDataJSON),
        attestationObject: bufToB64(cred.response.attestationObject),
        transports: cred.response.getTransports ? cred.response.getTransports() : []
      }
    };
  }
  btn.addEventListener("click", function () {
    btn.disabled = true;
    errBox.hidden = true;
    navigator.credentials.create({ publicKey: creationOptions(registration.options.publicKey) })
      .then(function (cred) {
        return fetch(endpoint, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ sessionId: registration.sessionId, credential: credentialJSON(cred) })
        });
      })
      .then(function (res) {
        if (!res.ok) throw new Error("registration failed");
        window.location.assign(continueURL);
      })
      .catch(function () {
        errBox.textContent = "The passkey wasn't added. You can continue without it.";
        errBox.hidden = false;
        btn.hidden = true;
      });
  });
})();
</script>
{{else}}
<h1>Sign in{{if .AppName}} to {{.AppName}}{{end}}</h1>
<p class="sub">to continue to <strong>{{.ClientName}}</strong></p>
{{if .Error}}<div class="error" role="alert">{{.Error}}</div>{{end}}
<div class="error" role="alert" id="pk-error" hidden></div>
{{if .PasskeyEnabled}}
<form method="post" action="{{.FormAction}}" id="pk-form" hidden>
{{template "params" .Params}}
<input type="hidden" name="passkey_session" id="pk-session">
<input type="hidden" name="passkey_credential" id="pk-credential">
</form>
<button type="button" id="pk-signin" hidden>Sign in with a passkey</button>
{{if .PasswordEnabled}}<div class="divider" id="pk-divider" hidden>or</div>{{end}}
{{end}}
{{if .PasswordEnabled}}
<form method="post" action="{{.FormAction}}" autocomplete="on">
{{template "params" .Params}}
<label for="identity">{{.IdentityLabel}}</label>
<input id="identity" name="identity" value="{{.Identity}}" autocomplete="username" required>
<label for="password">Password</label>
<input id="password" name="password" type="password" autocomplete="current-password" required>
<button type="submit" class="{{if .PasskeyEnabled}}secondary{{end}}">Sign in</button>
</form>
{{end}}
{{if .Providers}}
{{if or .PasswordEnabled .PasskeyEnabled}}<div class="divider">or</div>{{end}}
<div class="stack">
{{range .Providers}}<form method="post" action="{{$.FormAction}}">
{{template "params" $.Params}}
<input type="hidden" name="provider" value="{{.Name}}">
<button type="submit" class="secondary">Continue with {{.DisplayName}}</button>
</form>
{{end}}</div>
{{end}}
<a class="cancel" href="{{.CancelURL}}">Cancel</a>
{{if .PasskeyEnabled}}
<script nonce="{{.ScriptNonce}}">
(function () {
  var endpoint = {{.PasskeyURL}};
  var btn = document.getElementById("pk-signin");
  var errBox = document.getElementById("pk-error");
  var divider = document.getElementById("pk-divider");
  if (!window.PublicKeyCredential) {
    errBox.textContent = "This browser doesn't support passkeys.";
    errBox.hidden = {{.PasswordEnabled}};
    return;
  }
  btn.hidden = false;
  if (divider) divider.hidden = false;
  function b64ToBuf(v) {
    v = v.replace(/-/g, "+").replace(/_/g, "/");
    while (v.length % 4) v += "=";
    var s = atob(v), out = new Uint8Array(s.length);
    for (var i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
    return out.buffer;
  }
  function bufToB64(buf) {
    if (!buf) return null;
    var bytes = new Uint8Array(buf), s = "";
    for (var i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
    return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }
  function requestOptions(pk) {
    if (PublicKeyCredential.parseRequestOptionsFromJSON) {
      return PublicKeyCredential.parseRequestOptionsFromJSON(pk);
    }
    var o = Object.assign({}, pk);
    o.challenge = b64ToBuf(pk.challenge);
    o.allowCredentials = (pk.allowCredentials || []).map(function (c) {
      return Object.assign({}, c, { id: b64ToBuf(c.id) });
    });
    return o;
  }
  function credentialJSON(cred) {
    if (cred.toJSON) return cred.toJSON();
    return {
      id: cred.id,
      rawId: bufToB64(cred.rawId),
      type: cred.type,
      authenticatorAttachment: cred.authenticatorAttachment,
      clientExtensionResults: cred.getClientExtensionResults(),
      response: {
        clientDataJSON: bufToB64(cred.response.clientDataJSON),
        authenticatorData: bufToB64(cred.response.authenticatorData),
        signature: bufToB64(cred.response.signature),
        userHandle: bufToB64(cred.response.userHandle)
      }
    };
  }
  btn.addEventListener("click", function () {
    btn.disabled = true;
    errBox.hidden = true;
    var sessionId;
    fetch(endpoint, { method: "POST" })
      .then(function (res) {
        if (!res.ok) throw new Error("failed to start");
        return res.json();
      })
      .then(function (data) {
        sessionId = data.sessionId;
        return navigator.credentials.get({ publicKey: requestOptions(data.options.publicKey) });
      })
      .then(function (cred) {
        document.getElementById("pk-session").value = sessionId;
        document.getElementById("pk-credential").value = JSON.stringify(credentialJSON(cred));
        document.getElementById("pk-form").submit();
      })
      .catch(function () {
        errBox.textContent = "Passkey sign-in was canceled or failed.";
        errBox.hidden = false;
        btn.disabled = false;
      });
  });
})();
</script>
{{end}}
{{end}}
</main>
</body>
</html>`))
