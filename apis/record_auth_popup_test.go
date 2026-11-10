package apis_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

const popupTestOrigin = "https://app.example.com"

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

func popupTestParams(extra map[string]string) url.Values {
	v := url.Values{
		"response_type":         {"code"},
		"client_id":             {"@popup"},
		"redirect_uri":          {popupTestOrigin},
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

func (s *oauth2TestServer) popupExchange(code, verifier, origin string) (int, map[string]any) {
	req := httptestRequest(http.MethodPost, "/api/collections/users/auth-with-popup", strings.NewReader(
		`{"code":"`+code+`","codeVerifier":"`+verifier+`"}`,
	))
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

func TestPopupSignInFullFlow(t *testing.T) {
	t.Parallel()

	s := newPopupTestServer(t)

	user, err := s.app.FindAuthRecordByEmail("users", "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// helper script
	res := s.do(http.MethodGet, "/api/collections/users/oauth2-server/popup.js", nil, nil)
	script, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(script), "export async function authWithPopup") ||
		!strings.HasPrefix(res.Header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("unexpected helper script (%d %s)", res.StatusCode, res.Header.Get("Content-Type"))
	}

	// the sign-in page shows who asks for the sign-in
	params := popupTestParams(nil)
	res = s.do(http.MethodGet, "/api/collections/users/oauth2-server/authorize?"+params.Encode(), nil, nil)
	page, _ := io.ReadAll(res.Body)
	if res.Header.Get("Cross-Origin-Opener-Policy") != "unsafe-none" {
		t.Fatalf("expected the popup page to keep the opener, got COOP %q", res.Header.Get("Cross-Origin-Opener-Policy"))
	}
	if res.StatusCode != 200 || !strings.Contains(string(page), "<strong>app.example.com</strong>") || !strings.Contains(string(page), `name="password"`) {
		t.Fatalf("unexpected login page (%d): %s", res.StatusCode, page)
	}

	// the sign-in lands on the callback page (not on the frontend)
	res, code := s.login(params, "test@example.com", "1234567890")
	if code == "" {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected code redirect, got %d: %s", res.StatusCode, body)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	if loc.Host != "" || loc.Path != "/api/collections/users/oauth2-server/popup-callback" ||
		loc.Query().Get("origin") != popupTestOrigin || loc.Query().Get("state") != "xyz" {
		t.Fatalf("unexpected redirect %s", loc)
	}

	// the callback page posts the code only to the frontend origin
	res = s.do(http.MethodGet, loc.String(), nil, nil)
	page, _ = io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(page), "postMessage") ||
		!strings.Contains(string(page), `"code":"`+code+`"`) || !strings.Contains(string(page), `"state":"xyz"`) ||
		!strings.Contains(string(page), `"https://app.example.com"`) {
		t.Fatalf("unexpected callback page (%d): %s", res.StatusCode, page)
	}

	// the code can't be used at the OAuth2 token endpoint
	if status, data := s.exchange(oauth2TestTokenForm(code, map[string]string{"client_id": "@popup", "client_secret": ""}), nil); status != 401 {
		t.Fatalf("expected the token endpoint to reject the popup client, got %d %v", status, data)
	}

	// exchange
	status, data := s.popupExchange(code, oauth2TestVerifier, popupTestOrigin)
	if status != 200 {
		t.Fatalf("auth-with-popup: %d %v", status, data)
	}
	record, _ := data["record"].(map[string]any)
	token, _ := data["token"].(string)
	if record["id"] != user.Id {
		t.Fatalf("expected record %q, got %v", user.Id, record["id"])
	}
	if found, err := s.app.FindAuthRecordByToken(token, core.TokenTypeAuth); err != nil || found.Id != user.Id {
		t.Fatalf("invalid returned token: %v", err)
	}

	// single use
	if status, _ := s.popupExchange(code, oauth2TestVerifier, popupTestOrigin); status != 400 {
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
		{"application url origin", map[string]string{"redirect_uri": "https://pb.example.com"}, 200, ""},
		{"unpinned request host is not an origin", map[string]string{"redirect_uri": "http://example.com"}, 400, ""},
		{"unknown origin", map[string]string{"redirect_uri": "https://evil.example.com"}, 400, ""},
		{"origin with path", map[string]string{"redirect_uri": popupTestOrigin + "/cb"}, 400, ""},
		{"missing origin", map[string]string{"redirect_uri": ""}, 400, ""},
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
			if sc.location != "" && (!strings.HasPrefix(location, "/api/collections/users/oauth2-server/popup-callback?") || !strings.Contains(location, sc.location)) {
				t.Fatalf("expected a callback redirect with %q, got %q", sc.location, location)
			}
		})
	}

	// behind a TLS terminating proxy (plain http request, https Application URL)
	// the http variant of the public origin is not implied
	for origin, status := range map[string]int{"https://pb.example.com": 200, "http://pb.example.com": 400} {
		target := "http://pb.example.com/api/collections/users/oauth2-server/authorize?" + popupTestParams(map[string]string{"redirect_uri": origin}).Encode()
		if res := s.do(http.MethodGet, target, nil, nil); res.StatusCode != status {
			t.Fatalf("expected %d for origin %q behind the proxy, got %d", status, origin, res.StatusCode)
		}
	}

	// the local server on loopback is allowed (development)
	loopback := "http://127.0.0.1:8090"
	target := loopback + "/api/collections/users/oauth2-server/authorize?" + popupTestParams(map[string]string{"redirect_uri": loopback}).Encode()
	if res := s.do(http.MethodGet, target, nil, nil); res.StatusCode != 200 {
		t.Fatalf("expected the loopback origin to be allowed, got %d", res.StatusCode)
	}

	// any other host name that reaches the server is not
	target = "http://login.attacker.com:8090/api/collections/users/oauth2-server/authorize?" + popupTestParams(map[string]string{"redirect_uri": "http://login.attacker.com:8090"}).Encode()
	if res := s.do(http.MethodGet, target, nil, nil); res.StatusCode != 400 {
		t.Fatalf("expected an unpinned host to be rejected, got %d", res.StatusCode)
	}

	// the callback doesn't post to other origins
	res := s.do(http.MethodGet, "/api/collections/users/oauth2-server/popup-callback?code=abc&state=xyz&origin=https%3A%2F%2Fevil.example.com", nil, nil)
	page, _ := io.ReadAll(res.Body)
	if res.StatusCode != 400 || strings.Contains(string(page), "postMessage") {
		t.Fatalf("expected the callback to reject the origin, got %d: %s", res.StatusCode, page)
	}

	// disabled
	users, _ := s.app.FindCollectionByNameOrId("users")
	users.OAuth2Server.Popup.Enabled = false
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}
	res = s.do(http.MethodGet, "/api/collections/users/oauth2-server/authorize?"+popupTestParams(nil).Encode(), nil, nil)
	if res.StatusCode != 400 {
		t.Fatalf("expected the disabled popup sign-in to be rejected, got %d", res.StatusCode)
	}
	if status, _ := s.popupExchange("abc", oauth2TestVerifier, ""); status != 404 {
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
		name     string
		params   url.Values
		verifier string
		origin   string
		status   int
	}{
		{"wrong verifier", popupTestParams(nil), strings.Repeat("a", 43), popupTestOrigin, 400},
		{"missing verifier", popupTestParams(nil), "", popupTestOrigin, 400},
		{"another origin", popupTestParams(nil), oauth2TestVerifier, "https://evil.example.com", 400},
		{"code of a registered client", s.authorizeParams(nil), oauth2TestVerifier, popupTestOrigin, 400},
		{"no origin header", popupTestParams(nil), oauth2TestVerifier, "", 400},
		{"matching origin", popupTestParams(nil), oauth2TestVerifier, popupTestOrigin, 200},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			status, data := s.popupExchange(newCode(sc.params), sc.verifier, sc.origin)
			if status != sc.status {
				t.Fatalf("expected %d, got %d %v", sc.status, status, data)
			}
		})
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

func TestPopupSignInFailedExchangeKeepsRegisteredClientCode(t *testing.T) {
	t.Parallel()

	s := newPopupTestServer(t)

	res, code := s.login(s.authorizeParams(nil), "test@example.com", "1234567890")
	if code == "" {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected code redirect, got %d: %s", res.StatusCode, body)
	}

	// the popup endpoint rejects it without burning it
	if status, _ := s.popupExchange(code, oauth2TestVerifier, popupTestOrigin); status != 400 {
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
