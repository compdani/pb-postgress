package apis_test

import (
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestOAuth2ServerRememberedSignIn(t *testing.T) {
	t.Parallel()

	s := newOAuth2TestServer(t)

	users, err := s.app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	users.OAuth2Server.SessionDuration = 3600
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}
	cookieName := "pb_oauth2_session_" + users.Id

	authorize := func(extra map[string]string, cookie *http.Cookie) *http.Response {
		header := map[string]string{}
		if cookie != nil {
			header["Cookie"] = cookie.Name + "=" + cookie.Value
		}
		return s.do(http.MethodGet, "/api/collections/users/oauth2-server/authorize?"+s.authorizeParams(extra).Encode(), nil, header)
	}

	codeFrom := func(res *http.Response) string {
		loc, err := url.Parse(res.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		return loc.Query().Get("code")
	}

	// prompt=none without a session -> login_required back to the client
	res := authorize(map[string]string{"prompt": "none"}, nil)
	if loc, _ := url.Parse(res.Header.Get("Location")); res.StatusCode != http.StatusSeeOther || loc.Query().Get("error") != "login_required" {
		t.Fatalf("expected login_required, got %d %s", res.StatusCode, res.Header.Get("Location"))
	}

	// sign in once
	res, code := s.login(s.authorizeParams(nil), "test@example.com", "1234567890")
	if code == "" {
		t.Fatalf("expected a code, got %d", res.StatusCode)
	}

	var session *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == cookieName {
			session = c
		}
	}
	if session == nil || session.Value == "" || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode ||
		session.Path != "/api/collections/users/oauth2-server" || session.MaxAge != 3600 {
		t.Fatalf("unexpected session cookie %+v", session)
	}

	// next authorization (eg. another app popup) shows the account chooser
	res = authorize(nil, session)
	page, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(page), "Continue as") ||
		!strings.Contains(string(page), "test@example.com") ||
		!strings.Contains(string(page), "Use another account") || !strings.Contains(string(page), "Sign out") {
		t.Fatalf("expected the account chooser, got %d: %s", res.StatusCode, page)
	}
	if strings.Contains(string(page), `name="password"`) {
		t.Fatal("the chooser must not ask for the password")
	}

	// "use another account" points to the same request with prompt=login
	otherMatch := regexp.MustCompile(`href="([^"]*prompt=login[^"]*)"`).FindSubmatch(page)
	if otherMatch == nil {
		t.Fatal("missing the use another account link")
	}
	otherURL := html.UnescapeString(string(otherMatch[1]))
	res = s.do(http.MethodGet, otherURL, nil, map[string]string{"Cookie": session.Name + "=" + session.Value})
	page, _ = io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(page), `name="password"`) || strings.Contains(string(page), "Continue as") {
		t.Fatalf("expected the sign-in form for another account, got %d", res.StatusCode)
	}

	chooserAction := func(action string, cookie *http.Cookie) *http.Response {
		form := s.authorizeParams(nil)
		form.Set("action", action)
		header := map[string]string{}
		if cookie != nil {
			header["Cookie"] = cookie.Name + "=" + cookie.Value
		}
		return s.do(http.MethodPost, "/api/collections/users/oauth2-server/authorize", form, header)
	}

	// continue -> code without asking for credentials
	res = chooserAction("continue", session)
	code = codeFrom(res)
	if res.StatusCode != http.StatusSeeOther || code == "" {
		t.Fatalf("expected continue to redirect with a code, got %d", res.StatusCode)
	}
	if status, data := s.exchange(oauth2TestTokenForm(code, nil), nil); status != 200 {
		t.Fatalf("expected the remembered code to work, got %d %v", status, data)
	}

	// continue without the cookie (eg. a cross-site post) is refused
	if res := chooserAction("continue", nil); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected continue without session to fail, got %d", res.StatusCode)
	}

	// prompt=select_account shows the chooser too
	if res := authorize(map[string]string{"prompt": "select_account"}, session); res.StatusCode != 200 {
		t.Fatalf("expected the chooser for select_account, got %d", res.StatusCode)
	}

	// prompt=none with a session completes silently
	if res := authorize(map[string]string{"prompt": "none"}, session); res.StatusCode != http.StatusFound || codeFrom(res) == "" {
		t.Fatalf("expected prompt=none to complete silently, got %d", res.StatusCode)
	}

	// prompt=login forces the sign-in page
	if res := authorize(map[string]string{"prompt": "login"}, session); res.StatusCode != 200 {
		t.Fatalf("expected the sign-in page for prompt=login, got %d", res.StatusCode)
	}

	// sign out from the chooser clears the session and shows the sign-in form
	res = chooserAction("signout", session)
	page, _ = io.ReadAll(res.Body)
	cleared := false
	for _, c := range res.Cookies() {
		if c.Name == cookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if res.StatusCode != 200 || !cleared || !strings.Contains(string(page), `name="password"`) {
		t.Fatalf("expected sign out to clear the session and show the form, got %d (cleared %v)", res.StatusCode, cleared)
	}

	// sign in again for the remaining checks
	res, _ = s.login(s.authorizeParams(nil), "test@example.com", "1234567890")
	for _, c := range res.Cookies() {
		if c.Name == cookieName {
			session = c
		}
	}

	// invalid prompt
	if res := authorize(map[string]string{"prompt": "none login"}, session); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected invalid prompt combination to be rejected, got %d", res.StatusCode)
	}

	// the cookie value is not an api auth token
	if res := s.do(http.MethodGet, "/api/collections/users/oauth2-server/userinfo", nil, map[string]string{"Authorization": session.Value}); res.StatusCode != 401 {
		t.Fatalf("expected the session cookie to be rejected as api token, got %d", res.StatusCode)
	}

	// a forged/tampered cookie is ignored (sign-in form instead of the chooser)
	tampered := &http.Cookie{Name: cookieName, Value: session.Value[:len(session.Value)-2] + "xx"}
	if res := authorize(nil, tampered); !strings.Contains(readBody(res), `name="password"`) {
		t.Fatalf("expected a tampered cookie to show the sign-in page, got %d", res.StatusCode)
	}

	// sign out with a registered return address
	res = s.do(http.MethodGet, "/api/collections/users/oauth2-server/logout?client_id="+oauth2TestClientId+
		"&post_logout_redirect_uri="+url.QueryEscape(oauth2TestRedirectURI)+"&state=bye", nil, nil)
	if res.StatusCode != http.StatusFound || !strings.HasPrefix(res.Header.Get("Location"), oauth2TestRedirectURI+"?state=bye") {
		t.Fatalf("unexpected logout redirect %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	cleared = false
	for _, c := range res.Cookies() {
		if c.Name == cookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("expected the session cookie to be cleared")
	}

	// unregistered return addresses are not followed
	res = s.do(http.MethodGet, "/api/collections/users/oauth2-server/logout?client_id="+oauth2TestClientId+
		"&post_logout_redirect_uri="+url.QueryEscape("https://evil.example.com/"), nil, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected unregistered logout redirect to be refused, got %d", res.StatusCode)
	}

	// changing the tokenKey (eg. password change / sign out everywhere) ends the session
	user, err := s.app.FindAuthRecordByEmail("users", "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	user.RefreshTokenKey()
	if err := s.app.Save(user); err != nil {
		t.Fatal(err)
	}
	if res := authorize(nil, session); !strings.Contains(readBody(res), `name="password"`) {
		t.Fatal("expected the sign-in form after tokenKey change")
	}

	// disabled sessions -> no cookie and always ask
	users, _ = s.app.FindCollectionByNameOrId("users")
	users.OAuth2Server.SessionDuration = 0
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}
	res, _ = s.login(s.authorizeParams(nil), "test@example.com", "1234567890")
	for _, c := range res.Cookies() {
		if c.Name == cookieName {
			t.Fatal("expected no session cookie when sessions are disabled")
		}
	}
}

func readBody(res *http.Response) string {
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return string(raw)
}
