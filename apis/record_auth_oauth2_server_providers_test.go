package apis_test

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/auth"
	"golang.org/x/oauth2"
)

const hostedTestProvider = "hostedtest"

func init() {
	auth.Providers[hostedTestProvider] = func() auth.Provider {
		p := &oauth2MockProvider{
			AuthUser: &auth.AuthUser{Id: "hosted-ext-1", Email: "test@example.com"},
			Token:    &oauth2.Token{AccessToken: "abc"},
		}
		p.SetDisplayName("Test Provider")
		p.SetAuthURL("https://provider.example.com/auth")
		p.SetPKCE(true)
		return p
	}
}

func newHostedProvidersTestServer(t *testing.T) *oauth2TestServer {
	s := newOAuth2TestServer(t)

	users, err := s.app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	users.OAuth2Server.SessionDuration = 3600
	users.OAuth2.Enabled = true
	users.OAuth2.Providers = []core.OAuth2ProviderConfig{
		{Name: hostedTestProvider, ClientId: "abc", ClientSecret: "123"},
		// this same sign-in page (used by other app instances sharing the database)
		{
			Name:         "oidc",
			DisplayName:  "Central sign-in",
			ClientId:     oauth2TestClientId,
			ClientSecret: oauth2TestClientSecret,
			AuthURL:      "http://example.com/api/collections/users/oauth2-server/authorize",
			TokenURL:     "http://example.com/api/collections/users/oauth2-server/token",
			UserInfoURL:  "http://example.com/api/collections/users/oauth2-server/userinfo",
		},
	}
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}

	return s
}

// startProvider submits the provider button of the hosted page and
// returns the provider url and the browser cookie.
func (s *oauth2TestServer) startProvider(params url.Values, provider string) (*url.URL, *http.Cookie) {
	form := url.Values{}
	for k, v := range params {
		form[k] = v
	}
	form.Set("provider", provider)

	res := s.do(http.MethodPost, "/api/collections/users/oauth2-server/authorize", form, nil)
	if res.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(res.Body)
		s.t.Fatalf("expected a redirect to the provider, got %d: %s", res.StatusCode, body)
	}

	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		s.t.Fatal(err)
	}

	for _, c := range res.Cookies() {
		if strings.HasPrefix(c.Name, "pb_oauth2_provider_") {
			return loc, c
		}
	}

	s.t.Fatal("missing provider cookie")
	return nil, nil
}

func (s *oauth2TestServer) providerRedirect(query url.Values, cookies ...*http.Cookie) *http.Response {
	header := map[string]string{}
	parts := []string{}
	for _, c := range cookies {
		if c != nil {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	if len(parts) > 0 {
		header["Cookie"] = strings.Join(parts, "; ")
	}

	return s.do(http.MethodGet, "/api/oauth2-redirect?"+query.Encode(), nil, header)
}

func TestOAuth2ServerProvidersSignIn(t *testing.T) {
	t.Parallel()

	s := newHostedProvidersTestServer(t)

	user, err := s.app.FindAuthRecordByEmail("users", "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	totalUsers, _ := s.app.CountRecords("users")

	// the page lists the enabled providers (but not itself)
	params := s.authorizeParams(nil)
	res := s.do(http.MethodGet, "/api/collections/users/oauth2-server/authorize?"+params.Encode(), nil, nil)
	page, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(page), "Continue with Test Provider") ||
		!strings.Contains(string(page), `name="provider" value="hostedtest"`) {
		t.Fatalf("expected the provider button (%d): %s", res.StatusCode, page)
	}
	if strings.Contains(string(page), "Central sign-in") {
		t.Fatal("the page must not list itself as a provider")
	}

	// choosing the provider sends the browser to it
	loc, cookie := s.startProvider(params, hostedTestProvider)
	q := loc.Query()
	state := q.Get("state")
	if loc.Host != "provider.example.com" || !strings.HasPrefix(state, "pbh_") ||
		q.Get("redirect_uri") != "http://example.com/api/oauth2-redirect" ||
		q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "abc" {
		t.Fatalf("unexpected provider url %s", loc)
	}
	if !cookie.HttpOnly || cookie.Path != "/api/oauth2-redirect" || cookie.Name != "pb_oauth2_provider_"+strings.TrimPrefix(state, "pbh_") {
		t.Fatalf("unexpected provider cookie %+v", cookie)
	}

	// the provider redirect completes the page flow
	res = s.providerRedirect(url.Values{"state": {state}, "code": {"provider-code"}}, cookie)
	if res.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected a redirect to the client, got %d: %s", res.StatusCode, body)
	}
	back, _ := url.Parse(res.Header.Get("Location"))
	code := back.Query().Get("code")
	if !strings.HasPrefix(back.String(), oauth2TestRedirectURI+"?") || code == "" || back.Query().Get("state") != "xyz" {
		t.Fatalf("unexpected client redirect %s", back)
	}

	// the sign-in is remembered like any other
	remembered := false
	for _, c := range res.Cookies() {
		if strings.HasPrefix(c.Name, "pb_oauth2_session_") && c.Value != "" {
			remembered = true
		}
	}
	if !remembered {
		t.Fatal("expected the sign-in to be remembered")
	}

	status, data := s.exchange(oauth2TestTokenForm(code, nil), nil)
	if status != 200 {
		t.Fatalf("token exchange: %d %v", status, data)
	}
	token, _ := data["access_token"].(string)
	if found, err := s.app.FindAuthRecordByToken(token, core.TokenTypeAuth); err != nil || found.Id != user.Id {
		t.Fatalf("expected the token of %q: %v", user.Id, err)
	}
	if after, _ := s.app.CountRecords("users"); after != totalUsers {
		t.Fatalf("expected no new users, had %d now %d", totalUsers, after)
	}

	// single use
	res = s.providerRedirect(url.Values{"state": {state}, "code": {"provider-code"}}, cookie)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected the used state to be rejected, got %d", res.StatusCode)
	}
}

