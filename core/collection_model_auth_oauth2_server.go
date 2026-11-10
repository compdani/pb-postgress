package core

import (
	"crypto/subtle"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	validation "github.com/pocketbase/ozzo-validation/v4"
)

// DefaultOAuth2ServerCodeDuration is the authorization code lifetime used
// when [OAuth2ServerConfig.CodeDuration] is not set (eg. older collections).
const DefaultOAuth2ServerCodeDuration int64 = 120

var oauth2ServerClientIdRegex = regexp.MustCompile(`^[\w\-\.]+$`)

// OAuth2ServerConfig defines the options for using an auth collection
// as an OAuth2 authorization server (authorization code grant).
//
// The issued access token is the regular collection auth token, so it is
// accepted by any app instance that shares the same database.
type OAuth2ServerConfig struct {
	Enabled bool `form:"enabled" json:"enabled"`

	// CodeDuration specifies how long an issued authorization code is valid (in seconds).
	CodeDuration int64 `form:"codeDuration" json:"codeDuration"`

	// SessionDuration specifies how long the hosted sign-in page remembers
	// a signed in user (in seconds), so that the next authorization request
	// from any client completes without asking again (single sign-on).
	//
	// Set it to 0 to always ask for the credentials.
	SessionDuration int64 `form:"sessionDuration" json:"sessionDuration"`

	// Clients lists the applications allowed to request authorization.
	Clients []OAuth2ServerClient `form:"clients" json:"clients"`

	// Popup configures the popup sign-in for the collection own frontends,
	// which needs no registered client or secret (see [OAuth2ServerPopupConfig]).
	Popup OAuth2ServerPopupConfig `form:"popup" json:"popup"`
}

// Validate makes OAuth2ServerConfig validatable by implementing [validation.Validatable] interface.
func (c OAuth2ServerConfig) Validate() error {
	if !c.Enabled {
		return nil // no need to validate
	}

	return validation.ValidateStruct(&c,
		validation.Field(&c.CodeDuration, validation.Required, validation.Min(10), validation.Max(600)),
		validation.Field(&c.SessionDuration, validation.When(c.SessionDuration != 0, validation.Min(int64(60)), validation.Max(int64(31536000)))), // 1min - 1y
		// the popup sign-in doesn't need a registered client
		validation.Field(&c.Clients, validation.When(!c.Popup.Enabled, validation.Required), validation.By(checkOAuth2ServerClients)),
		validation.Field(&c.Popup),
	)
}

// OAuth2ServerPopupConfig defines the options of the popup sign-in.
//
// A frontend opens the hosted sign-in page in a popup and receives a
// single use code with window.postMessage, which it exchanges for the
// regular auth token (PKCE is required, there is no client secret).
type OAuth2ServerPopupConfig struct {
	Enabled bool `form:"enabled" json:"enabled"`

	// Origins lists the additional frontend origins allowed to use the
	// popup sign-in (eg. "https://app.example.com").
	//
	// The origin of the application URL setting (and the local server on
	// loopback) is always allowed; every other frontend origin must be listed.
	Origins []string `form:"origins" json:"origins"`
}

// Validate makes OAuth2ServerPopupConfig validatable by implementing [validation.Validatable] interface.
func (c OAuth2ServerPopupConfig) Validate() error {
	if !c.Enabled {
		return nil // no need to validate
	}

	return validation.ValidateStruct(&c,
		validation.Field(&c.Origins, validation.Each(validation.By(checkOAuth2ServerPopupOrigin))),
	)
}

// SessionDurationTime returns the remembered sign-in lifetime as [time.Duration].
func (c OAuth2ServerConfig) SessionDurationTime() time.Duration {
	return time.Duration(c.SessionDuration) * time.Second
}

// CodeDurationTime returns the authorization code lifetime as [time.Duration].
func (c OAuth2ServerConfig) CodeDurationTime() time.Duration {
	if c.CodeDuration <= 0 {
		return time.Duration(DefaultOAuth2ServerCodeDuration) * time.Second
	}
	return time.Duration(c.CodeDuration) * time.Second
}

// GetClient returns the client with the specified id (if any).
func (c OAuth2ServerConfig) GetClient(id string) (OAuth2ServerClient, bool) {
	if id == "" {
		return OAuth2ServerClient{}, false
	}

	for _, client := range c.Clients {
		if client.Id == id {
			return client, true
		}
	}

	return OAuth2ServerClient{}, false
}

func checkOAuth2ServerClients(value any) error {
	clients, _ := value.([]OAuth2ServerClient)

	existing := map[string]struct{}{}
	for i, client := range clients {
		if _, ok := existing[client.Id]; ok {
			return validation.Errors{
				strconv.Itoa(i): validation.Errors{
					"id": validation.NewError("validation_oauth2_server_client_exists", "Client id {{.id}} is used more than once.").
						SetParams(map[string]any{"id": client.Id}),
				},
			}
		}
		existing[client.Id] = struct{}{}
	}

	return nil
}

