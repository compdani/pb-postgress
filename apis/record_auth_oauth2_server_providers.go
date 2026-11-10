package apis

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/auth"
	"github.com/pocketbase/pocketbase/tools/security"
	"golang.org/x/oauth2"
)

// OAuth2 providers sign-in of the hosted OAuth2 server page.
//
// The page lists the enabled OAuth2 providers of the collection. Choosing
// one sends the browser to the provider, which returns to the regular
// "/api/oauth2-redirect" url (the same that is registered for the SDK
// flow), where the sign-in is completed and the page flow continues.
//
// The pending sign-in is kept in the single use sessions store and its id
// is the OAuth2 state (with a prefix to tell it apart from the realtime
// client ids of the SDK flow).

const (
	oauth2ServerProviderStatePrefix = "pbh_"
	oauth2ServerProviderSessionKind = "oauth2Provider"
	oauth2ServerProviderCookieName  = "pb_oauth2_provider_" // + the pending session id
	oauth2ServerProviderCookiePath  = "/api/oauth2-redirect"
)

var errOAuth2ServerSignInBlocked = errors.New("sign-in blocked")

type oauth2ServerPageProvider struct {
	Name        string
	DisplayName string
}

// oauth2ServerProviderSession is the pending provider sign-in of the hosted page.
type oauth2ServerProviderSession struct {
	Params       oauth2ServerAuthorizeParams `json:"params"`
	Provider     string                      `json:"provider"`
	CodeVerifier string                      `json:"codeVerifier"`
	RedirectURL  string                      `json:"redirectURL"`

	// Nonce binds the sign-in to the browser that started it (see the cookie).
	Nonce string `json:"nonce"`
}

// oauth2ServerBaseURL returns the public base url of the current request.
func oauth2ServerBaseURL(e *core.RequestEvent) string {
	// the application url knows the public scheme when running behind a proxy
	if u, err := url.Parse(e.App.Settings().Meta.AppURL); err == nil && u.Host != "" && strings.EqualFold(u.Host, e.Request.Host) {
		return u.Scheme + "://" + u.Host
	}

	scheme := "http"
	if e.IsTLS() {
		scheme = "https"
	}

	return scheme + "://" + e.Request.Host
}

// oauth2ServerPageProviders returns the OAuth2 providers that the hosted page can sign in with.
func oauth2ServerPageProviders(e *core.RequestEvent, collection *core.Collection) []oauth2ServerPageProvider {
	if !collection.OAuth2.Enabled {
		return nil
	}

	result := make([]oauth2ServerPageProvider, 0, len(collection.OAuth2.Providers))

	for _, config := range collection.OAuth2.Providers {
		provider, err := config.InitProvider()
		if err != nil {
			e.App.Logger().Debug(
				"Failed to setup OAuth2 provider",
				slog.String("name", config.Name),
				slog.String("error", err.Error()),
			)
			continue
		}

		if isOAuth2ServerOwnProvider(e, collection, provider) {
			continue
		}

		name := provider.DisplayName()
		if name == "" {
			name = config.Name
		}

		result = append(result, oauth2ServerPageProvider{Name: config.Name, DisplayName: name})
	}

	return result
}

// isOAuth2ServerOwnProvider reports whether the provider is this same sign-in page
// (eg. the "oidc" provider that other app instances sharing the database sign in with),
// because it would only lead back to the page.
func isOAuth2ServerOwnProvider(e *core.RequestEvent, collection *core.Collection, provider auth.Provider) bool {
	u, err := url.Parse(provider.AuthURL())
	if err != nil || u.Path != oauth2ServerCollectionAPIURL(collection)+"/oauth2-server/authorize" {
		return false
	}

	if strings.EqualFold(u.Host, e.Request.Host) {
		return true
	}

	appURL, err := url.Parse(e.App.Settings().Meta.AppURL)

	return err == nil && appURL.Host != "" && strings.EqualFold(u.Host, appURL.Host)
}

