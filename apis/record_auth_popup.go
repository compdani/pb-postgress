package apis

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

// Popup sign-in ("sso" provider) of an auth collection.
//
// It lets the collection own frontends sign users in on the hosted OAuth2
// server page, with no registered client and no extra JS, through the
// regular JS SDK call (and its popup + realtime flow):
//
//	await pb.collection("users").authWithOAuth2({ provider: "sso" })
//
//  1. "auth-methods" lists a virtual "sso" provider (when the popup sign-in is
//     enabled and the request comes from an allowed frontend origin) whose
//     auth url is the hosted sign-in page with a PKCE challenge;
//  2. the SDK opens it in a popup; after the sign-in the page redirects to the
//     regular "/api/oauth2-redirect", which pushes the code to the frontend
//     over its realtime (SSE) connection;
//  3. the SDK sends the code and the PKCE verifier to "auth-with-oauth2",
//     which signs the user in (no external provider is involved).
//
// The code can be redeemed only by the frontend origin it was issued for
// (the Origin header of the exchange must match the listed one).

const (
	// oauth2ServerPopupClientId is the reserved client_id of the popup sign-in
	// (it can't collide with a registered client because of the "@").
	oauth2ServerPopupClientId = "@popup"

	// oauth2ServerSSOProvider is the name of the virtual provider that the frontend
	// passes to authWithOAuth2 (providers are looked up by name, and the configured
	// ones must be registered ones, so it can't collide with them).
	oauth2ServerSSOProvider = "sso"
)

func (p *oauth2ServerAuthorizeParams) isPopup() bool {
	return p.ClientId == oauth2ServerPopupClientId
}

// oauth2ServerPopupClient returns the virtual client of the popup sign-in.
//
// The frontend host is used as name, so that the sign-in page shows the user who asks for the sign-in.
func oauth2ServerPopupClient(origin string) core.OAuth2ServerClient {
	name := origin
	if u, err := url.Parse(origin); err == nil && u.Host != "" {
		name = u.Host
	}

	return core.OAuth2ServerClient{Id: oauth2ServerPopupClientId, Name: name}
}

// normalizedOrigin returns the serialized origin of the url (lowercase, no default port)
// or an empty string.
func normalizedOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}

	host := strings.ToLower(u.Host)
	if port := u.Port(); (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		host = strings.TrimSuffix(host, ":"+port)
	}

	return u.Scheme + "://" + host
}

func isLoopbackHostname(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

// isOAuth2ServerPopupOrigin reports whether the frontend origin can use the popup sign-in.
//
// Other than the configured origins, the origin of the application URL is always
// allowed, and so is the origin of the local server when it runs on loopback.
func isOAuth2ServerPopupOrigin(e *core.RequestEvent, collection *core.Collection, origin string) bool {
	if origin == "" {
		return false
	}

	if slices.Contains(collection.OAuth2Server.Popup.Origins, origin) {
		return true
	}

	if appOrigin := normalizedOrigin(e.App.Settings().Meta.AppURL); appOrigin != "" && appOrigin == origin {
		return true
	}

	// the request host is trusted only on loopback (local development); otherwise
	// any host name that reaches this server would become an allowed popup origin
	if host := e.Request.Host; host != "" {
		hostname := host
		if h, _, err := net.SplitHostPort(host); err == nil {
			hostname = h
		}
		if isLoopbackHostname(hostname) {
			scheme := "http"
			if e.IsTLS() {
				scheme = "https"
			}

			return scheme+"://"+strings.ToLower(host) == origin
		}
	}

	return false
}

// oauth2ServerPopupRedirectURL is the only redirect url of the popup sign-in:
// the regular OAuth2 redirect handler of this server, which hands the code to
// the frontend over realtime.
func oauth2ServerPopupRedirectURL(e *core.RequestEvent) string {
	return oauth2ServerBaseURL(e) + "/api/oauth2-redirect"
}

// isOAuth2ServerPopupRedirectURL reports whether uri is this server OAuth2 redirect handler.
//
// The SDK builds it from the url the frontend uses to reach the server, so behind a TLS
// terminating proxy (where the request looks like plain http) it is https while
// oauth2ServerPopupRedirectURL may not know it. The host must still be this server.
func isOAuth2ServerPopupRedirectURL(e *core.RequestEvent, uri string) bool {
	if strings.EqualFold(uri, oauth2ServerPopupRedirectURL(e)) {
		return true
	}

	u, err := url.Parse(uri)
	if err != nil || u.Path != "/api/oauth2-redirect" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return false
	}

	if !strings.EqualFold(u.Host, e.Request.Host) {
		return false
	}

	return u.Scheme == "https" || (u.Scheme == "http" && isLoopbackHostname(u.Hostname()))
}

