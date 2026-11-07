package apis

import (
	"encoding/json"
	"github.com/pocketbase/pocketbase/core"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInstallerDisconnectedContract(t *testing.T) {
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	handler := installerHandler(app, nil)
	r := httptest.NewRequest("GET", "/api/installer/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var status map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || status["dbConnected"] != false || status["installed"] != false {
		t.Fatal(w.Body.String())
	}
	if _, ok := status["fileServer"]; ok {
		t.Fatal("anonymous status exposed file host details")
	}

	// Every mutation needs the setup token.
	for _, path := range []string{"test-db", "test-s3", "connect", "complete"} {
		for _, token := range []string{"", "wrong"} {
			r = httptest.NewRequest("POST", "/api/installer/"+path, strings.NewReader(`{}`))
			if token != "" {
				r.Header.Set("X-Installer-Token", token)
			}
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatalf("%s with token %q: status %d", path, token, w.Code)
			}
		}
	}

	for _, body := range []string{`{"driver":"sqlite","dsn":"secret"}`, `{"driver":"mysql","dsn":"not-a-valid-dsn-secret"}`} {
		r = httptest.NewRequest("POST", "/api/installer/test-db", strings.NewReader(body))
		r.Header.Set("X-Installer-Token", InstallerToken())
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
		if strings.Contains(w.Body.String(), "secret") {
			t.Fatal("credential leaked")
		}
	}
	r = httptest.NewRequest("POST", "/api/installer/complete", strings.NewReader(`{"email":"admin@example.com","password":"password"}`))
	r.Header.Set("X-Installer-Token", InstallerToken())
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
}