// oauth2ServerProviderCookie returns the browser binding cookie of a pending sign-in
// (one per pending sign-in, so that concurrent sign-ins don't invalidate each other).
func oauth2ServerProviderCookie(e *core.RequestEvent, sessionId string, value string, maxAge int) *http.Cookie {
	cookie := &http.Cookie{
		Name:     oauth2ServerProviderCookieName + sessionId,
		Value:    value,
		Path:     oauth2ServerProviderCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   oauth2ServerSecureCookie(e),
		SameSite: http.SameSiteLaxMode,
	}

	// some providers return with a cross-site POST (eg. Apple), which doesn't send Lax cookies
	if cookie.Secure {
		cookie.SameSite = http.SameSiteNoneMode
	}

	return cookie
}

// oauth2ServerProviderStart sends the browser to the chosen OAuth2 provider.
//
// The authorize params must be already validated.
func oauth2ServerProviderStart(
	e *core.RequestEvent,
	collection *core.Collection,
	client core.OAuth2ServerClient,
	params oauth2ServerAuthorizeParams,
	providerName string,
) error {
	unavailable := func() error {
		return renderOAuth2ServerLoginPage(e, http.StatusBadRequest, collection, client, params, "", "This sign-in option is not available.")
	}

	config, ok := collection.OAuth2.GetProviderConfig(providerName)
	if !collection.OAuth2.Enabled || !ok {
		return unavailable()
	}

	provider, err := config.InitProvider()
	if err != nil || isOAuth2ServerOwnProvider(e, collection, provider) {
		return unavailable()
	}

	pending := oauth2ServerProviderSession{
		Params:      params,
		Provider:    config.Name,
		RedirectURL: oauth2ServerBaseURL(e) + "/api/oauth2-redirect",
		Nonce:       security.RandomString(40),
	}

	provider.SetRedirectURL(pending.RedirectURL)

	urlOpts := []oauth2.AuthCodeOption{}

	// custom providers url options (see the auth-methods endpoint)
	if config.Name == auth.NameApple {
		urlOpts = append(urlOpts, oauth2.SetAuthURLParam("response_mode", "form_post"))
	}

	if provider.PKCE() {
		pending.CodeVerifier = security.RandomString(43)
		urlOpts = append(urlOpts,
			oauth2.SetAuthURLParam("code_challenge", security.S256Challenge(pending.CodeVerifier)),
			oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		)
	}

	raw, err := json.Marshal(pending)
	if err != nil {
		return e.InternalServerError("Failed to start the sign-in.", err)
	}

	session := core.NewPasskeySession(e.App)
	session.SetCollectionRef(collection.Id)
	session.SetKind(oauth2ServerProviderSessionKind)
	session.SetData(raw)
	if err := e.App.Save(session); err != nil {
		return e.InternalServerError("Failed to start the sign-in.", err)
	}

	e.SetCookie(oauth2ServerProviderCookie(e, session.Id, pending.Nonce, int(core.PasskeySessionDuration.Seconds())))

	return e.Redirect(http.StatusSeeOther, provider.BuildAuthURL(oauth2ServerProviderStatePrefix+session.Id, urlOpts...))
}

