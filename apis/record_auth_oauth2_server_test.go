package apis_test

import (
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pocketbase/dbx"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

const (
	oauth2TestClientId     = "event-pocket"
	oauth2TestClientSecret = "0123456789abcdefghijklmnopqrstuvwxyz"
	oauth2TestRedirectURI  = "https://events.example.com/auth/callback"
	oauth2TestVerifier     = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk_extra"
)

func oauth2TestChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type oauth2TestServer struct {
	t       *testing.T
	app     *tests.TestApp
	handler http.Handler
}

func newOAuth2TestServer(t *testing.T) *oauth2TestServer {
	app, err := tests.NewTestApp(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	users.MFA.Enabled = false
	users.OAuth2Server.Enabled = true
	users.OAuth2Server.CodeDuration = 60
	users.OAuth2Server.Clients = []core.OAuth2ServerClient{{
		Id:           oauth2TestClientId,
		Name:         "Event Pocket",
		Secret:       oauth2TestClientSecret,
		RedirectURIs: []string{oauth2TestRedirectURI},
	}}
	if err := app.Save(users); err != nil {
		t.Fatal(err)
	}

	router, err := apis.NewRouter(app)
	if err != nil {
		t.Fatal(err)
	}

	var handler http.Handler
	serveEvent := &core.ServeEvent{App: app, Router: router}
	err = app.OnServe().Trigger(serveEvent, func(e *core.ServeEvent) error {
		mux, err := e.Router.BuildMux()
		handler = mux
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	return &oauth2TestServer{t: t, app: app, handler: handler}
}

func (s *oauth2TestServer) do(method, target string, body url.Values, header map[string]string) *http.Response {
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(body.Encode())
	}

	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)

	return rec.Result()
}

func (s *oauth2TestServer) authorizeParams(extra map[string]string) url.Values {
	v := url.Values{
		"response_type":         {"code"},
		"client_id":             {oauth2TestClientId},
		"redirect_uri":          {oauth2TestRedirectURI},
		"state":                 {"xyz"},
		"scope":                 {"openid email profile"},
		"nonce":                 {"n-123"},
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

// login submits the hosted login form and returns the issued code.
func (s *oauth2TestServer) login(params url.Values, identity, password string) (*http.Response, string) {
	form := url.Values{}
	for k, v := range params {
		form[k] = v
	}
	form.Set("identity", identity)
	form.Set("password", password)

	res := s.do(http.MethodPost, "/api/collections/users/oauth2-server/authorize", form, nil)
	if res.StatusCode != http.StatusSeeOther {
		return res, ""
	}

	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		s.t.Fatal(err)
	}

	return res, loc.Query().Get("code")
}

func (s *oauth2TestServer) exchange(form url.Values, header map[string]string) (int, map[string]any) {
	res := s.do(http.MethodPost, "/api/collections/users/oauth2-server/token", form, header)
	defer res.Body.Close()

	data := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&data)

	return res.StatusCode, data
}

func oauth2TestTokenForm(code string, extra map[string]string) url.Values {
	v := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {oauth2TestRedirectURI},
		"client_id":     {oauth2TestClientId},
		"client_secret": {oauth2TestClientSecret},
		"code_verifier": {oauth2TestVerifier},
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

func TestOAuth2ServerFullFlow(t *testing.T) {
	t.Parallel()

	s := newOAuth2TestServer(t)

	// discovery
	res := s.do(http.MethodGet, "/api/collections/users/oauth2-server/.well-known/openid-configuration", nil, nil)
	if res.StatusCode != 200 {
		t.Fatalf("discovery status %d", res.StatusCode)
	}
	discovery := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&discovery)
	issuer, _ := discovery["issuer"].(string)
	if !strings.HasSuffix(issuer, "/api/collections/users/oauth2-server") {
		t.Fatalf("unexpected issuer %q", issuer)
	}

	// login page
	params := s.authorizeParams(nil)
	res = s.do(http.MethodGet, "/api/collections/users/oauth2-server/authorize?"+params.Encode(), nil, nil)
	page, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(page), "Event Pocket") || !strings.Contains(string(page), `name="password"`) {
		t.Fatalf("unexpected login page (%d): %s", res.StatusCode, page)
	}
	if res.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal("expected X-Frame-Options DENY")
	}

	// wrong password re-renders the page
	res, code := s.login(params, "test@example.com", "wrong")
	if res.StatusCode != http.StatusBadRequest || code != "" {
		t.Fatalf("expected failed login, got %d", res.StatusCode)
	}

	// successful login
	res, code = s.login(params, "test@example.com", "1234567890")
	if code == "" {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected code redirect, got %d: %s", res.StatusCode, body)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	if loc.Query().Get("state") != "xyz" || loc.Query().Get("iss") != issuer {
		t.Fatalf("unexpected redirect %s", loc)
	}
	if !strings.HasPrefix(loc.String(), oauth2TestRedirectURI+"?") {
		t.Fatalf("unexpected redirect target %s", loc)
	}

	// only the hash is stored
	if _, err := s.app.FindFirstRecordByData(core.CollectionNameOAuth2Codes, "codeHash", code); err == nil {
		t.Fatal("raw code must not be stored")
	}

	// bad PKCE verifier consumes the code and fails
	status, data := s.exchange(oauth2TestTokenForm(code, map[string]string{"code_verifier": strings.Repeat("a", 43)}), nil)
	if status != 400 || data["error"] != "invalid_grant" {
		t.Fatalf("expected invalid_grant for bad verifier, got %d %v", status, data)
	}

	// the code is single use, even after a failed attempt
	status, data = s.exchange(oauth2TestTokenForm(code, nil), nil)
	if status != 400 || data["error"] != "invalid_grant" {
		t.Fatalf("expected invalid_grant for reused code, got %d %v", status, data)
	}

	// new code + basic client auth
	_, code = s.login(params, "test@example.com", "1234567890")
	form := oauth2TestTokenForm(code, map[string]string{"client_id": "", "client_secret": ""})
	basic := base64.StdEncoding.EncodeToString([]byte(oauth2TestClientId + ":" + oauth2TestClientSecret))
	status, data = s.exchange(form, map[string]string{"Authorization": "Basic " + basic})
	if status != 200 {
		t.Fatalf("expected token response, got %d %v", status, data)
	}

	accessToken, _ := data["access_token"].(string)
	if data["token_type"] != "Bearer" || accessToken == "" {
		t.Fatalf("unexpected token response %v", data)
	}

	// the access token is a regular collection auth token
	record, err := s.app.FindAuthRecordByToken(accessToken, core.TokenTypeAuth)
	if err != nil || record.Email() != "test@example.com" {
		t.Fatalf("access token is not a valid auth token: %v", err)
	}

	// id_token signed with the client secret
	idToken, _ := data["id_token"].(string)
	jwksRes := s.do("GET", "/api/collections/users/oauth2-server/jwks", nil, nil)
	var jwks struct {
		Keys []struct {
			N string `json:"n"`
			E string `json:"e"`
		}
	}
	if err := json.NewDecoder(jwksRes.Body).Decode(&jwks); err != nil {
		t.Fatal(err)
	}
	jwksRes.Body.Close()
	n, _ := base64.RawURLEncoding.DecodeString(jwks.Keys[0].N)
	key := rsa.PublicKey{N: new(big.Int).SetBytes(n), E: 65537}
	token, err := jwt.Parse(idToken, func(t *jwt.Token) (any, error) { return &key, nil }, jwt.WithValidMethods([]string{"RS256"}))
	claims := token.Claims.(jwt.MapClaims)
	if err != nil {
		t.Fatalf("invalid id_token: %v", err)
	}
	if claims["sub"] != record.Id || claims["aud"] != oauth2TestClientId || claims["nonce"] != "n-123" ||
		claims["email"] != "test@example.com" || claims["iss"] != issuer {
		t.Fatalf("unexpected id_token claims %v", claims)
	}

	// userinfo
	res = s.do(http.MethodGet, "/api/collections/users/oauth2-server/userinfo", nil, map[string]string{"Authorization": "Bearer " + accessToken})
	userinfo := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&userinfo)
	if res.StatusCode != 200 || userinfo["sub"] != record.Id || userinfo["email"] != "test@example.com" || userinfo["record"] == nil {
		t.Fatalf("unexpected userinfo (%d): %v", res.StatusCode, userinfo)
	}
}

