package apis

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

func findOAuth2ServerClient(app core.App, col *core.Collection, id string) (core.OAuth2ServerClient, bool) {
	if !strings.HasPrefix(id, "dcr_") {
		return col.OAuth2Server.GetClient(id)
	}
	rows, err := app.FindRecordsByFilter("_oauth2ServerClients", "collectionRef={:collection} && clientId={:client} && revoked=false", "", 1, 0, dbx.Params{"collection": col.Id, "client": id})
	if err != nil || len(rows) != 1 {
		return core.OAuth2ServerClient{}, false
	}
	r := rows[0]
	var redirects []string
	if err := r.UnmarshalJSONField("redirectURIs", &redirects); err != nil {
		return core.OAuth2ServerClient{}, false
	}
	return core.OAuth2ServerClient{Id: id, Name: r.GetString("name"), RedirectURIs: redirects, Public: true, SelfRegistered: true}, true
}

type oauth2RegistrationWindow struct {
	hour  int64
	count int
}

var oauth2RegistrationLimits = struct {
	sync.Mutex
	windows map[string]oauth2RegistrationWindow
}{windows: map[string]oauth2RegistrationWindow{}}

func oauth2ServerRegister(e *core.RequestEvent) error {
	col, err := findOAuth2ServerCollection(e)
	if err != nil {
		return err
	}
	cfg := col.OAuth2Server.Registration
	if !cfg.Enabled {
		return e.NotFoundError("Client registration is disabled.", nil)
	}
	// RealIP honors the configured trusted proxy headers, so registrations
	// behind a reverse proxy are limited per client rather than per proxy
	key := fmt.Sprintf("%p:%s:%s", e.App, col.Id, e.RealIP())
	hour := time.Now().Unix() / 3600
	limit := cfg.RateLimit
	if limit <= 0 {
		limit = 10
	}
	oauth2RegistrationLimits.Lock()
	for k, v := range oauth2RegistrationLimits.windows {
		if v.hour != hour {
			delete(oauth2RegistrationLimits.windows, k)
		}
	}
	w := oauth2RegistrationLimits.windows[key]
	if w.hour != hour {
		w = oauth2RegistrationWindow{hour: hour}
	}
	if w.count >= limit {
		oauth2RegistrationLimits.Unlock()
		return e.JSON(429, map[string]string{"error": "rate_limit_exceeded"})
	}
	w.count++
	oauth2RegistrationLimits.windows[key] = w
	oauth2RegistrationLimits.Unlock()
	var body struct {
		Name      string   `json:"client_name"`
		Redirects []string `json:"redirect_uris"`
		Grants    []string `json:"grant_types"`
		Responses []string `json:"response_types"`
		Method    string   `json:"token_endpoint_auth_method"`
	}
	invalid := func() error { return e.JSON(400, map[string]string{"error": "invalid_client_metadata"}) }
	if err := e.BindBody(&body); err != nil {
		return invalid()
	}
	if len(body.Name) > 255 || len(body.Redirects) == 0 || len(body.Redirects) > 20 || (body.Method != "" && body.Method != "none") {
		return invalid()
	}
	for _, g := range body.Grants {
		if g != "authorization_code" {
			return invalid()
		}
	}
	for _, r := range body.Responses {
		if r != "code" {
			return invalid()
		}
	}
	for _, raw := range body.Redirects {
		u, err := url.Parse(raw)
		if err != nil || len(raw) > 2048 || u.Host == "" || u.User != nil || strings.Contains(raw, "#") || u.Opaque != "" {
			return invalid()
		}
		ip := net.ParseIP(u.Hostname())
		loop := strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())
		if u.Scheme != "https" && !(u.Scheme == "http" && loop) {
			return invalid()
		}
		if !loop && len(cfg.AllowedRedirectHosts) > 0 {
			allowed := false
			for _, h := range cfg.AllowedRedirectHosts {
				if strings.EqualFold(h, u.Hostname()) {
					allowed = true
				}
			}
			if !allowed {
				return invalid()
			}
		}
	}
	storage, err := e.App.FindCollectionByNameOrId("_oauth2ServerClients")
	if err != nil {
		return e.InternalServerError("Registration storage unavailable.", err)
	}
	id := "dcr_" + security.RandomString(40)
	r := core.NewRecord(storage)
	r.Set("collectionRef", col.Id)
	r.Set("clientId", id)
	r.Set("name", body.Name)
	r.Set("redirectURIs", body.Redirects)
	if err := e.App.Save(r); err != nil {
		return e.InternalServerError("Registration failed.", err)
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	return e.JSON(http.StatusCreated, map[string]any{"client_id": id, "client_name": body.Name, "redirect_uris": body.Redirects, "grant_types": []string{"authorization_code"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none"})
}

func oauth2ServerRootDiscovery(e *core.RequestEvent) error {
	cols, err := e.App.FindAllCollections()
	if err != nil {
		return err
	}
	for _, col := range cols {
		if col.IsAuth() && col.OAuth2Server.Enabled && col.OAuth2Server.RootDiscovery {
			e.Request.SetPathValue("collection", col.Id)
			return oauth2ServerDiscovery(e)
		}
	}
	return e.NotFoundError("Root discovery is disabled.", nil)
}