func TestOAuth2ServerProvidersPopupSignIn(t *testing.T) {
	t.Parallel()

	s := newHostedProvidersTestServer(t)

	users, _ := s.app.FindCollectionByNameOrId("users")
	users.OAuth2Server.Popup = core.OAuth2ServerPopupConfig{Enabled: true, Origins: []string{popupTestOrigin}}
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}

	loc, cookie := s.startProvider(popupTestParams(nil), hostedTestProvider)

	res := s.providerRedirect(url.Values{"state": {loc.Query().Get("state")}, "code": {"provider-code"}}, cookie)
	back, _ := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != http.StatusSeeOther || back.Path != "/api/collections/users/oauth2-server/popup-callback" {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected a redirect to the popup callback, got %d %s: %s", res.StatusCode, back, body)
	}

	status, data := s.popupExchange(back.Query().Get("code"), oauth2TestVerifier, popupTestOrigin)
	if status != 200 || data["token"] == nil {
		t.Fatalf("auth-with-popup: %d %v", status, data)
	}
}

func TestOAuth2ServerProvidersValidation(t *testing.T) {
	t.Parallel()

	s := newHostedProvidersTestServer(t)
	params := s.authorizeParams(nil)

	post := func(provider string) *http.Response {
		form := url.Values{}
		for k, v := range params {
			form[k] = v
		}
		form.Set("provider", provider)
		return s.do(http.MethodPost, "/api/collections/users/oauth2-server/authorize", form, nil)
	}

	// unknown provider or the page itself
	for _, name := range []string{"missing", "oidc"} {
		if res := post(name); res.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected provider %q to be rejected, got %d", name, res.StatusCode)
		}
	}

	// another browser (no cookie) is rejected and doesn't burn the pending sign-in
	loc, cookie := s.startProvider(params, hostedTestProvider)
	res := s.providerRedirect(url.Values{"state": {loc.Query().Get("state")}, "code": {"provider-code"}})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected the redirect without the browser cookie to be rejected, got %d", res.StatusCode)
	}
	res = s.providerRedirect(url.Values{"state": {loc.Query().Get("state")}, "code": {"provider-code"}}, cookie)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected the original browser to still complete the sign-in, got %d", res.StatusCode)
	}

	// canceled at the provider -> back on the sign-in form, which still posts to the authorize endpoint
	loc, cookie = s.startProvider(params, hostedTestProvider)
	res = s.providerRedirect(url.Values{"state": {loc.Query().Get("state")}, "error": {"access_denied"}}, cookie)
	page, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(page), "Test Provider was canceled or failed") ||
		!strings.Contains(string(page), `action="/api/collections/users/oauth2-server/authorize"`) {
		t.Fatalf("expected the sign-in form with an error, got %d: %s", res.StatusCode, page)
	}

	// unknown state of the hosted page
	res = s.providerRedirect(url.Values{"state": {"pbh_missing"}, "code": {"provider-code"}}, cookie)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected an unknown state to be rejected, got %d", res.StatusCode)
	}

	// disabled OAuth2 -> no buttons
	users, _ := s.app.FindCollectionByNameOrId("users")
	users.OAuth2.Enabled = false
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}
	res = s.do(http.MethodGet, "/api/collections/users/oauth2-server/authorize?"+params.Encode(), nil, nil)
	page, _ = io.ReadAll(res.Body)
	if res.StatusCode != 200 || strings.Contains(string(page), "Continue with") {
		t.Fatalf("expected no provider buttons (%d): %s", res.StatusCode, page)
	}
	if res := post(hostedTestProvider); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected the disabled provider to be rejected, got %d", res.StatusCode)
	}
}