// oauth2ServerRequestOrigin returns the origin of the frontend that made the request.
//
// Browsers send the Origin header on cross-origin requests (and on posts); a plain
// same-origin read (the frontend is served by this server) is told by fetch metadata.
func oauth2ServerRequestOrigin(e *core.RequestEvent) string {
	if origin := e.Request.Header.Get("Origin"); origin != "" {
		return normalizedOrigin(origin)
	}

	if e.Request.Header.Get("Sec-Fetch-Site") == "same-origin" {
		return normalizedOrigin(oauth2ServerBaseURL(e))
	}

	return ""
}

// oauth2ServerSSOProviderInfo returns the virtual "sso" provider listed by auth-methods
// (or false when the popup sign-in isn't available for the requesting frontend).
func oauth2ServerSSOProviderInfo(e *core.RequestEvent, collection *core.Collection) (providerInfo, bool) {
	if !collection.OAuth2Server.Popup.Enabled || collection.Name == core.CollectionNameSuperusers {
		return providerInfo{}, false
	}

	// only the allowed frontends see (and can use) the provider
	origin := oauth2ServerRequestOrigin(e)
	if !isOAuth2ServerPopupOrigin(e, collection, origin) {
		return providerInfo{}, false
	}

	name := e.App.Settings().Meta.AppName
	if name == "" {
		name = "Single sign-on"
	}

	info := providerInfo{
		Name:                oauth2ServerSSOProvider,
		DisplayName:         name,
		State:               security.RandomString(30),
		CodeVerifier:        security.RandomString(43),
		CodeChallengeMethod: "S256",
	}
	info.CodeChallenge = security.S256Challenge(info.CodeVerifier)

	query := url.Values{}
	query.Set("response_type", "code")
	query.Set("client_id", oauth2ServerPopupClientId)
	query.Set("state", info.State)
	query.Set("popup_origin", origin)
	query.Set("code_challenge", info.CodeChallenge)
	query.Set("code_challenge_method", info.CodeChallengeMethod)

	// the SDK appends the (encoded) redirect url
	info.AuthURL = oauth2ServerBaseURL(e) + oauth2ServerCollectionAPIURL(collection) + "/oauth2-server/authorize?" +
		query.Encode() + "&redirect_uri="
	info.AuthUrl = info.AuthURL

	return info, true
}

// recordAuthWithSSO signs in the record of an "sso" authorization code
// (the SDK sends it to auth-with-oauth2 like for any other provider).
func recordAuthWithSSO(e *core.RequestEvent, collection *core.Collection, form *recordOAuth2LoginForm) error {
	collection, err := findPopupCollection(e)
	if err != nil {
		return err
	}

	invalidCode := func() error {
		return e.BadRequestError("Invalid, expired or already used sign-in code.", nil)
	}

	if form.Code == "" || !oauth2ServerPKCERegex.MatchString(form.CodeVerifier) {
		return invalidCode()
	}

	// validate everything before consuming, so that a wrong request can't burn
	// the code of someone else (or of a registered client)
	code, err := e.App.FindOAuth2CodeByCode(form.Code)
	if err != nil {
		return invalidCode()
	}

	// the code was issued for the frontend origin that listed the provider, and
	// browsers always send the Origin header with this post
	origin := normalizedOrigin(e.Request.Header.Get("Origin"))
	if code.CollectionRef() != collection.Id ||
		code.ClientId() != oauth2ServerPopupClientId ||
		code.CodeChallenge() == "" ||
		code.HasExpired(collection.OAuth2Server.CodeDurationTime()) ||
		origin == "" || origin != code.Nonce() || // the origin is kept in the (otherwise unused) nonce
		subtle.ConstantTimeCompare([]byte(code.RedirectURI()), []byte(form.RedirectURL)) != 1 {
		return invalidCode()
	}

	hash := sha256.Sum256([]byte(form.CodeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])
	if subtle.ConstantTimeCompare([]byte(challenge), []byte(code.CodeChallenge())) != 1 {
		return invalidCode()
	}

	// single use
	consumed, err := e.App.ConsumeOAuth2Code(code)
	if err != nil {
		return e.InternalServerError("Failed to consume the sign-in code.", err)
	}
	if !consumed {
		return invalidCode()
	}

	record, err := e.App.FindRecordById(collection, code.RecordRef())
	if err != nil {
		return invalidCode()
	}

	return RecordAuthResponse(e, record, core.MFAMethodOAuth2, map[string]any{"isNew": false})
}

func findPopupCollection(e *core.RequestEvent) (*core.Collection, error) {
	collection, err := findOAuth2ServerCollection(e)
	if err != nil {
		return nil, err
	}

	if !collection.OAuth2Server.Popup.Enabled {
		return nil, e.NotFoundError("The collection is not configured to allow popup sign-in.", nil)
	}

	return collection, nil
}
