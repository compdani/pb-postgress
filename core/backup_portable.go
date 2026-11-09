package core

import (
	"archive/zip"
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/filesystem"
)

// PortableBackupFormat identifies a portable backup archive.
//
// The archive is a zip with:
//
//	manifest.json             format, formatVersion, created, sourceDriver,
//	                          files (bool) and per-collection row counts
//	collections.json          the collections, in the collections import format
//	params.json               [{"id","value"}]: _params rows, value as stored
//	                          (the settings may be encrypted with PB_ENCRYPTION)
//	migrations.json           [{"file","applied"}]: the _migrations rows
//	records/<collectionId>.jsonl
//	                          one JSON object per record, column -> string|null
//	storage/<collectionId>/<recordId>/<filename>
//	                          record files (thumbnails are not included)
//
// Record values use the text form a database returns for each column,
// which is also the form PocketBase loads records from. The same archive
// therefore restores on PostgreSQL and MySQL.
const (
	PortableBackupFormat        = "pocketbase-portable"
	PortableBackupFormatVersion = 1
)

// PocketBaseImportVersions is the range of standard PocketBase versions
// whose backups (pb_data zips) RestoreBackup can import.
const PocketBaseImportVersions = "v0.23.0 - v0.41.x"

const (
	portableManifestFile    = "manifest.json"
	portableCollectionsFile = "collections.json"
	portableParamsFile      = "params.json"
	portableMigrationsFile  = "migrations.json"
	portableRecordsDir      = "records/"
	portableStorageDir      = "storage/"
)

type portableManifest struct {
	Format        string                       `json:"format"`
	FormatVersion int                          `json:"formatVersion"`
	Created       string                       `json:"created"`
	SourceDriver  string                       `json:"sourceDriver"`
	Files         bool                         `json:"files"`
	Collections   []portableManifestCollection `json:"collections"`
}

type portableManifestCollection struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

type backupParam struct {
	ID    string `json:"id" db:"id"`
	Value string `json:"value" db:"value"`
}

type backupMigration struct {
	File    string `json:"file" db:"file"`
	Applied int64  `json:"applied" db:"applied"`
}

// backupSource is a readable backup archive.
type backupSource interface {
	// Kind describes the archive for error messages.
	Kind() string
	// Collections returns the collections in the import format.
	Collections() ([]map[string]any, error)
	Params() ([]backupParam, error)
	Migrations() ([]backupMigration, error)
	// Rows streams the stored rows of a collection.
	Rows(c *Collection, fn func(dbx.NullStringMap) error) error
	// Files streams record files; key is "<collectionId>/<recordId>/<name>".
	Files(fn func(key string, r io.Reader) error) error
	Close() error
}

func (app *BaseApp) requireUnscopedForBackup() error {
	if !app.HasTable("_instances") {
		return nil
	}
	scope, err := app.InstanceScope()
	if err != nil {
		return err
	}
	if scope.Mode != "" && scope.Mode != "all" {
		return ErrBackupScoped
	}
	return nil
}

// writePortableBackup writes a portable backup archive to dest.
func (app *BaseApp) writePortableBackup(ctx context.Context, dest string) (err error) {
	if err := app.requireUnscopedForBackup(); err != nil {
		return err
	}

	db, ok := app.ConcurrentDB().(*dbx.DB)
	if !ok {
		return errors.New("backups cannot be created inside a transaction")
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, out.Close())
	}()

	zw := zip.NewWriter(out)

	manifest := portableManifest{
		Format:        PortableBackupFormat,
		FormatVersion: PortableBackupFormatVersion,
		Created:       time.Now().UTC().Format(time.RFC3339),
		SourceDriver:  app.dialectName(),
	}

	var collections []*Collection

	// One repeatable-read snapshot for every table, so records, settings
	// and collections are consistent with each other.
	txErr := db.TransactionalContext(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, func(tx *dbx.Tx) error {
		txApp := app.createTxApp(tx, false)
		if err := txApp.CollectionQuery().OrderBy("created ASC", "id ASC").All(&collections); err != nil {
			return err
		}
		// keep the secrets (the regular serialization redacts them)
		collectionsJSON, err := collectionsBackupJSON(collections)
		if err != nil {
			return err
		}
		if err := writeZipJSON(zw, portableCollectionsFile, collectionsJSON); err != nil {
			return err
		}

		params := []backupParam{}
		if err := tx.Select("id", "value").From(paramsTable).OrderBy("id").All(&params); err != nil {
			return err
		}
		if err := writeZipJSON(zw, portableParamsFile, params); err != nil {
			return err
		}

		migrations := []backupMigration{}
		if err := tx.Select("file", "applied").From(DefaultMigrationsTable).OrderBy("file").All(&migrations); err != nil {
			return err
		}
		if err := writeZipJSON(zw, portableMigrationsFile, migrations); err != nil {
			return err
		}

		for _, c := range collections {
			if c.IsView() {
				continue
			}
			rows, err := writePortableRecords(ctx, tx, zw, c)
			if err != nil {
				return fmt.Errorf("collection %q: %w", c.Name, err)
			}
			manifest.Collections = append(manifest.Collections, portableManifestCollection{ID: c.Id, Name: c.Name, Rows: rows})
		}

		return nil
	})
	if txErr != nil {
		return txErr
	}

	// Files are not part of the database snapshot. Upload order (files are
	// stored before the record commits) keeps every exported record's files
	// present; files of later records may also be included.
	files, err := app.writePortableFiles(ctx, zw, collections)
	if err != nil {
		return err
	}
	manifest.Files = files

	if err := writeZipJSON(zw, portableManifestFile, manifest); err != nil {
		return err
	}

	return zw.Close()
}

