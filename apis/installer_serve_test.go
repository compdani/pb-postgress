package apis_test

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// ServeInstaller must return after setup so the caller starts apis.Serve
// (cron, hooks) instead of keeping the installer listener as the live API.
func TestServeInstallerHandsOffAfterSetup(t *testing.T) {
	cfg := tests.NewDatabaseConfig(t)
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: cfg.DataDir})
	t.Cleanup(func() { app.ResetBootstrapState() })

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	result := make(chan error, 1)
	go func() { result <- apis.ServeInstaller(app, addr) }()

	post := func(path string, token string, body any) int {
		t.Helper()
		raw, _ := json.Marshal(body)
		var res *http.Response
		deadline := time.Now().Add(5 * time.Second)
		for {
			req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/installer/"+path, bytes.NewReader(raw))
			req.Header.Set("X-Installer-Token", token)
			res, err = http.DefaultClient.Do(req)
			if err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	if code := post("connect", "wrong", map[string]string{"driver": cfg.DBDriver, "dsn": cfg.DBDSN}); code != 401 {
		t.Fatalf("connect without token: %d", code)
	}
	if code := post("connect", apis.InstallerToken(), map[string]string{"driver": cfg.DBDriver, "dsn": cfg.DBDSN}); code != 200 {
		t.Fatalf("connect: %d", code)
	}

	// The regular API is not served by the installer listener.
	res, err := http.Get("http://" + addr + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("installer served the regular API: %d", res.StatusCode)
	}

	if code := post("complete", apis.InstallerToken(), map[string]string{"email": "admin@example.com", "password": "1234567890"}); code != 200 {
		t.Fatalf("complete: %d", code)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ServeInstaller: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeInstaller did not return after setup")
	}
}
