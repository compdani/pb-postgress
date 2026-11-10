package apis

import (
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// Popup sign-in of an auth collection.
//
// It lets the collection own frontends sign users in on the hosted
// OAuth2 server page without registering a client (and without handling
// any password):
//
//  1. the frontend opens "/oauth2-server/authorize" in a popup with
//     client_id=@popup, its own origin as redirect_uri and a PKCE challenge;
//  2. after the sign-in the popup lands on "/oauth2-server/popup-callback",
//     which hands the single use code to the opener with window.postMessage
//     (only to the allowed origin);
//  3. the frontend exchanges the code and the PKCE verifier for the regular
//     auth response at "/auth-with-popup".
//
// "/oauth2-server/popup.js" serves a small helper for the JS SDK that does all of the above.

const (
	// oauth2ServerPopupClientId is the reserved client_id of the popup sign-in
	// (it can't collide with a registered client because of the "@").
	oauth2ServerPopupClientId = "@popup"

	oauth2ServerPopupMessageType = "pocketbase:popup"

	authMethodPopup = "popup"
)

//go:embed record_auth_popup.js
var recordAuthPopupScript []byte

func bindRecordAuthPopupApi(sub *router.RouterGroup[*core.RequestEvent]) {
	sub.GET("/oauth2-server/popup.js", oauth2ServerPopupScript)
	sub.GET("/oauth2-server/popup-callback", oauth2ServerPopupCallback).Bind(
		SkipSuccessActivityLog(), // the url contains the authorization code
	)
	sub.POST("/auth-with-popup", recordAuthWithPopup).Bind(
		collectionPathRateLimit("", "authWithPopup", "auth"),
	)
}

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

// oauth2ServerReturnURL returns the url that sends the authorization
// response (code or error) back to the client.
//
// The params must be already validated.
func oauth2ServerReturnURL(collection *core.Collection, params oauth2ServerAuthorizeParams, values map[string]string) string {
	if !params.isPopup() {
		return oauth2ServerRedirectURL(params.RedirectURI, values)
	}

	values = maps.Clone(values)
	values["origin"] = params.RedirectURI

	return oauth2ServerRedirectURL(oauth2ServerCollectionAPIURL(collection)+"/oauth2-server/popup-callback", values)
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

// GET /api/collections/{collection}/oauth2-server/popup.js
func oauth2ServerPopupScript(e *core.RequestEvent) error {
	e.Response.Header().Set("Cache-Control", "max-age=3600")

	return e.Blob(http.StatusOK, "text/javascript; charset=utf-8", recordAuthPopupScript)
}

// oauth2ServerPopupCallback hands the authorization response to the window that opened the popup.
//
// GET /api/collections/{collection}/oauth2-server/popup-callback
//
//	?origin=...&state=...&code=... (or &error=...&error_description=...)
func oauth2ServerPopupCallback(e *core.RequestEvent) error {
	collection, err := findPopupCollection(e)
	if err != nil {
		return err
	}

	q := e.Request.URL.Query()

	origin := q.Get("origin")
	if !isOAuth2ServerPopupOrigin(e, collection, origin) {
		return renderOAuth2ServerPage(e, http.StatusBadRequest, oauth2ServerPageData{
			AppName:    e.App.Settings().Meta.AppName,
			ErrorTitle: "Invalid sign-in request",
			Error:      "This site is not allowed to use the popup sign-in.",
		})
	}

	return renderOAuth2ServerPage(e, http.StatusOK, oauth2ServerPageData{
		Mode:        "popupDone",
		AppName:     e.App.Settings().Meta.AppName,
		PopupOrigin: origin,
		PopupMessage: map[string]string{
			"type":             oauth2ServerPopupMessageType,
			"state":            q.Get("state"),
			"code":             q.Get("code"),
			"error":            q.Get("error"),
			"errorDescription": q.Get("error_description"),
		},
	})
}

type recordAuthWithPopupForm struct {
	Code         string `form:"code" json:"code"`
	CodeVerifier string `form:"codeVerifier" json:"codeVerifier"`
}

// recordAuthWithPopup exchanges the code of the popup sign-in for the regular auth response.
//
// POST /api/collections/{collection}/auth-with-popup
func recordAuthWithPopup(e *core.RequestEvent) error {
	collection, err := findPopupCollection(e)
	if err != nil {
		return err
	}

	form := &recordAuthWithPopupForm{}
	if err := e.BindBody(form); err != nil {
		return e.BadRequestError("An error occurred while loading the submitted data.", err)
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

	// browsers always send the Origin header with this post, so the code can be
	// used only by the frontend it was issued for
	if code.CollectionRef() != collection.Id ||
		code.ClientId() != oauth2ServerPopupClientId ||
		code.CodeChallenge() == "" ||
		code.HasExpired(collection.OAuth2Server.CodeDurationTime()) ||
		e.Request.Header.Get("Origin") != code.RedirectURI() {
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

	return RecordAuthResponse(e, record, authMethodPopup, nil)
}