func TestOAuth2ServerAuthorizeValidation(t *testing.T) {
	t.Parallel()

	s := newOAuth2TestServer(t)

	scenarios := []struct {
		name           string
		collection     string
		extra          map[string]string
		expectStatus   int
		expectRedirect string // expected error param when redirecting
	}{
		{"disabled collection", "clients", nil, 404, ""},
		{"unknown client", "users", map[string]string{"client_id": "nope"}, 400, ""},
		{"unregistered redirect", "users", map[string]string{"redirect_uri": "https://evil.example.com/cb"}, 400, ""},
		{"redirect prefix is not enough", "users", map[string]string{"redirect_uri": oauth2TestRedirectURI + "/x"}, 400, ""},
		{"unsupported response type", "users", map[string]string{"response_type": "token"}, 303, "unsupported_response_type"},
		{"plain pkce", "users", map[string]string{"code_challenge_method": "plain"}, 303, "invalid_request"},
		{"pkce without method", "users", map[string]string{"code_challenge_method": ""}, 303, "invalid_request"},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			params := s.authorizeParams(sc.extra)
			res := s.do(http.MethodGet, "/api/collections/"+sc.collection+"/oauth2-server/authorize?"+params.Encode(), nil, nil)
			if res.StatusCode != sc.expectStatus {
				t.Fatalf("expected %d, got %d", sc.expectStatus, res.StatusCode)
			}
			if sc.expectRedirect != "" {
				loc, _ := url.Parse(res.Header.Get("Location"))
				if loc.Query().Get("error") != sc.expectRedirect || loc.Query().Get("state") != "xyz" {
					t.Fatalf("unexpected redirect %s", loc)
				}
			}
		})
	}
}

