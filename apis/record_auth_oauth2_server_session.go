package apis

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

// Remembered sign-in (single sign-on) of the hosted OAuth2 server page.
//
// After a successful sign-in the page sets an HttpOnly cookie scoped to the
// collection "/oauth2-server" path. Following authorization requests from
// any client complete immediately (the popup just redirects back) until the
// cookie expires, the user signs out or the record tokenKey changes
// (eg. password change), because the cookie is signed with it.
//
// The cookie value is not a valid api auth token (it has its own token type).

const oauth2ServerSessionTokenType = "oauth2Session"

func oauth2ServerSessionCookieName(collection *core.Collection) string {
	return "pb_oauth2_session_" + collection.Id
}

func oauth2ServerSessionCookiePath(collection *core.Collection) string {
	return oauth2ServerCollectionAPIURL(collection) + "/oauth2-server"
}

func oauth2ServerSessionKey(record *core.Record) string {
	return record.TokenKey() + record.Collection().AuthToken.Secret
}

func oauth2ServerSecureCookie(e *core.RequestEvent) bool {
	return e.IsTLS() || strings.HasPrefix(strings.ToLower(e.App.Settings().Meta.AppURL), "https://")
}

// setOAuth2ServerSession remembers the signed in record (if enabled for the collection).
func setOAuth2ServerSession(e *core.RequestEvent, collection *core.Collection, record *core.Record) {
	duration := collection.OAuth2Server.SessionDurationTime()
	if duration <= 0 {
		return
	}

	token, err := security.NewJWT(jwt.MapClaims{
		core.TokenClaimType:         oauth2ServerSessionTokenType,
		core.TokenClaimId:           record.Id,
		core.TokenClaimCollectionId: collection.Id,
		"authTime":                  time.Now().Unix(),
	}, oauth2ServerSessionKey(record), duration)
	if err != nil {
		e.App.Logger().Warn("[oauth2Server] Failed to create the sign-in session", "error", err)
		return
	}

	http.SetCookie(e.Response, &http.Cookie{
		Name:     oauth2ServerSessionCookieName(collection),
		Value:    token,
		Path:     oauth2ServerSessionCookiePath(collection),
		MaxAge:   int(duration.Seconds()),
		HttpOnly: true,
		Secure:   oauth2ServerSecureCookie(e),
		SameSite: http.SameSiteLaxMode,
	})
}

// clearOAuth2ServerSession forgets the remembered sign-in.
func clearOAuth2ServerSession(e *core.RequestEvent, collection *core.Collection) {
	http.SetCookie(e.Response, &http.Cookie{
		Name:     oauth2ServerSessionCookieName(collection),
		Value:    "",
		Path:     oauth2ServerSessionCookiePath(collection),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   oauth2ServerSecureCookie(e),
		SameSite: http.SameSiteLaxMode,
	})
}

// readOAuth2ServerSession returns the remembered record (if any and still valid).
func readOAuth2ServerSession(e *core.RequestEvent, collection *core.Collection) *core.Record {
	if collection.OAuth2Server.SessionDuration <= 0 {
		return nil
	}

	cookie, err := e.Request.Cookie(oauth2ServerSessionCookieName(collection))
	if err != nil || cookie.Value == "" {
		return nil
	}

	claims, err := security.ParseUnverifiedJWT(cookie.Value)
	if err != nil {
		return nil
	}

	id, _ := claims[core.TokenClaimId].(string)
	collectionId, _ := claims[core.TokenClaimCollectionId].(string)
	tokenType, _ := claims[core.TokenClaimType].(string)
	if id == "" || collectionId != collection.Id || tokenType != oauth2ServerSessionTokenType {
		return nil
	}

	record, err := e.App.FindRecordById(collection, id)
	if err != nil {
		return nil
	}

	// verifies the signature and expiration
	if _, err := security.ParseJWT(cookie.Value, oauth2ServerSessionKey(record)); err != nil {
		return nil
	}

	return record
}

// checkOAuth2ServerSignIn applies the collection sign-in constraints and
// returns a user facing message when the record can't sign in.
func checkOAuth2ServerSignIn(e *core.RequestEvent, collection *core.Collection, record *core.Record) (string, error) {
	requestInfo, err := e.RequestInfo()
	if err != nil {
		return "", err
	}

	if ok, _ := e.App.CanAccessRecord(record, requestInfo, collection.AuthRule); !ok {
		return "This account is not allowed to sign in.", nil
	}

	if collection.MFA.Enabled {
		if needed, err := wantsMFA(e, record); needed || err != nil {
			return "This account requires multi-factor authentication, which this sign-in page doesn't support.", nil
		}
	}

	return "", nil
}

// issueOAuth2ServerCode creates an authorization code for the record and
// returns the client redirect url carrying it.
func issueOAuth2ServerCode(e *core.RequestEvent, collection *core.Collection, client core.OAuth2ServerClient, params oauth2ServerAuthorizeParams, record *core.Record) (string, error) {
	rawCode := security.RandomString(48)

	code := core.NewOAuth2Code(e.App)
	code.SetCollectionRef(collection.Id)
	code.SetRecordRef(record.Id)
	code.SetClientId(client.Id)
	code.SetCode(rawCode)
	code.SetRedirectURI(params.RedirectURI)
	code.SetScope(params.Scope)
	code.SetNonce(params.Nonce)
	code.SetCodeChallenge(params.CodeChallenge)
	code.SetCodeChallengeMethod(params.CodeChallengeMethod)
	if err := e.App.Save(code); err != nil {
		return "", err
	}

	return oauth2ServerRedirectURL(params.RedirectURI, map[string]string{
		"code":  rawCode,
		"state": params.State,
		"iss":   oauth2ServerIssuer(e, collection),
	}), nil
}

func hasOAuth2ServerPrompt(prompt string, values ...string) bool {
	for _, p := range strings.Fields(prompt) {
		for _, v := range values {
			if p == v {
				return true
			}
		}
	}
	return false
}

// oauth2ServerLogout forgets the remembered sign-in.
//
// GET|POST /api/collections/{collection}/oauth2-server/logout
//
//	?client_id=...&post_logout_redirect_uri=...&state=...
//
// The post logout redirect uri must be one of the client redirect uris,
// otherwise a "signed out" page is shown.
func oauth2ServerLogout(e *core.RequestEvent) error {
	collection, err := findOAuth2ServerCollection(e)
	if err != nil {
		return err
	}

	clearOAuth2ServerSession(e, collection)

	q := e.Request.URL.Query()
	redirectURI := q.Get("post_logout_redirect_uri")
	if redirectURI != "" {
		client, ok := collection.OAuth2Server.GetClient(q.Get("client_id"))
		if !ok || !client.HasRedirectURI(redirectURI) {
			return renderOAuth2ServerPage(e, http.StatusBadRequest, oauth2ServerPageData{
				AppName:    e.App.Settings().Meta.AppName,
				ErrorTitle: "You're signed out",
				Error:      "The return address isn't registered for this app, so you weren't sent back.",
			})
		}

		return e.Redirect(http.StatusFound, oauth2ServerRedirectURL(redirectURI, map[string]string{
			"state": q.Get("state"),
		}))
	}

	return renderOAuth2ServerPage(e, http.StatusOK, oauth2ServerPageData{
		AppName:    e.App.Settings().Meta.AppName,
		ErrorTitle: "You're signed out",
		Error:      "You can close this window.",
	})
}

var errOAuth2ServerLoginRequired = errors.New("login_required")
