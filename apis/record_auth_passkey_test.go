package apis_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

const (
	passkeyTestRPID   = "auth.example.com"
	passkeyTestOrigin = "https://auth.example.com"
)

var b64url = base64.RawURLEncoding

// softAuthenticator is a minimal WebAuthn authenticator used to drive the
// real server-side verification in tests.
type softAuthenticator struct {
	t            *testing.T
	key          *ecdsa.PrivateKey
	credentialId []byte
	userHandle   []byte
	signCount    uint32
	origin       string
}

func newSoftAuthenticator(t *testing.T) *softAuthenticator {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	id := make([]byte, 32)
	_, _ = rand.Read(id)

	return &softAuthenticator{t: t, key: key, credentialId: id, origin: passkeyTestOrigin}
}

func (a *softAuthenticator) clientData(kind string, challenge string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"type":        kind,
		"challenge":   challenge,
		"origin":      a.origin,
		"crossOrigin": false,
	})
	return raw
}

func (a *softAuthenticator) authData(flags byte, attested bool) []byte {
	rpHash := sha256.Sum256([]byte(passkeyTestRPID))

	data := append([]byte{}, rpHash[:]...)
	data = append(data, flags)
	counter := make([]byte, 4)
	binary.BigEndian.PutUint32(counter, a.signCount)
	data = append(data, counter...)

	if attested {
		data = append(data, make([]byte, 16)...) // aaguid
		idLen := make([]byte, 2)
		binary.BigEndian.PutUint16(idLen, uint16(len(a.credentialId)))
		data = append(data, idLen...)
		data = append(data, a.credentialId...)

		x := make([]byte, 32)
		y := make([]byte, 32)
		a.key.PublicKey.X.FillBytes(x)
		a.key.PublicKey.Y.FillBytes(y)
		coseKey, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
		if err != nil {
			a.t.Fatal(err)
		}
		data = append(data, coseKey...)
	}

	return data
}

// create answers a navigator.credentials.create() call.
func (a *softAuthenticator) create(options map[string]any) map[string]any {
	publicKey := options["publicKey"].(map[string]any)
	user := publicKey["user"].(map[string]any)

	handle, err := b64url.DecodeString(user["id"].(string))
	if err != nil {
		a.t.Fatal(err)
	}
	a.userHandle = handle

	attObj, err := cbor.Marshal(map[string]any{
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": a.authData(0x45, true), // UP | UV | AT
	})
	if err != nil {
		a.t.Fatal(err)
	}

	return map[string]any{
		"id":    b64url.EncodeToString(a.credentialId),
		"rawId": b64url.EncodeToString(a.credentialId),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64url.EncodeToString(a.clientData("webauthn.create", publicKey["challenge"].(string))),
			"attestationObject": b64url.EncodeToString(attObj),
		},
	}
}

// get answers a navigator.credentials.get() call (no allowCredentials needed).
func (a *softAuthenticator) get(options map[string]any) map[string]any {
	publicKey := options["publicKey"].(map[string]any)

	a.signCount++
	authData := a.authData(0x05, false) // UP | UV
	clientData := a.clientData("webauthn.get", publicKey["challenge"].(string))
	clientHash := sha256.Sum256(clientData)

	digest := sha256.Sum256(append(append([]byte{}, authData...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		a.t.Fatal(err)
	}

	return map[string]any{
		"id":    b64url.EncodeToString(a.credentialId),
		"rawId": b64url.EncodeToString(a.credentialId),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64url.EncodeToString(clientData),
			"authenticatorData": b64url.EncodeToString(authData),
			"signature":         b64url.EncodeToString(signature),
			"userHandle":        b64url.EncodeToString(a.userHandle),
		},
	}
}

// -------------------------------------------------------------------

func newPasskeyTestServer(t *testing.T) *oauth2TestServer {
	s := newOAuth2TestServer(t)

	s.app.Settings().Meta.AppURL = passkeyTestOrigin

	users, err := s.app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	users.Passkey.Enabled = true
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}

	return s
}

func (s *oauth2TestServer) jsonRequest(method, target string, body any, token string) (int, map[string]any) {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}

	req := httptestRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
	}

	res := serve(s.handler, req)
	defer res.Body.Close()

	data := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&data)

	return res.StatusCode, data
}

func (s *oauth2TestServer) userToken(email string) (*core.Record, string) {
	user, err := s.app.FindAuthRecordByEmail("users", email)
	if err != nil {
		s.t.Fatal(err)
	}
	token, err := user.NewAuthToken()
	if err != nil {
		s.t.Fatal(err)
	}
	return user, token
}