func TestOAuth2ServerTokenValidation(t *testing.T) {
	t.Parallel()

	s := newOAuth2TestServer(t)
	params := s.authorizeParams(nil)

	newCode := func() string {
		_, code := s.login(params, "test@example.com", "1234567890")
		if code == "" {
			t.Fatal("failed to obtain code")
		}
		return code
	}

	scenarios := []struct {
		name         string
		extra        map[string]string
		expectStatus int
		expectError  string
	}{
		{"wrong secret", map[string]string{"client_secret": strings.Repeat("x", 36)}, 401, "invalid_client"},
		{"unknown client", map[string]string{"client_id": "other"}, 401, "invalid_client"},
		{"wrong grant", map[string]string{"grant_type": "password"}, 400, "unsupported_grant_type"},
		{"wrong redirect", map[string]string{"redirect_uri": "https://events.example.com/other"}, 400, "invalid_grant"},
		{"missing verifier", map[string]string{"code_verifier": ""}, 400, "invalid_grant"},
		{"unknown code", map[string]string{"code": "nope"}, 400, "invalid_grant"},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			extra := map[string]string{"code": newCode()}
			for k, v := range sc.extra {
				extra[k] = v
			}

			status, data := s.exchange(oauth2TestTokenForm(extra["code"], extra), nil)
			if status != sc.expectStatus || data["error"] != sc.expectError {
				t.Fatalf("expected %d %s, got %d %v", sc.expectStatus, sc.expectError, status, data)
			}
		})
	}

	t.Run("pkce downgrade", func(t *testing.T) {
		_, code := s.login(s.authorizeParams(map[string]string{"code_challenge": "", "code_challenge_method": ""}), "test@example.com", "1234567890")
		status, data := s.exchange(oauth2TestTokenForm(code, nil), nil)
		if status != 400 || data["error"] != "invalid_grant" {
			t.Fatalf("expected invalid_grant, got %d %v", status, data)
		}

		// without pkce on both sides it works
		_, code = s.login(s.authorizeParams(map[string]string{"code_challenge": "", "code_challenge_method": ""}), "test@example.com", "1234567890")
		status, data = s.exchange(oauth2TestTokenForm(code, map[string]string{"code_verifier": ""}), nil)
		if status != 200 {
			t.Fatalf("expected 200, got %d %v", status, data)
		}
	})

	t.Run("tokenKey change invalidates pending codes", func(t *testing.T) {
		code := newCode()

		user, err := s.app.FindAuthRecordByEmail("users", "test@example.com")
		if err != nil {
			t.Fatal(err)
		}
		user.RefreshTokenKey()
		if err := s.app.Save(user); err != nil {
			t.Fatal(err)
		}

		status, data := s.exchange(oauth2TestTokenForm(code, nil), nil)
		if status != 400 || data["error"] != "invalid_grant" {
			t.Fatalf("expected invalid_grant, got %d %v", status, data)
		}
	})
}

