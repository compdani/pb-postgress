package apis

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/pocketbase/pocketbase/ui"
)

var installerMutex sync.Mutex

var (
	installerTokenOnce  sync.Once
	installerTokenValue string
)

// InstallerToken is the one-time setup secret for this process. Installer
// mutations require it in the X-Installer-Token header. It is printed with the
// installer URL and never stored.
func InstallerToken() string {
	installerTokenOnce.Do(func() {
		installerTokenValue = security.RandomString(48)
	})
	return installerTokenValue
}

func installerAuthorized(r *http.Request) bool {
	got := r.Header.Get("X-Installer-Token")
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(InstallerToken())) == 1
}

// installerStatus reports setup progress. File host details are only
// included for a caller holding the setup token.
func installerStatus(app core.App, detailed bool) (map[string]any, error) {
	status := map[string]any{"dbConnected": app.IsBootstrapped(), "installed": false}
	if !app.IsBootstrapped() {
		return status, nil
	}
	// The temporary installer superuser does not count as an installation.
	count, err := app.CountRecords(core.CollectionNameSuperusers, dbx.Not(dbx.HashExp{"email": core.DefaultInstallerEmail}))
	if err != nil {
		return nil, err
	}
	pending := false
	if ba := core.AsBaseApp(app); ba != nil {
		pending = ba.InstanceSetupPending()
	}
	// A database another instance already set up still needs this instance
	// to choose its role (replica or new) while the setup is pending.
	status["existing"] = count > 0
	status["joinPending"] = count > 0 && pending
	if ba := core.AsBaseApp(app); ba != nil {
		_, filesRow, err := ba.PendingRestoreFilesBackup()
		if err != nil {
			return nil, err // an unreadable state is an error, not "nothing pending"
		}
		status["filesPending"] = count > 0 && pending && filesRow
	}
	status["installed"] = count > 0 && !pending
	if !detailed {
		return status, nil
	}
	if count > 0 && pending {
		if ba := core.AsBaseApp(app); ba != nil {
			instances, err := ba.Instances()
			if err != nil {
				return nil, err
			}
			id, _ := ba.InstanceID()
			others := []*core.Instance{}
			for _, in := range instances {
				if in.ID != id {
					others = append(others, in)
				}
			}
			status["instances"] = others
		}
	}
	status["fileServer"], status["fileMode"] = nil, ""
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

// installerHandler serves /api/installer/*. onComplete, when set, is called
// after setup finishes or after connecting to an already set-up database.
func installerHandler(app core.App, onComplete func()) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		installerMutex.Lock()
		defer installerMutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fail := func(code int, err error) {
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		}
		if r.URL.Path == "/api/installer/status" && r.Method == "GET" {
			s, e := installerStatus(app, installerAuthorized(r))
			if e != nil {
				fail(500, errors.New("database status unavailable"))
				return
			}
			json.NewEncoder(w).Encode(s)
			return
		}
		if r.URL.Path == "/api/installer/backups" && r.Method == "GET" {
			if !installerAuthorized(r) {
				fail(401, errors.New("invalid or missing installer token"))
				return
			}
			installerBackups(app, w, fail)
			return
		}
		if r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		if !installerAuthorized(r) {
			fail(401, errors.New("invalid or missing installer token"))
			return
		}
		status, err := installerStatus(app, false)
		if err != nil {
			fail(500, errors.New("database status unavailable"))
			return
		}
		if status["installed"] == true {
			fail(403, errors.New("installation already complete"))
			return
		}
		if r.URL.Path == "/api/installer/backup/upload" {
			r.Body = http.MaxBytesReader(w, r.Body, 4<<30)
		} else {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
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
				// Joining a database that another instance already set up: keep
				// the installer so the user picks a replica or a new instance.
				if s, err := installerStatus(app, false); err == nil && s["existing"] == true {
					if ba := core.AsBaseApp(app); ba != nil {
						if err := ba.SetInstanceSetupPending(true); err != nil {
							fail(500, errors.New("could not save the setup state"))
							return
						}
					}
					json.NewEncoder(w).Encode(map[string]any{"ok": true, "existing": true})
					return
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/api/installer/backup/upload":
			installerBackupUpload(app, w, r, fail)
		case "/api/installer/backup/analyze":
			var body struct {
				Key string `json:"key"`
			}
			if err = json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
				fail(400, errors.New("invalid request body"))
				return
			}
			if !installerBackupsAllowed(app, fail) {
				return
			}
			ba := core.AsBaseApp(app)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			analysis, err := ba.AnalyzeBackup(ctx, body.Key)
			if err != nil {
				fail(400, err)
				return
			}
			json.NewEncoder(w).Encode(analysis)
		case "/api/installer/join":
			installerJoin(app, w, r, fail, onComplete)
		case "/api/installer/join/files":
			installerJoinFiles(app, w, r, fail, onComplete)
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
			if ba := core.AsBaseApp(app); ba != nil {
				_, filesRow, err := ba.PendingRestoreFilesBackup()
				if err != nil {
					fail(500, errors.New("could not read the restore state"))
					return
				}
				if filesRow {
					fail(409, errors.New("a restore did not finish copying its files"))
					return
				}
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
			// A first install has nothing to review: apply the app migrations
			// now. Later ones wait for a superuser to review them.
			if err = app.RunAppMigrations(); err != nil {
				fail(500, errors.New("applying the migrations failed: "+err.Error()))
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
			if onComplete != nil {
				onComplete()
			}
		default:
			http.NotFound(w, r)
		}
	})
}

