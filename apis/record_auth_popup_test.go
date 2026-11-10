package apis_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
)

const (
	popupTestOrigin      = "https://app.example.com"
	popupTestRedirectURL = "http://example.com/api/oauth2-redirect" // the test requests are served on example.com
)

func newPopupTestServer(t *testing.T) *oauth2TestServer {
	s := newOAuth2TestServer(t)

	users, err := s.app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	users.OAuth2Server.Popup = core.OAuth2ServerPopupConfig{
		Enabled: true,
		Origins: []string{popupTestOrigin},
	}
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}

	return s
}

// popupTestParams are the authorize params that the SDK sends to the auth url of the "sso" provider.
func popupTestParams(extra map[string]string) url.Values {
	v := url.Values{
		"response_type":         {"code"},
		"client_id":             {"@popup"},
		"redirect_uri":          {popupTestRedirectURL},
		"popup_origin":          {popupTestOrigin},
		"state":                 {"xyz"},
		"code_challenge":        {oauth2TestChallenge(oauth2TestVerifier)},
		"code_challenge_method": {"S256"},
	}
	for k, val := range extra {
		if val == "" {
			v.Del(k)
		} else {
			v.Set(k, val)
		}
	}
	return v
}

// popupExchange is the auth-with-oauth2 call of the SDK for the "sso" provider.
func (s *oauth2TestServer) popupExchange(code, verifier, origin string) (int, map[string]any) {
	return s.popupExchangeWith(code, verifier, origin, popupTestRedirectURL)
}

func (s *oauth2TestServer) popupExchangeWith(code, verifier, origin, redirectURL string) (int, map[string]any) {
	body, _ := json.Marshal(map[string]any{
		"provider":     "sso",
		"code":         code,
		"codeVerifier": verifier,
		"redirectURL":  redirectURL,
	})

	req := httptestRequest(http.MethodPost, "/api/collections/users/auth-with-oauth2", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}

	res := serve(s.handler, req)
	defer res.Body.Close()

	data := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&data)

	return res.StatusCode, data
}

// ssoProvider returns the "sso" provider that auth-methods lists for the frontend origin (if any).
func (s *oauth2TestServer) ssoProvider(origin string, header map[string]string) map[string]any {
	req := httptestRequest(http.MethodGet, "/api/collections/users/auth-methods", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}

	res := serve(s.handler, req)
	defer res.Body.Close()

	data := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&data)

	oauth2, _ := data["oauth2"].(map[string]any)
	providers, _ := oauth2["providers"].([]any)
	for _, p := range providers {
		if provider, _ := p.(map[string]any); provider["name"] == "sso" {
			return provider
		}
	}

	return nil
}

func TestPopupSignInListedForAllowedOrigins(t *testing.T) {
	t.Parallel()

	s := newPopupTestServer(t)

	provider := s.ssoProvider(popupTestOrigin, nil)
	if provider == nil {
		t.Fatal("expected the sso provider for the allowed origin")
	}

	authURL, _ := provider["authURL"].(string)
	u, err := url.Parse(authURL + url.QueryEscape(popupTestRedirectURL))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "example.com" || u.Path != "/api/collections/users/oauth2-server/authorize" ||
		q.Get("client_id") != "@popup" || q.Get("popup_origin") != popupTestOrigin ||
		q.Get("redirect_uri") != popupTestRedirectURL || q.Get("response_type") != "code" ||
		q.Get("state") == "" || q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" ||
		provider["codeVerifier"] == "" || provider["codeChallenge"] != q.Get("code_challenge") {
		t.Fatalf("unexpected auth url %s (%v)", u, provider)
	}

	// not listed for other frontends, or when it can't be told who asks
	if s.ssoProvider("https://evil.example.com", nil) != nil {
		t.Fatal("expected no sso provider for an unknown origin")
	}
	if s.ssoProvider("", nil) != nil {
		t.Fatal("expected no sso provider without an origin")
	}
	if s.ssoProvider("", map[string]string{"Sec-Fetch-Site": "cross-site"}) != nil {
		t.Fatal("expected no sso provider for a cross-site request without an origin")
	}

	// the frontend served by the server itself (same-origin reads carry no Origin header)
	users, _ := s.app.FindCollectionByNameOrId("users")
	s.app.Settings().Meta.AppURL = "http://example.com"
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}
	if s.ssoProvider("", map[string]string{"Sec-Fetch-Site": "same-origin"}) == nil {
		t.Fatal("expected the sso provider for a same-origin frontend on the application url")
	}

	// disabled -> gone, even for an allowed origin
	users.OAuth2Server.Popup.Enabled = false
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}
	if s.ssoProvider(popupTestOrigin, nil) != nil {
		t.Fatal("expected no sso provider when the popup sign-in is disabled")
	}
}