func TestOAuth2ServerClientSecretsAreRedacted(t *testing.T) {
	t.Parallel()

	s := newOAuth2TestServer(t)

	users, err := s.app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}

	raw, _ := json.Marshal(users)
	if strings.Contains(string(raw), oauth2TestClientSecret) {
		t.Fatal("client secret leaked in the collection serialization")
	}
	if !strings.Contains(string(raw), `"oauth2Server":{`) {
		t.Fatalf("missing oauth2Server options: %s", raw)
	}

	// updating via the api without secrets keeps them by client id
	superuser, err := s.app.FindAuthRecordByEmail(core.CollectionNameSuperusers, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := superuser.NewAuthToken()

	body := `{"oauth2Server":{"enabled":true,"codeDuration":60,"clients":[` +
		`{"id":"new-client","name":"New","secret":"","redirectURIs":["https://new.example.com/cb"]},` +
		`{"id":"` + oauth2TestClientId + `","name":"Event Pocket","redirectURIs":["` + oauth2TestRedirectURI + `"]}]}}`

	req := httptest.NewRequest(http.MethodPatch, "/api/collections/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", token)
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)

	// the new client has no secret -> validation error (and must not inherit one)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"secret"`) {
		t.Fatalf("expected secret validation error, got %d: %s", rec.Code, rec.Body.String())
	}

	body = strings.Replace(body, `"secret":""`, `"secret":"`+strings.Repeat("s", 40)+`"`, 1)
	req = httptest.NewRequest(http.MethodPatch, "/api/collections/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", token)
	rec = httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	users, _ = s.app.FindCollectionByNameOrId("users")
	client, ok := users.OAuth2Server.GetClient(oauth2TestClientId)
	if !ok || !client.ValidateSecret(oauth2TestClientSecret) {
		t.Fatalf("expected the existing client secret to be preserved, got %+v", client)
	}
	newClient, _ := users.OAuth2Server.GetClient("new-client")
	if !newClient.ValidateSecret(strings.Repeat("s", 40)) {
		t.Fatalf("unexpected new client secret %q", newClient.Secret)
	}
}

func TestOAuth2ServerConfigValidation(t *testing.T) {
	t.Parallel()

	s := newOAuth2TestServer(t)

	superusers, err := s.app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	if err != nil {
		t.Fatal(err)
	}
	superusers.OAuth2Server = core.OAuth2ServerConfig{
		Enabled:      true,
		CodeDuration: 60,
		Clients: []core.OAuth2ServerClient{{
			Id: "x-client", Secret: oauth2TestClientSecret, RedirectURIs: []string{oauth2TestRedirectURI},
		}},
	}
	if err := s.app.Save(superusers); err == nil {
		t.Fatal("expected the superusers collection to reject the OAuth2 server option")
	}

	users, _ := s.app.FindCollectionByNameOrId("users")
	for _, uri := range []string{"http://example.com/cb", "https://example.com/cb#frag", "/relative"} {
		users.OAuth2Server.Clients[0].RedirectURIs = []string{uri}
		if err := s.app.Save(users); err == nil {
			t.Fatalf("expected redirect uri %q to be rejected", uri)
		}
	}

	users.OAuth2Server.Clients[0].RedirectURIs = []string{"http://localhost:3000/cb", "myapp://callback"}
	if err := s.app.Save(users); err != nil {
		t.Fatalf("expected loopback/custom scheme redirect uris to be valid: %v", err)
	}
}