func TestOAuth2ServerProvidersConcurrentSignIns(t *testing.T) {
	t.Parallel()

	s := newHostedProvidersTestServer(t)
	params := s.authorizeParams(nil)

	locA, cookieA := s.startProvider(params, hostedTestProvider)
	locB, cookieB := s.startProvider(params, hostedTestProvider)
	if cookieA.Name == cookieB.Name {
		t.Fatal("expected one binding cookie per pending sign-in")
	}

	// the browser holds both cookies; B completes first, then A
	for _, step := range []struct {
		loc *url.URL
	}{{locB}, {locA}} {
		res := s.providerRedirect(url.Values{"state": {step.loc.Query().Get("state")}, "code": {"provider-code"}}, cookieA, cookieB)
		if res.StatusCode != http.StatusSeeOther {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("expected both overlapping sign-ins to complete, got %d: %s", res.StatusCode, body)
		}
	}
}

func TestOAuth2ServerProvidersCrossSitePost(t *testing.T) {
	t.Parallel()

	s := newHostedProvidersTestServer(t)

	form := s.authorizeParams(nil)
	form.Set("provider", hostedTestProvider)

	scenarios := []struct {
		name   string
		header map[string]string
		status int
	}{
		{"cross-site fetch metadata", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"same-site (sibling) fetch metadata", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		{"foreign origin", map[string]string{"Origin": "https://evil.example.com"}, 403},
		{"null origin", map[string]string{"Origin": "null"}, 403},
		// a same-origin form post under a no-referrer policy sends "Origin: null" with same-origin fetch metadata
		{"null origin with same-origin fetch metadata", map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"}, 303},
		{"null origin with cross-site fetch metadata", map[string]string{"Origin": "null", "Sec-Fetch-Site": "cross-site"}, 403},
		{"same host other scheme", map[string]string{"Origin": "https://example.com"}, 403},
		{"same-origin fetch metadata", map[string]string{"Sec-Fetch-Site": "same-origin"}, 303},
		{"same origin header", map[string]string{"Origin": "http://example.com"}, 303},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			res := s.do(http.MethodPost, "/api/collections/users/oauth2-server/authorize", form, sc.header)
			if res.StatusCode != sc.status {
				t.Fatalf("expected %d, got %d", sc.status, res.StatusCode)
			}
			if sc.status == 403 && len(res.Cookies()) > 0 {
				t.Fatal("a rejected post must not set cookies")
			}
		})
	}
}

func TestOAuth2ServerProvidersMFABlocksBeforeLinking(t *testing.T) {
	t.Parallel()

	s := newHostedProvidersTestServer(t)

	users, _ := s.app.FindCollectionByNameOrId("users")
	users.MFA.Enabled = true
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}

	user, err := s.app.FindAuthRecordByEmail("users", "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	user.SetVerified(false)
	if err := s.app.Save(user); err != nil {
		t.Fatal(err)
	}

	linksBefore, _ := s.app.FindAllExternalAuthsByRecord(user)

	loc, cookie := s.startProvider(s.authorizeParams(nil), hostedTestProvider)
	res := s.providerRedirect(url.Values{"state": {loc.Query().Get("state")}, "code": {"provider-code"}}, cookie)
	page, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(page), "multi-factor") {
		t.Fatalf("expected the MFA message, got %d: %s", res.StatusCode, page)
	}

	// nothing was linked, verified or rotated
	after, _ := s.app.FindAuthRecordByEmail("users", "test@example.com")
	if after.Verified() || after.ValidatePassword("1234567890") == false {
		t.Fatal("expected the account password and verification to stay untouched")
	}
	if links, _ := s.app.FindAllExternalAuthsByRecord(after); len(links) != len(linksBefore) {
		t.Fatalf("expected the linked providers to stay %d, got %d", len(linksBefore), len(links))
	}
}
