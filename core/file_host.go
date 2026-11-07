package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/security"
)

// FileHost is the single file-serving instance shared by all instances using this database.
type FileHost struct {
	Secret       string    `db:"secret" json:"-"`
	InstanceID   string    `db:"instanceId" json:"instanceId"`
	URL          string    `db:"url" json:"url"`
	Mode         string    `db:"mode" json:"mode"`
	RegisteredAt time.Time `db:"registered_at" json:"registered_at,omitempty"`
}

func (app *BaseApp) InstanceID() (string, error) {
	data, err := os.ReadFile(filepath.Join(app.DataDir(), "db.json"))
	var c DatabaseConfig
	if err == nil {
		if err = json.Unmarshal(data, &c); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if c.InstanceID != "" {
		return c.InstanceID, nil
	}
	c.Driver, c.DSN = app.config.DBDriver, app.config.DBDSN
	c.InstanceID = security.RandomString(32)
	if err = SaveDatabaseConfig(app.DataDir(), c); err != nil {
		return "", err
	}
	return c.InstanceID, nil
}

func (app *BaseApp) FileHost() (*FileHost, error) {
	var host FileHost
	err := app.DB().Select("id AS instanceId", "url", "mode", "secret", "registered_at").From("_fileHosts").One(&host)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &host, nil
}
func (app *BaseApp) SetFileHost(host FileHost) error {
	u, err := url.Parse(host.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("file host requires an absolute http(s) public URL")
	}
	if host.InstanceID == "" || (host.Mode != "full" && host.Mode != "bucket" && host.Mode != "s3") {
		return errors.New("file host requires instanceId and mode full or bucket")
	}
	if host.Secret == "" {
		host.Secret = security.RandomString(64)
	}
	return app.RunInTransaction(func(txApp App) error {
		if _, err := txApp.DB().Delete("_fileHosts", nil).Execute(); err != nil {
			return err
		}
		_, err := txApp.DB().Insert("_fileHosts", dbx.Params{"id": host.InstanceID, "url": strings.TrimRight(host.URL, "/"), "mode": host.Mode, "secret": host.Secret, "registered_at": time.Now().UTC()}).Execute()
		return err
	})
}