func TestPopupSignInFullFlow(t *testing.T) {
	t.Parallel()

	s := newPopupTestServer(t)

	user, err := s.app.FindAuthRecordByEmail("users", "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// the frontend's realtime connection, subscribed like the SDK authWithOAuth2 does
	client := subscriptions.NewDefaultClient()
	s.app.SubscriptionsBroker().Register(client)
	client.Subscribe("@oauth2")

	// the realtime handler always has a goroutine reading the (unbuffered) client channel
	messages := make(chan subscriptions.Message, 4)
	go func() {
		for msg := range client.Channel() {
			messages <- msg
		}
	}()

	// the SDK reads the provider, replaces the state with the realtime client id and opens the popup
	provider := s.ssoProvider(popupTestOrigin, nil)
	authURL, _ := url.Parse(provider["authURL"].(string) + url.QueryEscape(popupTestRedirectURL))
	q := authURL.Query()
	q.Set("state", client.Id())
	authURL.RawQuery = q.Encode()

	// the sign-in page shows who asks for the sign-in
	res := s.do(http.MethodGet, authURL.Path+"?"+authURL.RawQuery, nil, nil)
	page, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(page), "<strong>app.example.com</strong>") || !strings.Contains(string(page), `name="password"`) {
		t.Fatalf("unexpected login page (%d): %s", res.StatusCode, page)
	}

	// the sign-in returns to the regular OAuth2 redirect handler of the server
	res, code := s.login(authURL.Query(), "test@example.com", "1234567890")
	if code == "" {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected code redirect, got %d: %s", res.StatusCode, body)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	if loc.Scheme+"://"+loc.Host+loc.Path != popupTestRedirectURL || loc.Query().Get("state") != client.Id() {
		t.Fatalf("unexpected redirect %s", loc)
	}

	// which pushes the code to the frontend over realtime
	if res := s.do(http.MethodGet, loc.RequestURI(), nil, nil); res.StatusCode >= 400 {
		t.Fatalf("redirect handler: %d", res.StatusCode)
	}
	select {
	case msg := <-messages:
		data := map[string]string{}
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			t.Fatal(err)
		}
		if msg.Name != "@oauth2" || data["state"] != client.Id() || data["code"] != code {
			t.Fatalf("unexpected realtime message %s %v", msg.Name, data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected the code over realtime")
	}

	// the code can't be used at the OAuth2 token endpoint
	if status, data := s.exchange(oauth2TestTokenForm(code, map[string]string{"client_id": "@popup", "client_secret": ""}), nil); status != 401 {
		t.Fatalf("expected the token endpoint to reject the popup client, got %d %v", status, data)
	}

	// the SDK exchanges it at auth-with-oauth2
	status, data := s.popupExchange(code, provider["codeVerifier"].(string), popupTestOrigin)
	if status != 200 {
		t.Fatalf("auth-with-oauth2 (sso): %d %v", status, data)
	}
	record, _ := data["record"].(map[string]any)
	token, _ := data["token"].(string)
	meta, _ := data["meta"].(map[string]any)
	if record["id"] != user.Id || meta["isNew"] != false {
		t.Fatalf("expected record %q, got %v (meta %v)", user.Id, record["id"], meta)
	}
	if found, err := s.app.FindAuthRecordByToken(token, core.TokenTypeAuth); err != nil || found.Id != user.Id {
		t.Fatalf("invalid returned token: %v", err)
	}

	// single use
	if status, _ := s.popupExchange(code, provider["codeVerifier"].(string), popupTestOrigin); status != 400 {
		t.Fatalf("expected the used code to be rejected, got %d", status)
	}
}

func TestPopupSignInAuthorizeValidation(t *testing.T) {
	t.Parallel()

	s := newPopupTestServer(t)
	s.app.Settings().Meta.AppURL = "https://pb.example.com/"

	scenarios := []struct {
		name     string
		extra    map[string]string
		status   int
		location string
	}{
		{"configured origin", nil, 200, ""},
		{"application url origin", map[string]string{"popup_origin": "https://pb.example.com"}, 200, ""},
		{"unpinned request host is not an origin", map[string]string{"popup_origin": "http://example.com"}, 400, ""},
		{"unknown origin", map[string]string{"popup_origin": "https://evil.example.com"}, 400, ""},
		{"origin with path", map[string]string{"popup_origin": popupTestOrigin + "/cb"}, 400, ""},
		{"missing origin", map[string]string{"popup_origin": ""}, 400, ""},
		{"another redirect url", map[string]string{"redirect_uri": popupTestOrigin}, 400, ""},
		{"redirect to another host", map[string]string{"redirect_uri": "http://evil.example.com/api/oauth2-redirect"}, 400, ""},
		{"missing PKCE", map[string]string{"code_challenge": "", "code_challenge_method": ""}, 303, "error=invalid_request"},
		{"silent sign-in is not supported", map[string]string{"prompt": "none"}, 303, "error=invalid_request"},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			res := s.do(http.MethodGet, "/api/collections/users/oauth2-server/authorize?"+popupTestParams(sc.extra).Encode(), nil, nil)
			if res.StatusCode != sc.status {
				body, _ := io.ReadAll(res.Body)
				t.Fatalf("expected %d, got %d: %s", sc.status, res.StatusCode, body)
			}

			location := res.Header.Get("Location")
			if sc.location != "" && (!strings.HasPrefix(location, popupTestRedirectURL+"?") || !strings.Contains(location, sc.location)) {
				t.Fatalf("expected a redirect to the OAuth2 redirect handler with %q, got %q", sc.location, location)
			}
		})
	}

	// the local server on loopback is allowed (development)
	loopback := "http://127.0.0.1:8090"
	target := loopback + "/api/collections/users/oauth2-server/authorize?" + popupTestParams(map[string]string{
		"popup_origin": loopback,
		"redirect_uri": loopback + "/api/oauth2-redirect",
	}).Encode()
	if res := s.do(http.MethodGet, target, nil, nil); res.StatusCode != 200 {
		t.Fatalf("expected the loopback origin to be allowed, got %d", res.StatusCode)
	}

	// any other host name that reaches the server is not
	attacker := "http://login.attacker.com:8090"
	target = attacker + "/api/collections/users/oauth2-server/authorize?" + popupTestParams(map[string]string{
		"popup_origin": attacker,
		"redirect_uri": attacker + "/api/oauth2-redirect",
	}).Encode()
	if res := s.do(http.MethodGet, target, nil, nil); res.StatusCode != 400 {
		t.Fatalf("expected an unpinned host to be rejected, got %d", res.StatusCode)
	}

	// disabled
	users, _ := s.app.FindCollectionByNameOrId("users")
	users.OAuth2Server.Popup.Enabled = false
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}
	res := s.do(http.MethodGet, "/api/collections/users/oauth2-server/authorize?"+popupTestParams(nil).Encode(), nil, nil)
	if res.StatusCode != 400 {
		t.Fatalf("expected the disabled popup sign-in to be rejected, got %d", res.StatusCode)
	}
	if status, _ := s.popupExchange("abc", oauth2TestVerifier, popupTestOrigin); status != 404 {
		t.Fatalf("expected 404 for the disabled popup sign-in, got %d", status)
	}
}

