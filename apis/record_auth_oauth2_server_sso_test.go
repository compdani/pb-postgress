package apis_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

// TestOAuth2ServerAsProviderForSameDatabaseApp simulates an app instance
// that uses the central sign-in collection as its "oidc" provider (the
// regular authWithOAuth2 popup + realtime flow of the JS SDK), so that
// passkeys live on a single domain.
func TestOAuth2ServerAsProviderForSameDatabaseApp(t *testing.T) {
	t.Parallel()

	s := newPasskeyTestServer(t)

	// real http server for the server-to-server token and userinfo calls
	srv := httptest.NewServer(s.handler)
	defer srv.Close()

	appRedirectURL := srv.URL + "/api/oauth2-redirect"
	issuer := srv.URL + "/api/collections/users/oauth2-server"

	users, err := s.app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	users.OAuth2Server.Clients[0].RedirectURIs = append(users.OAuth2Server.Clients[0].RedirectURIs, appRedirectURL)
	users.OAuth2.Enabled = true
	users.OAuth2.Providers = []core.OAuth2ProviderConfig{{
		Name:         "oidc",
		DisplayName:  "Central sign-in",
		ClientId:     oauth2TestClientId,
		ClientSecret: oauth2TestClientSecret,
		AuthURL:      issuer + "/authorize",
		TokenURL:     issuer + "/token",
		UserInfoURL:  issuer + "/userinfo",
	}}
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}

	// unverified user -> the provider doesn't return the email, so email matching can't be used
	user, token := s.userToken("test@example.com")
	user.SetVerified(false)
	if err := s.app.Save(user); err != nil {
		t.Fatal(err)
	}
	authenticator := s.registerPasskey(token)

	totalUsers, _ := s.app.CountRecords("users")

	// 1. the SDK reads the provider auth url (state, PKCE) from auth-methods
	status, methods := s.jsonRequest(http.MethodGet, "/api/collections/users/auth-methods", nil, "")
	if status != 200 {
		t.Fatalf("auth-methods: %d", status)
	}
	providers := methods["oauth2"].(map[string]any)["providers"].([]any)
	provider := providers[0].(map[string]any)

	authURL, err := url.Parse(provider["authURL"].(string) + url.QueryEscape(appRedirectURL))
	if err != nil {
		t.Fatal(err)
	}

	// 2. the popup opens the hosted sign-in page
	res := s.do(http.MethodGet, authURL.Path+"?"+authURL.RawQuery, nil, nil)
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected the hosted page, got %d: %s", res.StatusCode, body)
	}

	// 3. the user signs in with the passkey (no email)
	_, options := s.jsonRequest(http.MethodPost, "/api/collections/users/request-passkey", nil, "")
	credential, _ := json.Marshal(authenticator.get(options["options"].(map[string]any)))

	form := authURL.Query()
	form.Set("passkey_session", options["sessionId"].(string))
	form.Set("passkey_credential", string(credential))
	res, code := s.login2(form)
	if code == "" {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected a code redirect, got %d: %s", res.StatusCode, body)
	}

	loc, _ := url.Parse(res.Header.Get("Location"))
	if loc.Scheme+"://"+loc.Host+loc.Path != appRedirectURL || loc.Query().Get("state") != provider["state"] {
		t.Fatalf("expected the redirect to the app oauth2-redirect handler with the SDK state, got %s", loc)
	}

	// 4. the SDK receives the code over realtime and calls auth-with-oauth2 on the app instance
	status, data := s.jsonRequest(http.MethodPost, "/api/collections/users/auth-with-oauth2", map[string]any{
		"provider":     "oidc",
		"code":         code,
		"codeVerifier": provider["codeVerifier"],
		"redirectURL":  appRedirectURL,
	}, "")
	if status != 200 {
		t.Fatalf("auth-with-oauth2: %d %v", status, data)
	}

	record := data["record"].(map[string]any)
	if record["id"] != user.Id {
		t.Fatalf("expected the same record %q, got %v", user.Id, record["id"])
	}
	if meta, _ := data["meta"].(map[string]any); meta["isNew"] != false {
		t.Fatalf("expected an existing record, got meta %v", meta)
	}

	if after, _ := s.app.CountRecords("users"); after != totalUsers {
		t.Fatalf("expected no new users, had %d now %d", totalUsers, after)
	}

	// the returned token works for the record (shared database)
	if found, err := s.app.FindAuthRecordByToken(data["token"].(string), core.TokenTypeAuth); err != nil || found.Id != user.Id {
		t.Fatalf("invalid returned token: %v", err)
	}
}
