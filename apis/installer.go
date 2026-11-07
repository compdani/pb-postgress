package apis

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

var installerMutex sync.Mutex

func installerStatus(app core.App) (map[string]any, error) {
	status := map[string]any{"dbConnected": app.IsBootstrapped(), "installed": false, "fileServer": nil, "fileMode": ""}
	if !app.IsBootstrapped() {
		return status, nil
	}
	count, err := app.CountRecords(core.CollectionNameSuperusers)
	if err != nil {
		return nil, err
	}
	status["installed"] = count > 0
	if ba := core.AsBaseApp(app); ba != nil {
		host, err := ba.FileHost()
		if err != nil {
			return nil, err
		}
		status["fileServer"] = host
		mode, err := ba.FileMode()
		if err != nil {
			return nil, err
		}
		status["fileMode"] = mode
	}
	return status, nil
}

func installerHandler(app core.App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		installerMutex.Lock()
		defer installerMutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fail := func(code int, err error) {
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		}
		if r.URL.Path == "/api/installer/status" && r.Method == "GET" {
			s, e := installerStatus(app)
			if e != nil {
				fail(500, errors.New("database status unavailable"))
				return
			}
			json.NewEncoder(w).Encode(s)
			return
		}
		if r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		status, err := installerStatus(app)
		if err != nil {
			fail(500, errors.New("database status unavailable"))
			return
		}
		if status["installed"] == true {
			fail(403, errors.New("installation already complete"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		switch r.URL.Path {
		case "/api/installer/test-db", "/api/installer/connect":
			if app.IsBootstrapped() && r.URL.Path == "/api/installer/connect" {
				fail(409, errors.New("database already connected"))
				return
			}
			var c core.DatabaseConfig
			if err = json.NewDecoder(r.Body).Decode(&c); err != nil {
				fail(400, errors.New("invalid request body"))
				return
			}
			db, err := core.ConnectDatabase(c.Driver, c.DSN)
			if err != nil {
				fail(400, err)
				return
			}
			db.Close()
			if r.URL.Path == "/api/installer/connect" {
				if err = core.SaveDatabaseConfig(app.DataDir(), c); err == nil {
					err = app.Bootstrap()
				}
				if err != nil {
					fail(500, errors.New("database initialization failed"))
					return
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/api/installer/test-s3":
			var s3 core.S3Config
			if err = json.NewDecoder(r.Body).Decode(&s3); err != nil {
				fail(400, errors.New("invalid S3 configuration"))
				return
			}
			if err = core.TestS3Connection(s3); err != nil {
				fail(400, err)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/api/installer/complete":
			if !app.IsBootstrapped() {
				fail(409, errors.New("connect a database first"))
				return
			}
			var data struct {
				FileMode      string              `json:"fileMode"`
				S3            core.S3Config       `json:"s3"`
				Scope         *core.InstanceScope `json:"scope"`
				Email         string              `json:"email"`
				Password      string              `json:"password"`
				IsFileServer  bool                `json:"isFileServer"`
				FileServerURL string              `json:"fileServerUrl"`
			}
			if err = json.NewDecoder(r.Body).Decode(&data); err != nil {
				fail(400, errors.New("invalid request body"))
				return
			}
			err = app.RunInTransaction(func(tx core.App) error {
				c, err := tx.FindCollectionByNameOrId(core.CollectionNameSuperusers)
				if err != nil {
					return err
				}
				record := core.NewRecord(c)
				record.SetEmail(data.Email)
				record.SetPassword(data.Password)
				if err = tx.Save(record); err != nil {
					return err
				}
				ba := core.AsBaseApp(tx)
				if data.FileMode == "" {
					data.FileMode = "local"
					if !data.IsFileServer && data.FileServerURL != "" {
						data.FileMode = "fileServer"
					}
				}
				if err := ba.SetFileMode(data.FileMode); err != nil {
					return err
				}
				if data.Scope != nil {
					if err := ba.SetInstanceScope(*data.Scope); err != nil {
						return err
					}
				}
				switch data.FileMode {
				case "s3":
					settings, err := tx.Settings().Clone()
					if err != nil {
						return err
					}
					settings.S3 = data.S3
					settings.S3.Enabled = true
					settings.S3.Scope = "all"
					if err := tx.Save(settings); err != nil {
						return err
					}
					endpoint := data.S3.Endpoint
					if endpoint == "" {
						endpoint = "https://s3.amazonaws.com"
					}
					return ba.SetFileHost(core.FileHost{InstanceID: "s3", URL: endpoint + "/" + data.S3.Bucket, Mode: "s3"})
				case "fileServer":
					host, err := ba.FileHost()
					if err != nil {
						return err
					}
					if host == nil {
						return errors.New("register the remote file host before completing installation")
					}
					if data.FileServerURL != "" && host.URL != data.FileServerURL {
						return errors.New("fileServerUrl does not match registered file host")
					}
				case "local":
					if data.IsFileServer {
						id, err := ba.InstanceID()
						if err != nil {
							return err
						}
						return ba.SetFileHost(core.FileHost{InstanceID: id, URL: data.FileServerURL, Mode: "full"})
					}
				}

				return nil
			})
			if err != nil {
				fail(400, err)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	})
}

func bindInstallerAPI(app core.App, rg *router.RouterGroup[*core.RequestEvent]) {
	h := installerHandler(app)
	for _, path := range []string{"test-db", "test-s3", "connect", "complete"} {
		rg.POST("/installer/"+path, WrapStdHandler(h))
	}
	rg.GET("/installer/status", WrapStdHandler(h))
}

// ServeInstaller runs the API before a database is configured. After completion,
// the full API and dashboard are available after connecting.
func ServeInstaller(app core.App, addr string) error {
	installer := installerHandler(app)
	var mux http.Handler
	var mu sync.Mutex
	return http.ListenAndServe(addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/installer/") {
			installer.ServeHTTP(w, r)
			return
		}
		mu.Lock()
		if mux == nil && app.IsBootstrapped() {
			router, err := NewRouter(app)
			if err == nil {
				mux, err = router.BuildMux()
			}
			if err != nil {
				mu.Unlock()
				http.Error(w, "API initialization failed", 500)
				return
			}
		}
		handler := mux
		mu.Unlock()
		if handler == nil {
			http.NotFound(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	}))
}
