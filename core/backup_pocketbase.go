package core

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/pocketbase/dbx"

	// Pure Go SQLite driver, used only to read data.db from PocketBase
	// backups being imported. The app database is never SQLite.
	_ "modernc.org/sqlite"
)

// pocketBaseSource reads a standard PocketBase backup: a zip of pb_data
// with data.db and storage/. auxiliary.db (logs) is not imported.
type pocketBaseSource struct {
	file          *os.File
	zr            *zip.Reader
	db            *dbx.DB
	tempDB        string
	storagePrefix string
}

// findPocketBaseDataDB returns the data.db entry closest to the archive
// root (a zip of the pb_data directory itself nests it one level down).
func findPocketBaseDataDB(zr *zip.Reader) *zip.File {
	var found *zip.File
	for _, f := range zr.File {
		if path.Base(f.Name) != "data.db" || strings.Count(f.Name, "/") > 1 {
			continue
		}
		if found == nil || len(f.Name) < len(found.Name) {
			found = f
		}
	}
	return found
}

func openPocketBaseSource(f *os.File, zr *zip.Reader, dataDB *zip.File, tempDir string) (src *pocketBaseSource, err error) {
	tmp, err := os.CreateTemp(tempDir, "pb_import_*.db")
	if err != nil {
		return nil, err
	}
	tempDB := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tempDB)
		}
	}()

	r, err := dataDB.Open()
	if err != nil {
		tmp.Close()
		return nil, err
	}
	_, copyErr := io.Copy(tmp, r)
	r.Close()
	if err := errors.Join(copyErr, tmp.Close()); err != nil {
		return nil, err
	}

	dsn := "file:" + (&url.URL{Path: tempDB}).EscapedPath() + "?mode=ro&_pragma=query_only(1)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db := dbx.NewFromDB(sqlDB, "sqlite")

	src = &pocketBaseSource{
		file:          f,
		zr:            zr,
		db:            db,
		tempDB:        tempDB,
		storagePrefix: strings.TrimSuffix(dataDB.Name, "data.db") + "storage/",
	}

	if err := src.checkVersion(); err != nil {
		db.Close()
		return nil, err
	}

	return src, nil
}

// checkVersion accepts the collections layout introduced in v0.23.0.
func (s *pocketBaseSource) checkVersion() error {
	columns := []string{}
	err := s.db.NewQuery("SELECT name FROM pragma_table_info('_collections')").Column(&columns)
	if err != nil {
		return fmt.Errorf("data.db is not a readable PocketBase database: %w", err)
	}
	if len(columns) == 0 {
		return errors.New("data.db has no _collections table, so it is not a PocketBase database")
	}
	hasFields := false
	for _, c := range columns {
		if c == "fields" {
			hasFields = true
		}
	}
	if !hasFields {
		return fmt.Errorf("this PocketBase backup is older than v0.23.0; upgrade it with PocketBase v0.23+ first (supported: %s)", PocketBaseImportVersions)
	}
	return nil
}

func (s *pocketBaseSource) Kind() string {
	return "PocketBase backup"
}

func (s *pocketBaseSource) Collections() ([]map[string]any, error) {
	collections := []*Collection{}
	if err := s.db.Select("*").From("_collections").OrderBy("created ASC", "id ASC").All(&collections); err != nil {
		return nil, fmt.Errorf("reading collections (supported PocketBase versions: %s): %w", PocketBaseImportVersions, err)
	}

	result := make([]map[string]any, 0, len(collections))
	for _, c := range collections {
		// keep the secrets (the regular serialization redacts them)
		encoded, err := c.marshalJSONWithSecrets()
		if err != nil {
			return nil, err
		}
		m := map[string]any{}
		if err := json.Unmarshal(encoded, &m); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, nil
}

func (s *pocketBaseSource) Params() ([]backupParam, error) {
	params := []backupParam{}
	return params, s.db.Select("id", "value").From("_params").All(&params)
}

func (s *pocketBaseSource) Migrations() ([]backupMigration, error) {
	migrations := []backupMigration{}
	return migrations, s.db.Select("file", "applied").From("_migrations").All(&migrations)
}

func (s *pocketBaseSource) Rows(c *Collection, fn func(dbx.NullStringMap) error) error {
	rows, err := s.db.Select("*").From(c.Name).OrderBy("rowid").Rows()
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		row := dbx.NullStringMap{}
		if err := rows.ScanMap(row); err != nil {
			return err
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *pocketBaseSource) Files(fn func(key string, r io.Reader) error) error {
	return zipFiles(s.zr, s.storagePrefix, fn)
}

func (s *pocketBaseSource) Close() error {
	return errors.Join(s.db.Close(), s.file.Close(), os.Remove(s.tempDB))
}