func writeZipJSON(zw *zip.Writer, name string, v any) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writePortableRecords(ctx context.Context, tx *dbx.Tx, zw *zip.Writer, c *Collection) (int64, error) {
	w, err := zw.Create(portableRecordsDir + c.Id + ".jsonl")
	if err != nil {
		return 0, err
	}

	rows, err := tx.Select("*").From(c.Name).OrderBy("id").WithContext(ctx).Rows()
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	enc := json.NewEncoder(w)
	var count int64
	for rows.Next() {
		row := dbx.NullStringMap{}
		if err := rows.ScanMap(row); err != nil {
			return count, err
		}
		line := make(map[string]any, len(row))
		for k, v := range row {
			if v.Valid {
				line[k] = v.String
			} else {
				line[k] = nil
			}
		}
		if err := enc.Encode(line); err != nil {
			return count, err
		}
		count++
	}

	return count, rows.Err()
}

// writePortableFiles adds the files of collections stored locally and
// reports whether any collection's files were included.
func (app *BaseApp) writePortableFiles(ctx context.Context, zw *zip.Writer, collections []*Collection) (bool, error) {
	included := false

	for _, c := range collections {
		if c.IsView() {
			continue
		}
		kind, _, err := app.fileStorageKind(c)
		if err != nil {
			return included, err
		}
		if kind != fileStorageLocal {
			continue
		}
		included = true

		if err := app.writeCollectionFiles(ctx, zw, c); err != nil {
			return included, fmt.Errorf("files of collection %q: %w", c.Name, err)
		}
	}

	return included, nil
}

func (app *BaseApp) writeCollectionFiles(ctx context.Context, zw *zip.Writer, c *Collection) error {
	fsys, err := app.NewFilesystemForCollection(c)
	if err != nil {
		return err
	}
	defer fsys.Close()

	fsys.SetContext(ctx)

	objects, err := fsys.List(c.Id + "/")
	if err != nil {
		return err
	}

	for _, obj := range objects {
		if isBackupThumbKey(obj.Key) {
			continue
		}
		if err := copyFileToZip(fsys, zw, obj.Key); err != nil {
			return err
		}
	}

	return nil
}

func copyFileToZip(fsys *filesystem.System, zw *zip.Writer, key string) error {
	r, err := fsys.GetReader(key)
	if err != nil {
		return err
	}
	defer r.Close()

	w, err := zw.CreateHeader(&zip.FileHeader{
		Name:     portableStorageDir + key,
		Method:   zip.Deflate,
		Modified: r.ModTime(),
	})
	if err != nil {
		return err
	}

	_, err = io.Copy(w, r)
	return err
}

// isBackupThumbKey reports generated thumbnails ("<col>/<rec>/thumbs_<file>/..."),
// which are recreated on demand and not backed up.
func isBackupThumbKey(key string) bool {
	parts := strings.Split(key, "/")
	return len(parts) > 3 && strings.HasPrefix(parts[2], "thumbs_")
}

// -------------------------------------------------------------------
// Portable archive reader

type portableSource struct {
	file     *os.File
	zr       *zip.Reader
	entries  map[string]*zip.File
	manifest portableManifest
}