// registerPasskey registers a new passkey for the user through the api.
func (s *oauth2TestServer) registerPasskey(token string) *softAuthenticator {
	authenticator := newSoftAuthenticator(s.t)

	status, options := s.jsonRequest(http.MethodPost, "/api/collections/users/request-passkey-registration", nil, token)
	if status != 200 {
		s.t.Fatalf("request registration: %d %v", status, options)
	}

	status, data := s.jsonRequest(http.MethodPost, "/api/collections/users/confirm-passkey-registration", map[string]any{
		"sessionId":  options["sessionId"],
		"name":       "Test laptop",
		"credential": authenticator.create(options["options"].(map[string]any)),
	}, "")
	if status != 200 {
		s.t.Fatalf("confirm registration: %d %v", status, data)
	}

	return authenticator
}

func (s *oauth2TestServer) passkeyLogin(authenticator *softAuthenticator) (int, map[string]any) {
	status, options := s.jsonRequest(http.MethodPost, "/api/collections/users/request-passkey", nil, "")
	if status != 200 {
		s.t.Fatalf("request passkey: %d %v", status, options)
	}

	return s.jsonRequest(http.MethodPost, "/api/collections/users/auth-with-passkey", map[string]any{
		"sessionId":  options["sessionId"],
		"credential": authenticator.get(options["options"].(map[string]any)),
	}, "")
}

// -------------------------------------------------------------------

func TestPasskeyRegistrationAndUsernamelessLogin(t *testing.T) {
	t.Parallel()

	s := newPasskeyTestServer(t)

	user, token := s.userToken("test@example.com")

	// auth-methods advertises the passkey support
	status, methods := s.jsonRequest(http.MethodGet, "/api/collections/users/auth-methods", nil, "")
	if status != 200 || methods["passkey"].(map[string]any)["enabled"] != true {
		t.Fatalf("expected passkey in auth methods, got %v", methods)
	}

	// registration requires auth
	if status, _ := s.jsonRequest(http.MethodPost, "/api/collections/users/request-passkey-registration", nil, ""); status != 401 {
		t.Fatalf("expected 401, got %d", status)
	}

	authenticator := s.registerPasskey(token)

	// the login options don't ask for (or reveal) any account
	status, options := s.jsonRequest(http.MethodPost, "/api/collections/users/request-passkey", nil, "")
	publicKey := options["options"].(map[string]any)["publicKey"].(map[string]any)
	if status != 200 || publicKey["allowCredentials"] != nil || publicKey["userVerification"] != "required" {
		t.Fatalf("unexpected login options %v", options)
	}

	// usernameless login
	status, data := s.passkeyLogin(authenticator)
	if status != 200 {
		t.Fatalf("expected successful passkey login, got %d %v", status, data)
	}
	record := data["record"].(map[string]any)
	if record["id"] != user.Id || data["token"] == "" {
		t.Fatalf("unexpected auth response %v", data)
	}

	// the sign counter and last usage are persisted
	passkeys, err := s.app.FindAllPasskeysByRecord(user)
	if err != nil || len(passkeys) != 1 {
		t.Fatalf("expected 1 passkey, got %d (%v)", len(passkeys), err)
	}
	if passkeys[0].LastUsed().IsZero() || passkeys[0].Name() != "Test laptop" {
		t.Fatalf("unexpected passkey state %v", passkeys[0].Record)
	}

	// the owner can list their passkeys (without the credential data)
	status, list := s.jsonRequest(http.MethodGet, "/api/collections/_passkeys/records", nil, token)
	items, _ := list["items"].([]any)
	if status != 200 || len(items) != 1 {
		t.Fatalf("expected 1 listed passkey, got %d %v", status, list)
	}
	if _, ok := items[0].(map[string]any)["credential"]; ok {
		t.Fatal("the credential data must be hidden")
	}

	// other users can't see them
	_, otherToken := s.userToken("test2@example.com")
	_, list = s.jsonRequest(http.MethodGet, "/api/collections/_passkeys/records", nil, otherToken)
	if items, _ := list["items"].([]any); len(items) != 0 {
		t.Fatalf("expected no passkeys for another user, got %v", list)
	}

	// the owner can delete them, after which the login fails
	passkeyId := items[0].(map[string]any)["id"].(string)
	if status, _ := s.jsonRequest(http.MethodDelete, "/api/collections/_passkeys/records/"+passkeyId, nil, token); status != 204 {
		t.Fatalf("expected 204 delete, got %d", status)
	}
	if status, _ := s.passkeyLogin(authenticator); status != 400 {
		t.Fatalf("expected login with deleted passkey to fail, got %d", status)
	}
}