// oauth2ServerProviderCallback completes the provider sign-in started from the hosted page.
//
// It is called by the "/api/oauth2-redirect" handler.
func oauth2ServerProviderCallback(e *core.RequestEvent, sessionId string, data oauth2RedirectData) error {
	invalid := func(reason string) error {
		e.App.Logger().Debug("Invalid hosted page OAuth2 redirect", "reason", reason, "sessionId", sessionId)

		return renderOAuth2ServerPage(e, http.StatusBadRequest, oauth2ServerPageData{
			AppName:    e.App.Settings().Meta.AppName,
			ErrorTitle: "The sign-in has expired",
			Error:      "Close this window and try again.",
		})
	}

	// load and consume the pending sign-in (single use)
	// ---
	session, err := e.App.FindPasskeySessionById(sessionId)
	if err != nil {
		return invalid("missing session")
	}

	if session.HasExpired() || session.Kind() != oauth2ServerProviderSessionKind {
		return invalid("expired or invalid session")
	}

	pending := oauth2ServerProviderSession{}
	if err := json.Unmarshal(session.Data(), &pending); err != nil {
		return invalid("invalid session data")
	}

	// the redirect must come from the browser that started the sign-in
	// (checked before consuming, so that another browser can't burn the pending sign-in)
	cookie, err := e.Request.Cookie(oauth2ServerProviderCookieName + session.Id)
	if err != nil || pending.Nonce == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(pending.Nonce)) != 1 {
		return invalid("missing or invalid browser cookie")
	}
	e.SetCookie(oauth2ServerProviderCookie(e, session.Id, "", -1))

	// single use
	consumed, err := e.App.ConsumePasskeySession(session)
	if err != nil {
		return e.InternalServerError("Failed to complete the sign-in.", err)
	}
	if !consumed {
		return invalid("already used session")
	}

	collection, err := e.App.FindCachedCollectionByNameOrId(session.CollectionRef())
	if err != nil || !collection.IsAuth() || !collection.OAuth2Server.Enabled || !collection.OAuth2.Enabled {
		return invalid("missing or not configured collection")
	}

	client, authErr := pending.Params.validate(e, collection)
	if authErr != nil {
		return oauth2ServerAuthorizeFailure(e, collection, pending.Params, authErr)
	}

	providerLabel := pending.Provider
	if config, ok := collection.OAuth2.GetProviderConfig(pending.Provider); ok {
		if provider, err := config.InitProvider(); err == nil && provider.DisplayName() != "" {
			providerLabel = provider.DisplayName()
		}
	}

	loginFailed := func(message string) error {
		return renderOAuth2ServerLoginPage(e, http.StatusBadRequest, collection, client, pending.Params, "", message)
	}

	if data.Error != "" || data.Code == "" {
		return loginFailed("The sign-in with " + providerLabel + " was canceled or failed.")
	}

	// temporary store the Apple user's name so that it can be assigned to a new record
	if data.AppleUser != "" {
		if err := parseAndStoreAppleRedirectName(e.App, oauth2RedirectAppleNameStoreKeyPrefix+data.Code, data.AppleUser); err != nil {
			e.App.Logger().Debug("Failed to parse and load Apple Redirect name data", "error", err)
		}
	}

	// exchange the code and sign in (or sign up) the related record
	// ---
	e.Set(core.RequestEventKeyInfoContext, core.RequestInfoContextOAuth2)

	form := &recordOAuth2LoginForm{
		collection:   collection,
		Provider:     pending.Provider,
		Code:         data.Code,
		CodeVerifier: pending.CodeVerifier,
		RedirectURL:  pending.RedirectURL,
	}

	if err := form.validate(); err != nil {
		return loginFailed("This sign-in option is not available.")
	}

	var record *core.Record
	var blocked string

	err = oauth2Authenticate(e, collection, form,
		func(e *core.RecordAuthWithOAuth2RequestEvent) error {
			// the sign-in constraints (access rule, MFA) must hold before the record
			// is linked, verified or its password rotated by the OAuth2 sign-in
			if e.Record == nil {
				return nil
			}

			message, err := checkOAuth2ServerSignIn(e.RequestEvent, collection, e.Record)
			if err != nil {
				return err
			}
			if message != "" {
				blocked = message
				return errOAuth2ServerSignInBlocked
			}

			return nil
		},
		func(e *core.RecordAuthWithOAuth2RequestEvent) error {
			record = e.Record
			return nil
		},
	)
	if blocked != "" {
		return loginFailed(blocked)
	}
	if err != nil || record == nil {
		e.App.Logger().Debug("Failed hosted page OAuth2 sign-in", "error", err, "provider", pending.Provider)

		return loginFailed("The sign-in with " + providerLabel + " failed.")
	}

	return oauth2ServerCompleteSignIn(e, collection, client, pending.Params, record, core.MFAMethodOAuth2, "")
}