func TestPopupSignInExchangeValidation(t *testing.T) {
	t.Parallel()

	s := newPopupTestServer(t)

	newCode := func(params url.Values) string {
		res, code := s.login(params, "test@example.com", "1234567890")
		if code == "" {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("expected code redirect, got %d: %s", res.StatusCode, body)
		}
		return code
	}

	scenarios := []struct {
		name        string
		params      url.Values
		verifier    string
		origin      string
		redirectURL string
		status      int
	}{
		{"wrong verifier", popupTestParams(nil), strings.Repeat("a", 43), popupTestOrigin, popupTestRedirectURL, 400},
		{"missing verifier", popupTestParams(nil), "", popupTestOrigin, popupTestRedirectURL, 400},
		{"another origin", popupTestParams(nil), oauth2TestVerifier, "https://evil.example.com", popupTestRedirectURL, 400},
		{"no origin header", popupTestParams(nil), oauth2TestVerifier, "", popupTestRedirectURL, 400},
		{"another redirect url", popupTestParams(nil), oauth2TestVerifier, popupTestOrigin, "http://evil.example.com/api/oauth2-redirect", 400},
		{"code of a registered client", s.authorizeParams(nil), oauth2TestVerifier, popupTestOrigin, oauth2TestRedirectURI, 400},
		{"matching origin", popupTestParams(nil), oauth2TestVerifier, popupTestOrigin, popupTestRedirectURL, 200},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			status, data := s.popupExchangeWith(newCode(sc.params), sc.verifier, sc.origin, sc.redirectURL)
			if status != sc.status {
				t.Fatalf("expected %d, got %d %v", sc.status, status, data)
			}
		})
	}
}