func bindInstallerAPI(app core.App, rg *router.RouterGroup[*core.RequestEvent]) {
	h := installerHandler(app, nil)
	for _, path := range []string{"test-db", "test-s3", "connect", "complete", "join", "join/files", "backup/upload", "backup/analyze"} {
		rg.POST("/installer/"+path, WrapStdHandler(h))
	}
	rg.GET("/installer/status", WrapStdHandler(h))
	rg.GET("/installer/backups", WrapStdHandler(h))
}

// ServeInstaller serves only the installer API and the dashboard while no
// database is configured, and prints the setup URL with the one-time
// installer token. It returns nil once setup is finished (or the connected
// database is already set up), so the caller can start the full server with
// apis.Serve; the installer listener never serves the regular API.
func ServeInstaller(app core.App, addr string) error {
	done := make(chan struct{})
	var once sync.Once
	installer := installerHandler(app, func() { once.Do(func() { close(done) }) })
	var dashboard http.Handler
	if ui.DistDirFS != nil {
		dashboard = http.StripPrefix("/_/", http.FileServerFS(ui.DistDirFS))
	}
	server := &http.Server{
		Addr:              addr,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      2 * time.Hour, // a restore runs inside one request
		IdleTimeout:       2 * time.Minute,
	}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/installer/"):
			installer.ServeHTTP(w, r)
		case dashboard != nil && r.URL.Path == "/_":
			http.Redirect(w, r, "/_/", http.StatusMovedPermanently)
		case dashboard != nil && strings.HasPrefix(r.URL.Path, "/_/"):
			dashboard.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})

	// Hand off once setup is done. Shutdown lets the in-flight installer
	// response finish before ListenAndServe returns.
	go func() {
		<-done
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	host := addr
	if strings.HasPrefix(host, ":") || strings.HasPrefix(host, "0.0.0.0:") {
		host = "127.0.0.1" + host[strings.Index(host, ":"):]
	}
	color.Magenta("\n(!) No database is configured. Open the URL below to set up this instance:")
	color.New(color.Bold).Add(color.FgCyan).Printf("http://%s/_/#/pbinstall/%s\n\n", host, InstallerToken())

	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) && app.IsBootstrapped() {
		return nil
	}
	return err
}

// installerBackupsAllowed reports whether the backup helpers may be used: only
// while this instance's join setup is pending.
func installerBackupsAllowed(app core.App, fail func(int, error)) bool {
	ba := core.AsBaseApp(app)
	if ba == nil || !app.IsBootstrapped() || !ba.InstanceSetupPending() {
		fail(409, errors.New("no pending instance setup"))
		return false
	}
	return true
}

func installerBackups(app core.App, w http.ResponseWriter, fail func(int, error)) {
	if !installerBackupsAllowed(app, fail) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fsys, err := app.NewBackupsFilesystem()
	if err != nil {
		fail(400, err)
		return
	}
	defer fsys.Close()
	fsys.SetContext(ctx)
	items, err := fsys.List("")
	if err != nil {
		fail(400, err)
		return
	}
	result := make([]backupFileInfo, len(items))
	for i, obj := range items {
		modified, _ := types.ParseDateTime(obj.ModTime)
		result[i] = backupFileInfo{Key: obj.Key, Size: obj.Size, Modified: modified}
	}
	json.NewEncoder(w).Encode(result)
}

func installerBackupUpload(app core.App, w http.ResponseWriter, r *http.Request, fail func(int, error)) {
	if !installerBackupsAllowed(app, fail) {
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fail(400, errors.New("invalid upload"))
		return
	}
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		fail(400, errors.New("missing file"))
		return
	}
	file, err := filesystem.NewFileFromMultipart(headers[0])
	if err != nil {
		fail(400, err)
		return
	}
	if !strings.HasSuffix(strings.ToLower(file.OriginalName), ".zip") {
		fail(400, errors.New("a backup must be a .zip file"))
		return
	}
	fsys, err := app.NewBackupsFilesystem()
	if err != nil {
		fail(400, err)
		return
	}
	defer fsys.Close()
	file.OriginalName = filepath.Base(file.OriginalName)
	file.Name = file.OriginalName
	if ok, _ := fsys.Exists(file.OriginalName); ok {
		fail(400, errors.New("a backup with this name already exists"))
		return
	}
	if err := fsys.UploadFile(file, file.OriginalName); err != nil {
		fail(400, err)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "key": file.OriginalName})
}