// checkOAuth2ServerPopupOrigin accepts only serialized origins (scheme://host[:port], lowercase,
// no default port, userinfo, path, query or fragment), because they are compared as is
// with the Origin header and used as the postMessage target.
func checkOAuth2ServerPopupOrigin(value any) error {
	raw, _ := value.(string)

	invalid := validation.NewError("validation_invalid_origin", "Must be an origin like https://app.example.com (lowercase, no path, no default port).")

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Path != "" || u.Opaque != "" {
		return invalid
	}

	if u.Scheme != "https" && u.Scheme != "http" {
		return invalid
	}

	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return validation.NewError("validation_insecure_origin", "Plain http is allowed only for localhost.")
	}

	port := u.Port()
	if strings.HasSuffix(u.Host, ":") {
		return invalid // empty explicit port
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return invalid
		}
		if (u.Scheme == "https" && n == 443) || (u.Scheme == "http" && n == 80) {
			return invalid
		}
	}
	if raw != u.Scheme+"://"+strings.ToLower(u.Host) {
		return invalid
	}

	return nil
}

// OAuth2ServerClient defines a single client application of the collection OAuth2 server.
type OAuth2ServerClient struct {
	// Id is the public client identifier (the "client_id" parameter).
	Id string `form:"id" json:"id"`

	// Name is a human readable name shown on the hosted login page.
	Name string `form:"name" json:"name"`

	// Secret is the client secret used to authenticate the client at the token endpoint
	// and to sign the OpenID Connect id_token (HS256).
	Secret string `form:"secret" json:"secret,omitempty"`

	// RedirectURIs lists the exact redirect URIs allowed for the client.
	RedirectURIs []string `form:"redirectURIs" json:"redirectURIs"`
}

// Validate makes OAuth2ServerClient validatable by implementing [validation.Validatable] interface.
func (c OAuth2ServerClient) Validate() error {
	return validation.ValidateStruct(&c,
		validation.Field(&c.Id, validation.Required, validation.Length(3, 100), validation.Match(oauth2ServerClientIdRegex)),
		validation.Field(&c.Name, validation.Length(0, 255)),
		validation.Field(&c.Secret, validation.Required, validation.Length(30, 255)),
		validation.Field(&c.RedirectURIs, validation.Required, validation.Each(validation.By(checkOAuth2ServerRedirectURI))),
	)
}

// HasRedirectURI reports whether uri exactly matches one of the client redirect URIs.
func (c OAuth2ServerClient) HasRedirectURI(uri string) bool {
	return uri != "" && slices.Contains(c.RedirectURIs, uri)
}

// ValidateSecret checks the provided secret in constant time.
func (c OAuth2ServerClient) ValidateSecret(secret string) bool {
	if c.Secret == "" || secret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Secret), []byte(secret)) == 1
}

func checkOAuth2ServerRedirectURI(value any) error {
	raw, _ := value.(string)
	if raw == "" {
		return validation.ErrRequired
	}

	if len(raw) > 2048 {
		return validation.NewError("validation_redirect_uri_too_long", "Must be at most 2048 characters.")
	}

	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || (u.Host == "" && u.Opaque == "" && u.Path == "") {
		return validation.NewError("validation_invalid_redirect_uri", "Must be an absolute URI.")
	}

	if u.Fragment != "" || u.RawFragment != "" {
		return validation.NewError("validation_invalid_redirect_uri", "Must not contain a fragment.")
	}

	if (u.Scheme == "http" || u.Scheme == "https") && u.Host == "" {
		return validation.NewError("validation_invalid_redirect_uri", "Must include a host.")
	}

	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return validation.NewError("validation_insecure_redirect_uri", "Plain http is allowed only for localhost; use https.")
	}

	return nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// RedactedOAuth2ServerClients returns a copy of the clients without their secrets.
func RedactedOAuth2ServerClients(clients []OAuth2ServerClient) []OAuth2ServerClient {
	if clients == nil {
		return []OAuth2ServerClient{}
	}

	result := make([]OAuth2ServerClient, len(clients))
	for i, client := range clients {
		client.RedirectURIs = slices.Clone(client.RedirectURIs)
		client.Secret = ""
		result[i] = client
	}

	return result
}

// RestoreSecrets fills every client with an empty secret with the secret
// of the original client that has the same id.
//
// It is used after binding a request body because the serialized
// collection never exposes the client secrets.
func (c *OAuth2ServerConfig) RestoreSecrets(original []OAuth2ServerClient) {
	for i := range c.Clients {
		if c.Clients[i].Secret != "" {
			continue
		}

		if existing, ok := (OAuth2ServerConfig{Clients: original}).GetClient(c.Clients[i].Id); ok {
			c.Clients[i].Secret = existing.Secret
		}
	}
}