func TestPasskeyLoginRejections(t *testing.T) {
	t.Parallel()

	s := newPasskeyTestServer(t)
	_, token := s.userToken("test@example.com")
	authenticator := s.registerPasskey(token)

	t.Run("replayed session", func(t *testing.T) {
		_, options := s.jsonRequest(http.MethodPost, "/api/collections/users/request-passkey", nil, "")
		body := map[string]any{
			"sessionId":  options["sessionId"],
			"credential": authenticator.get(options["options"].(map[string]any)),
		}
		if status, data := s.jsonRequest(http.MethodPost, "/api/collections/users/auth-with-passkey", body, ""); status != 200 {
			t.Fatalf("expected first use to work, got %d %v", status, data)
		}
		if status, _ := s.jsonRequest(http.MethodPost, "/api/collections/users/auth-with-passkey", body, ""); status != 400 {
			t.Fatalf("expected replay to fail, got %d", status)
		}
	})

	t.Run("wrong origin", func(t *testing.T) {
		authenticator.origin = "https://evil.example.com"
		defer func() { authenticator.origin = passkeyTestOrigin }()

		if status, _ := s.passkeyLogin(authenticator); status != 400 {
			t.Fatalf("expected wrong origin to fail, got %d", status)
		}
	})

	t.Run("user handle of another record", func(t *testing.T) {
		original := authenticator.userHandle
		other, _ := s.userToken("test2@example.com")
		authenticator.userHandle = []byte(other.Id)
		defer func() { authenticator.userHandle = original }()

		if status, _ := s.passkeyLogin(authenticator); status != 400 {
			t.Fatalf("expected mismatched user handle to fail, got %d", status)
		}
	})

	t.Run("unknown credential", func(t *testing.T) {
		if status, _ := s.passkeyLogin(newSoftAuthenticatorWithHandle(t, authenticator.userHandle)); status != 400 {
			t.Fatalf("expected unknown credential to fail, got %d", status)
		}
	})

	t.Run("cloned authenticator (sign counter going back)", func(t *testing.T) {
		authenticator.signCount = 0
		if status, _ := s.passkeyLogin(authenticator); status != 400 {
			t.Fatalf("expected a lower sign counter to fail, got %d", status)
		}
	})

	t.Run("disabled collection", func(t *testing.T) {
		if status, _ := s.jsonRequest(http.MethodPost, "/api/collections/clients/request-passkey", nil, ""); status != 403 {
			t.Fatalf("expected 403, got %d", status)
		}
	})
}

func TestPasskeyRegistrationRejections(t *testing.T) {
	t.Parallel()

	s := newPasskeyTestServer(t)
	_, token := s.userToken("test@example.com")
	_, otherToken := s.userToken("test2@example.com")

	authenticator := newSoftAuthenticator(t)
	_, options := s.jsonRequest(http.MethodPost, "/api/collections/users/request-passkey-registration", nil, token)
	body := map[string]any{
		"sessionId":  options["sessionId"],
		"credential": authenticator.create(options["options"].(map[string]any)),
	}

	// another user can't complete someone else's registration
	if status, _ := s.jsonRequest(http.MethodPost, "/api/collections/users/confirm-passkey-registration", body, otherToken); status != 400 {
		t.Fatalf("expected 400, got %d", status)
	}

	// and the session was consumed by the failed attempt
	if status, _ := s.jsonRequest(http.MethodPost, "/api/collections/users/confirm-passkey-registration", body, token); status != 400 {
		t.Fatalf("expected consumed session to fail, got %d", status)
	}

	// the same credential can't be registered twice
	s.registerPasskeyWith(token, authenticator)
	_, options = s.jsonRequest(http.MethodPost, "/api/collections/users/request-passkey-registration", nil, token)
	excluded := options["options"].(map[string]any)["publicKey"].(map[string]any)["excludeCredentials"].([]any)
	if len(excluded) != 1 {
		t.Fatalf("expected the existing passkey to be excluded, got %v", excluded)
	}
	status, _ := s.jsonRequest(http.MethodPost, "/api/collections/users/confirm-passkey-registration", map[string]any{
		"sessionId":  options["sessionId"],
		"credential": authenticator.create(options["options"].(map[string]any)),
	}, token)
	if status != 400 {
		t.Fatalf("expected duplicate credential to fail, got %d", status)
	}
}