// installerJoin finishes the setup of an instance joining an already set up
// database: a replica of an instance, or a new instance (clean or restored).
func installerJoin(app core.App, w http.ResponseWriter, r *http.Request, fail func(int, error), onComplete func()) {
	ba := core.AsBaseApp(app)
	if ba == nil || !app.IsBootstrapped() {
		fail(409, errors.New("connect a database first"))
		return
	}
	if !ba.InstanceSetupPending() {
		fail(409, errors.New("this instance has no pending setup"))
		return
	}
	// a restore committed but its files are not copied yet: only that can continue
	if _, filesRow, err := ba.PendingRestoreFilesBackup(); err != nil {
		fail(500, errors.New("could not read the restore state"))
		return
	} else if filesRow {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "filesPending": true, "filesError": "Copying the restored files did not finish."})
		return
	}
	var body struct {
		Mode        string                               `json:"mode"` // replica | new
		CopyFrom    string                               `json:"copyFrom"`
		Name        string                               `json:"name"`
		URL         string                               `json:"url"`
		Start       string                               `json:"start"` // clean | restore
		Backup      string                               `json:"backup"`
		Settings    string                               `json:"settings"` // instance | none
		Resolutions map[string]core.CollectionResolution `json:"resolutions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(400, errors.New("invalid request body"))
		return
	}

	if body.URL != "" {
		u, perr := url.Parse(body.URL)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			fail(400, errors.New("the public URL must start with http:// or https://"))
			return
		}
	}

	report := &core.RestoreReport{TranslatedViews: []core.RestoreViewReport{}, SkippedViews: []core.RestoreViewReport{}}
	var err error
	switch {
	case body.Mode == "replica":
		if body.CopyFrom == "" {
			fail(400, errors.New("choose the instance to replicate"))
			return
		}
		err = ba.ConfigureReplicaInstance(body.CopyFrom, body.Name, body.URL)
	case body.Mode == "new" && body.Start == "clean":
		err = ba.ConfigureCleanInstance(body.Name, body.URL)
	case body.Mode == "new" && body.Start == "restore":
		if body.Backup == "" {
			fail(400, errors.New("choose a backup"))
			return
		}
		if err = ba.SetInstanceInfo(body.Name, body.URL); err != nil {
			break
		}
		// every new instance has its own settings; "instance" also restores
		// the backup settings into them
		if err = ba.EnableInstanceSettings(""); err != nil {
			break
		}
		// its own migration history, filled from the backup by the restore
		if err = ba.UseOwnMigrationGroup(); err != nil {
			break
		}
		settings := body.Settings
		if settings != "instance" {
			settings = "none"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		err = ba.RestoreBackupWithOptions(ctx, body.Backup, core.RestoreOptions{
			DropCollections:   true,
			RequireResolution: true,
			ScopeToImported:   true,
			Resolutions:       body.Resolutions,
			Settings:          settings,
		})
		if err == nil || errors.Is(err, core.ErrRestoreFiles) {
			if r, _ := app.Store().Get(core.StoreKeyLastRestoreReport).(*core.RestoreReport); r != nil {
				report = r
			}
			_ = ba.SetInstanceInfo(body.Name, body.URL)
			_ = ba.SetInstanceURLSetting(body.URL)
		}
	default:
		err = errors.New("choose a replica or a new instance (clean or from a backup)")
	}
	// The database part committed (collections, records, scope) but copying
	// the files failed: keep the setup pending so the files can be retried
	// (or explicitly discarded) through /api/installer/join/files.
	if errors.Is(err, core.ErrRestoreFiles) {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "filesPending": true, "filesError": err.Error(), "report": report})
		return
	}
	if err != nil {
		fail(400, err)
		return
	}
	if err := ba.SetInstanceSetupPending(false); err != nil {
		fail(500, errors.New("could not save the setup state"))
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "report": report})
	if onComplete != nil {
		onComplete()
	}
}

// installerJoinFiles retries copying the files of a join restore whose
// database part committed ({} to retry, {"discard":true} to finish without
// them), then finishes the setup.
func installerJoinFiles(app core.App, w http.ResponseWriter, r *http.Request, fail func(int, error), onComplete func()) {
	ba := core.AsBaseApp(app)
	if ba == nil || !app.IsBootstrapped() || !ba.InstanceSetupPending() {
		fail(409, errors.New("there are no restored files to copy"))
		return
	}
	if _, filesRow, err := ba.PendingRestoreFilesBackup(); err != nil {
		fail(500, errors.New("could not read the restore state"))
		return
	} else if !filesRow {
		fail(409, errors.New("there are no restored files to copy"))
		return
	}
	var body struct {
		Discard bool `json:"discard"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if !body.Discard {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		if err := ba.CopyPendingRestoreFiles(ctx); err != nil {
			fail(400, err)
			return
		}
	}
	// Finish the setup first and clear the saved state last: if the second
	// step fails the instance is complete and the leftover state can be
	// discarded from Settings > Backups. The other order could leave a pending
	// setup without anything to retry.
	if err := ba.SetInstanceSetupPending(false); err != nil {
		fail(500, errors.New("could not save the setup state"))
		return
	}
	if err := ba.DiscardRestoreFiles(); err != nil {
		app.Logger().Warn("Could not clear the pending restore files state", "error", err)
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
	if onComplete != nil {
		onComplete()
	}
}