func TestOAuth2ServerPublicClient(t *testing.T) {
	s := newOAuth2TestServer(t)
	users, err := s.app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	users.OAuth2Server.Clients[0].Public = true
	users.OAuth2Server.Clients[0].Secret = ""
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}
	params := s.authorizeParams(map[string]string{"code_challenge": "", "code_challenge_method": ""})
	res := s.do(http.MethodGet, "/api/collections/users/oauth2-server/authorize?"+params.Encode(), nil, nil)
	if !strings.Contains(res.Header.Get("Location"), "error=invalid_request") {
		t.Fatalf("missing PKCE accepted: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res.Body.Close()
	params = s.authorizeParams(nil)
	res, code := s.login(params, "test@example.com", "1234567890")
	if code == "" {
		page, _ := io.ReadAll(res.Body)
		t.Fatalf("login: %d %s", res.StatusCode, page)
	}
	res.Body.Close()
	for _, form := range []url.Values{oauth2TestTokenForm(code, nil), oauth2TestTokenForm(code, map[string]string{"client_secret": ""})} {
		if form.Has("client_secret") {
			status, data := s.exchange(form, nil)
			if status != 401 {
				t.Fatalf("public secret: %d %v", status, data)
			}
			continue
		}
		status, data := s.exchange(form, nil)
		if status != 200 || data["access_token"] == nil || data["id_token"] != nil {
			t.Fatalf("public exchange: %d %v", status, data)
		}
	}
}

func TestOAuth2ServerPathInsertionDiscovery(t *testing.T) {
	s := newOAuth2TestServer(t)
	for _, prefix := range []string{"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration"} {
		res := s.do(http.MethodGet, prefix+"/api/collections/users/oauth2-server", nil, nil)
		var md map[string]any
		err := json.NewDecoder(res.Body).Decode(&md)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 || !strings.HasSuffix(md["issuer"].(string), "/api/collections/users/oauth2-server") {
			t.Fatalf("discovery: %d %v %v", res.StatusCode, md, err)
		}
	}
}

func TestOAuth2ServerRegistration(t *testing.T) {
	s := newOAuth2TestServer(t)
	users, _ := s.app.FindCollectionByNameOrId("users")
	register := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/collections/users/oauth2-server/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handler.ServeHTTP(rec, req)
		return rec
	}
	good := `{"client_name":"Test unverified","redirect_uris":["https://allowed.example/cb"],"token_endpoint_auth_method":"none"}`
	if rec := register(good); rec.Code != 404 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	users.OAuth2Server.Registration = core.OAuth2ServerRegistrationConfig{Enabled: true, AllowedRedirectHosts: []string{"ALLOWED.example"}, RateLimit: 100}
	users.OAuth2Server.SessionDuration = 3600
	users.OAuth2Server.RootDiscovery = true
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"redirect_uris":["http://allowed.example/cb"]}`, `{"redirect_uris":["https://other.example/cb"]}`, `{"redirect_uris":["https://allowed.example/cb"],"grant_types":["implicit"]}`, `{"redirect_uris":["https://allowed.example/cb"],"response_types":["token"]}`} {
		if rec := register(bad); rec.Code != 400 {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	rec := register(good)
	if rec.Code != 201 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var result map[string]any
	json.Unmarshal(rec.Body.Bytes(), &result)
	id := result["client_id"].(string)
	params := s.authorizeParams(map[string]string{"client_id": id, "redirect_uri": "https://allowed.example/cb"})
	page := s.do("GET", "/api/collections/users/oauth2-server/authorize?"+params.Encode(), nil, nil)
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != 200 || !strings.Contains(string(body), "unverified app") || !strings.Contains(string(body), "allowed.example") {
		t.Fatal(page.StatusCode, string(body))
	}
	login, code := s.login(params, "test@example.com", "1234567890")
	if code == "" {
		t.Fatal("login failed", login.StatusCode)
	}
	for _, cookie := range login.Cookies() {
		if cookie.Value != "" {
			params.Set("prompt", "none")
			res := s.do("GET", "/api/collections/users/oauth2-server/authorize?"+params.Encode(), nil, map[string]string{"Cookie": cookie.String()})
			if !strings.Contains(res.Header.Get("Location"), "interaction_required") {
				t.Fatal("silent DCR", res.StatusCode, res.Header)
			}
			res.Body.Close()
			params.Del("prompt")
			res = s.do("GET", "/api/collections/users/oauth2-server/authorize?"+params.Encode(), nil, map[string]string{"Cookie": cookie.String()})
			if res.StatusCode != 200 {
				t.Fatal("expected page", res.StatusCode)
			}
			res.Body.Close()
			// a same-site "continue" post must not authorize an unverified app from the session alone
			form := url.Values{}
			for k, v := range params {
				form[k] = v
			}
			form.Set("action", "continue")
			res = s.do(http.MethodPost, "/api/collections/users/oauth2-server/authorize", form, map[string]string{"Cookie": cookie.String()})
			if res.StatusCode == http.StatusSeeOther || strings.Contains(res.Header.Get("Location"), "code=") {
				t.Fatal("unverified app continued from the remembered session", res.StatusCode, res.Header)
			}
			res.Body.Close()
		}
	}
	client, err := s.app.FindFirstRecordByData("_oauth2ServerClients", "clientId", id)
	if err != nil {
		t.Fatal(err)
	}
	client.Set("revoked", true)
	if err := s.app.Save(client); err != nil {
		t.Fatal(err)
	}
	status, data := s.exchange(oauth2TestTokenForm(code, map[string]string{"client_id": id, "client_secret": "", "redirect_uri": "https://allowed.example/cb"}), nil)
	if status != 401 {
		t.Fatal(status, data)
	}
	page = s.do("GET", "/api/collections/users/oauth2-server/authorize?"+params.Encode(), nil, nil)
	if page.StatusCode != 400 {
		t.Fatal("revoked authorize", page.StatusCode)
	}
	page.Body.Close()
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration"} {
		res := s.do("GET", path, nil, nil)
		if res.StatusCode != 200 {
			t.Fatal(path, res.StatusCode)
		}
		res.Body.Close()
	}
	other := core.NewAuthCollection("otherroot")
	other.OAuth2Server.RootDiscovery = true
	if err := s.app.Save(other); err == nil {
		t.Fatal("duplicate root allowed")
	}
	users.OAuth2Server.Registration.RateLimit = 1
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}
	if rec := register(good); rec.Code != 429 {
		t.Fatal("rate limit", rec.Code)
	}
}

func TestOAuth2ServerHashMigration(t *testing.T) {
	s := newOAuth2TestServer(t)
	users, _ := s.app.FindCollectionByNameOrId("users")
	// Simulate an existing deployment by bypassing the save hook with raw options.
	var options map[string]any
	if err := json.Unmarshal(users.RawOptions, &options); err != nil {
		t.Fatal(err)
	}
	config := users.OAuth2Server
	config.Clients[0].Secret = oauth2TestClientSecret
	config.SigningKey = ""
	options["oauth2Server"] = config
	raw, _ := json.Marshal(options)
	if _, err := s.app.DB().NewQuery("UPDATE {{_collections}} SET [[options]]={:options} WHERE [[id]]={:id}").Bind(dbx.Params{"options": string(raw), "id": users.Id}).Execute(); err != nil {
		t.Fatal(err)
	}
	for _, migration := range core.SystemMigrations.Items() {
		if migration.File != "1794000007_oauth2_server_secrets.go" {
			continue
		}
		if err := migration.Up(s.app); err != nil {
			t.Fatal(err)
		}
		users, _ = s.app.FindCollectionByNameOrId("users")
		first := users.OAuth2Server.Clients[0].Secret
		if first == oauth2TestClientSecret || !users.OAuth2Server.Clients[0].ValidateSecret(oauth2TestClientSecret) {
			t.Fatal("not hashed")
		}
		if err := migration.Up(s.app); err != nil {
			t.Fatal(err)
		}
		users, _ = s.app.FindCollectionByNameOrId("users")
		if users.OAuth2Server.Clients[0].Secret != first {
			t.Fatal("not idempotent")
		}
		if err := s.app.ReloadCachedCollections(); err != nil {
			t.Fatal(err)
		}
		_, code := s.login(s.authorizeParams(nil), "test@example.com", "1234567890")
		status, data := s.exchange(oauth2TestTokenForm(code, nil), nil)
		if status != 200 || data["id_token"] == nil {
			t.Fatal(status, data)
		}
		return
	}
	t.Fatal("migration missing")
}