func TestOAuth2ServerPasskeySignIn(t *testing.T) {
	t.Parallel()

	s := newPasskeyTestServer(t)
	params := s.authorizeParams(nil)

	// the login page offers the passkey sign-in
	res := s.do(http.MethodGet, "/api/collections/users/oauth2-server/authorize?"+params.Encode(), nil, nil)
	page, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(page), "Sign in with a passkey") {
		t.Fatalf("expected passkey button on the login page")
	}
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'nonce-") {
		t.Fatalf("expected nonce based script policy, got %q", csp)
	}

	// password sign-in of a user without passkeys shows the "add a passkey" step
	form := url.Values{}
	for k, v := range params {
		form[k] = v
	}
	form.Set("identity", "test@example.com")
	form.Set("password", "1234567890")
	res = s.do(http.MethodPost, "/api/collections/users/oauth2-server/authorize", form, nil)
	page, _ = io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(page), "Add a passkey") {
		t.Fatalf("expected the add passkey step, got %d: %s", res.StatusCode, page)
	}

	continueMatch := regexp.MustCompile(`id="pk-skip" href="([^"]+)"`).FindSubmatch(page)
	if continueMatch == nil || !strings.Contains(string(continueMatch[1]), "code=") {
		t.Fatalf("expected a continue link with the code")
	}

	// complete the offered registration without an auth token (bound to the session)
	sessionMatch := regexp.MustCompile(`"sessionId":"([a-z0-9]+)"`).FindSubmatch(page)
	if sessionMatch == nil {
		t.Fatalf("missing registration session in the page")
	}
	optionsMatch := regexp.MustCompile(`"options":(\{.*\})\};`).FindSubmatch(page)
	if optionsMatch == nil {
		t.Fatalf("missing registration options in the page")
	}
	registrationOptions := map[string]any{}
	if err := json.Unmarshal(optionsMatch[1], &registrationOptions); err != nil {
		t.Fatalf("invalid embedded options: %v", err)
	}

	authenticator := newSoftAuthenticator(t)
	status, data := s.jsonRequest(http.MethodPost, "/api/collections/users/confirm-passkey-registration", map[string]any{
		"sessionId":  string(sessionMatch[1]),
		"credential": authenticator.create(registrationOptions),
	}, "")
	if status != 200 {
		t.Fatalf("expected registration from the hosted page to work, got %d %v", status, data)
	}

	// now sign in through the hosted page with the passkey only
	_, options := s.jsonRequest(http.MethodPost, "/api/collections/users/request-passkey", nil, "")
	credential, _ := json.Marshal(authenticator.get(options["options"].(map[string]any)))

	form = url.Values{}
	for k, v := range params {
		form[k] = v
	}
	form.Set("passkey_session", options["sessionId"].(string))
	form.Set("passkey_credential", string(credential))
	res, code := s.login2(form)
	if code == "" {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected code redirect after passkey sign-in, got %d: %s", res.StatusCode, body)
	}

	status, tokenData := s.exchange(oauth2TestTokenForm(code, nil), nil)
	if status != 200 || tokenData["access_token"] == "" {
		t.Fatalf("expected token exchange to work, got %d %v", status, tokenData)
	}

	// users with a passkey are not asked again after a password sign-in
	form.Del("passkey_session")
	form.Del("passkey_credential")
	form.Set("identity", "test@example.com")
	form.Set("password", "1234567890")
	if _, code := s.login2(form); code == "" {
		t.Fatal("expected a direct redirect for users that already have a passkey")
	}
}

func TestPasskeyConfigValidation(t *testing.T) {
	t.Parallel()

	app, err := tests.NewTestApp(t)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	superusers, _ := app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	superusers.Passkey.Enabled = true
	if err := app.Save(superusers); err == nil {
		t.Fatal("expected superusers to reject passkeys")
	}

	users, _ := app.FindCollectionByNameOrId("users")
	users.Passkey = core.PasskeyConfig{Enabled: true, RPID: "not a domain"}
	if err := app.Save(users); err == nil {
		t.Fatal("expected invalid rpId to fail")
	}

	users.Passkey = core.PasskeyConfig{Enabled: true, RPID: "localhost", Origins: []string{"http://example.com"}}
	if err := app.Save(users); err == nil {
		t.Fatal("expected insecure origin to fail")
	}

	users.Passkey = core.PasskeyConfig{Enabled: true, RPID: "example.com", Origins: []string{"https://app.example.com", "http://localhost:5173"}}
	if err := app.Save(users); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

// -------------------------------------------------------------------

func httptestRequest(method, target string, body io.Reader) *http.Request {
	return httptest.NewRequest(method, target, body)
}

func serve(handler http.Handler, req *http.Request) *http.Response {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Result()
}

func newSoftAuthenticatorWithHandle(t *testing.T, userHandle []byte) *softAuthenticator {
	a := newSoftAuthenticator(t)
	a.userHandle = userHandle
	return a
}

func (s *oauth2TestServer) registerPasskeyWith(token string, authenticator *softAuthenticator) {
	status, options := s.jsonRequest(http.MethodPost, "/api/collections/users/request-passkey-registration", nil, token)
	if status != 200 {
		s.t.Fatalf("request registration: %d %v", status, options)
	}

	status, data := s.jsonRequest(http.MethodPost, "/api/collections/users/confirm-passkey-registration", map[string]any{
		"sessionId":  options["sessionId"],
		"credential": authenticator.create(options["options"].(map[string]any)),
	}, token)
	if status != 200 {
		s.t.Fatalf("confirm registration: %d %v", status, data)
	}
}

// login2 submits the full hosted sign-in form and returns the issued code (if any).
func (s *oauth2TestServer) login2(form url.Values) (*http.Response, string) {
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