func TestPopupSignInFailedExchangeKeepsRegisteredClientCode(t *testing.T) {
	t.Parallel()

	s := newPopupTestServer(t)

	res, code := s.login(s.authorizeParams(nil), "test@example.com", "1234567890")
	if code == "" {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected code redirect, got %d: %s", res.StatusCode, body)
	}

	// the sso exchange rejects it without burning it
	if status, _ := s.popupExchangeWith(code, oauth2TestVerifier, popupTestOrigin, oauth2TestRedirectURI); status != 400 {
		t.Fatalf("expected 400, got %d", status)
	}

	if status, data := s.exchange(oauth2TestTokenForm(code, nil), nil); status != 200 {
		t.Fatalf("expected the registered client code to still work, got %d %v", status, data)
	}
}

func TestOAuth2ServerTokenKeepsCodeOfAnotherClient(t *testing.T) {
	t.Parallel()

	s := newPopupTestServer(t)

	res, code := s.login(popupTestParams(nil), "test@example.com", "1234567890")
	if code == "" {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected code redirect, got %d: %s", res.StatusCode, body)
	}

	// a registered client presenting the popup code gets invalid_grant, without burning it
	if status, _ := s.exchange(oauth2TestTokenForm(code, nil), nil); status != 400 {
		t.Fatalf("expected 400, got %d", status)
	}

	if status, data := s.popupExchange(code, oauth2TestVerifier, popupTestOrigin); status != 200 {
		t.Fatalf("expected the popup code to still work, got %d %v", status, data)
	}
}

func TestPopupSignInConfigValidation(t *testing.T) {
	t.Parallel()

	s := newPopupTestServer(t)

	users, _ := s.app.FindCollectionByNameOrId("users")

	// no registered client is needed
	users.OAuth2Server.Clients = nil
	if err := s.app.Save(users); err != nil {
		t.Fatalf("expected the popup sign-in to be valid without clients: %v", err)
	}

	for _, origin := range []string{
		"http://example.com", "https://example.com/path", "example.com", "https://app.example.com@evil.example",
		"https://example.com#frag", "https://Example.com", "https://example.com:443", "https://example.com/",
		"https://example.com:0443", "https://example.com:", "https://example.com:99999", "https://example.com:0",
	} {
		users.OAuth2Server.Popup.Origins = []string{origin}
		if err := s.app.Save(users); err == nil {
			t.Fatalf("expected origin %q to be rejected", origin)
		}
	}

	users.OAuth2Server.Popup.Origins = []string{"http://localhost:5173"}
	if err := s.app.Save(users); err != nil {
		t.Fatalf("expected a localhost origin to be valid: %v", err)
	}

	users.OAuth2Server.Popup.Enabled = false
	users.OAuth2Server.Popup.Origins = nil
	if err := s.app.Save(users); err == nil {
		t.Fatal("expected an enabled OAuth2 server without clients and popup sign-in to be rejected")
	}
}