func openPortableSource(f *os.File, zr *zip.Reader, entries map[string]*zip.File) (*portableSource, error) {
	src := &portableSource{file: f, zr: zr, entries: entries}

	if err := src.readJSON(portableManifestFile, &src.manifest); err != nil {
		return nil, err
	}
	if src.manifest.Format != PortableBackupFormat {
		return nil, fmt.Errorf("unknown backup format %q", src.manifest.Format)
	}
	if src.manifest.FormatVersion < 1 || src.manifest.FormatVersion > PortableBackupFormatVersion {
		return nil, fmt.Errorf("portable backup format version %d is not supported by this build (supports up to %d)", src.manifest.FormatVersion, PortableBackupFormatVersion)
	}

	return src, nil
}

func (s *portableSource) Kind() string {
	return "portable backup (from " + s.manifest.SourceDriver + ")"
}

func (s *portableSource) readJSON(name string, v any) error {
	f, ok := s.entries[name]
	if !ok {
		return fmt.Errorf("the backup is missing %s", name)
	}
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	if err := json.NewDecoder(r).Decode(v); err != nil {
		return fmt.Errorf("invalid %s: %w", name, err)
	}
	return nil
}

func (s *portableSource) Collections() ([]map[string]any, error) {
	result := []map[string]any{}
	return result, s.readJSON(portableCollectionsFile, &result)
}

func (s *portableSource) Params() ([]backupParam, error) {
	result := []backupParam{}
	return result, s.readJSON(portableParamsFile, &result)
}

func (s *portableSource) Migrations() ([]backupMigration, error) {
	result := []backupMigration{}
	return result, s.readJSON(portableMigrationsFile, &result)
}

func (s *portableSource) Rows(c *Collection, fn func(dbx.NullStringMap) error) error {
	f, ok := s.entries[portableRecordsDir+c.Id+".jsonl"]
	if !ok {
		return nil // no rows were exported for this collection
	}
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()

	dec := json.NewDecoder(bufio.NewReader(r))
	for line := 1; ; line++ {
		raw := map[string]*string{}
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("records of %q, line %d: %w", c.Name, line, err)
		}
		row := make(dbx.NullStringMap, len(raw))
		for k, v := range raw {
			if v != nil {
				row[k] = sql.NullString{String: *v, Valid: true}
			} else {
				row[k] = sql.NullString{}
			}
		}
		if err := fn(row); err != nil {
			return err
		}
	}
}

func (s *portableSource) Files(fn func(key string, r io.Reader) error) error {
	return zipFiles(s.zr, portableStorageDir, fn)
}

func (s *portableSource) Close() error {
	return s.file.Close()
}

// zipFiles calls fn for each regular file under prefix, skipping
// thumbnails and local blob attribute sidecars.
func zipFiles(zr *zip.Reader, prefix string, fn func(key string, r io.Reader) error) error {
	keys := []*zip.File{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !strings.HasPrefix(f.Name, prefix) {
			continue
		}
		key := strings.TrimPrefix(f.Name, prefix)
		if strings.HasSuffix(key, ".attrs") || isBackupThumbKey(key) || strings.Count(key, "/") != 2 {
			continue
		}
		if clean := path.Clean(key); clean != key || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("invalid file path %q in the backup", f.Name)
		}
		keys = append(keys, f)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })

	for _, f := range keys {
		r, err := f.Open()
		if err != nil {
			return err
		}
		err = fn(strings.TrimPrefix(f.Name, prefix), r)
		r.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// openBackupSource detects the archive type: a portable backup has
// manifest.json at its root; a PocketBase backup has data.db.
func openBackupSource(archivePath, tempDir string) (backupSource, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("the backup is not a valid zip archive: %w", err)
	}

	entries := make(map[string]*zip.File, len(zr.File))
	for _, zf := range zr.File {
		entries[zf.Name] = zf
	}

	if _, ok := entries[portableManifestFile]; ok {
		src, err := openPortableSource(f, zr, entries)
		if err != nil {
			f.Close()
			return nil, err
		}
		return src, nil
	}

	if dataDB := findPocketBaseDataDB(zr); dataDB != nil {
		src, err := openPocketBaseSource(f, zr, dataDB, tempDir)
		if err != nil {
			f.Close()
			return nil, err
		}
		return src, nil
	}

	f.Close()
	return nil, errors.New("unrecognized backup: expected a portable backup (manifest.json) or a PocketBase backup (data.db)")
}
