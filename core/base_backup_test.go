package core_test

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	_ "modernc.org/sqlite"
)

const backupTestFile = "9n89pl5vkct6330/la4y2w4o98acwuj/300_uh_lkx91_hvb_Da8K5pl069.png"

func TestPortableBackupRoundTrip(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	before := recordCounts(t, app)

	if err := app.CreateBackup(context.Background(), "round.zip"); err != nil {
		t.Fatal(err)
	}

	manifest := readBackupManifest(t, app, "round.zip")
	if manifest["format"] != core.PortableBackupFormat || manifest["files"] != true {
		t.Fatalf("unexpected manifest %v", manifest)
	}
	archive := readBackupEntries(t, app, "round.zip")
	if _, ok := archive["storage/"+backupTestFile]; !ok {
		t.Fatalf("expected %s in the archive", backupTestFile)
	}
	for name := range archive {
		if strings.Contains(name, "/thumbs_") {
			t.Fatalf("thumbnail %s should not be backed up", name)
		}
	}

	// Change data, schema, settings and files after the backup.
	demo1, err := app.FindCollectionByNameOrId("demo1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().NewQuery("DELETE FROM {{demo1}}").Execute(); err != nil {
		t.Fatal(err)
	}
	extra := core.NewBaseCollection("after_backup")
	if err := app.Save(extra); err != nil {
		t.Fatal(err)
	}
	app.Settings().Meta.AppName = "changed_after_backup"
	app.Settings().S3.Bucket = "kept-bucket" // storage settings belong to the deployment
	if err := app.Save(app.Settings()); err != nil {
		t.Fatal(err)
	}
	fsys, err := app.NewFilesystemForCollection(demo1)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsys.Delete(backupTestFile); err != nil {
		t.Fatal(err)
	}
	fsys.Close()

	if err := app.RestoreBackup(context.Background(), "round.zip"); err != nil {
		t.Fatal(err)
	}

	after := recordCounts(t, app)
	for name, n := range before {
		if after[name] != n {
			t.Errorf("collection %s: expected %d records, got %d", name, n, after[name])
		}
	}
	if _, err := app.FindCollectionByNameOrId("after_backup"); err == nil {
		t.Fatal("expected the collection created after the backup to be removed")
	}
	if app.Settings().Meta.AppName == "changed_after_backup" {
		t.Fatal("expected the settings to be restored")
	}
	if app.Settings().S3.Bucket != "kept-bucket" {
		t.Fatalf("expected S3 settings to be kept, got %q", app.Settings().S3.Bucket)
	}

	superuser, err := app.FindAuthRecordByEmail(core.CollectionNameSuperusers, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !superuser.ValidatePassword("1234567890") {
		t.Fatal("expected the restored superuser password hash to validate")
	}

	fsys, err = app.NewFilesystemForCollection(demo1)
	if err != nil {
		t.Fatal(err)
	}
	defer fsys.Close()
	if ok, _ := fsys.Exists(backupTestFile); !ok {
		t.Fatal("expected the deleted file to be restored")
	}
}

func TestRestorePocketBaseBackup(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	_, currentFile, _, _ := runtime.Caller(0)
	dataDir := filepath.Join(filepath.Dir(currentFile), "..", "tests", "data")

	expected := sqliteRecordCounts(t, filepath.Join(dataDir, "data.db"))

	// Make the target differ from the source.
	if _, err := app.DB().NewQuery("DELETE FROM {{demo1}}").Execute(); err != nil {
		t.Fatal(err)
	}
	if err := app.Save(core.NewBaseCollection("not_in_pocketbase")); err != nil {
		t.Fatal(err)
	}

	writePocketBaseBackup(t, app, "pocketbase.zip", dataDir, "pb_data/")

	if err := app.RestoreBackup(context.Background(), "pocketbase.zip"); err != nil {
		t.Fatal(err)
	}

	after := recordCounts(t, app)
	for name, n := range expected {
		if after[name] != n {
			t.Errorf("collection %s: expected %d records (as in data.db), got %d", name, n, after[name])
		}
	}
	if _, err := app.FindCollectionByNameOrId("not_in_pocketbase"); err == nil {
		t.Fatal("expected collections missing from the PocketBase backup to be removed")
	}

	superuser, err := app.FindAuthRecordByEmail(core.CollectionNameSuperusers, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !superuser.ValidatePassword("1234567890") {
		t.Fatal("expected the imported superuser password hash to validate")
	}

	demo1, err := app.FindCollectionByNameOrId("demo1")
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := app.NewFilesystemForCollection(demo1)
	if err != nil {
		t.Fatal(err)
	}
	defer fsys.Close()
	if ok, _ := fsys.Exists(backupTestFile); !ok {
		t.Fatal("expected the PocketBase storage files to be imported")
	}
}

func TestRestoreRejectsInvalidBackups(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	tempDir := t.TempDir()

	// pre-v0.23 layout: _collections without a fields column
	oldDB := filepath.Join(tempDir, "data.db")
	db, err := sql.Open("sqlite", oldDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE _collections (id TEXT, name TEXT, schema TEXT)"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	scenarios := []struct {
		name    string
		entries map[string][]byte
		err     string
	}{
		{"not a backup", map[string][]byte{"readme.txt": []byte("x")}, "unrecognized backup"},
		{"unknown portable version", map[string][]byte{"manifest.json": []byte(`{"format":"pocketbase-portable","formatVersion":99}`)}, "format version 99"},
		{"old PocketBase", map[string][]byte{"data.db": mustRead(t, oldDB)}, "older than v0.23.0"},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			uploadZip(t, app, "invalid.zip", s.entries)
			err := app.RestoreBackup(context.Background(), "invalid.zip")
			if err == nil || !strings.Contains(err.Error(), s.err) {
				t.Fatalf("expected error containing %q, got %v", s.err, err)
			}
		})
	}

	if _, err := app.FindCollectionByNameOrId("demo1"); err != nil {
		t.Fatalf("expected the database to be untouched: %v", err)
	}
}

func TestBackupRequiresUnscopedInstance(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	if err := app.SetInstanceScope(core.InstanceScope{Mode: "deny", Collections: []string{"demo1"}}); err != nil {
		t.Fatal(err)
	}

	if err := app.CreateBackup(context.Background(), "scoped.zip"); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("expected a scope error, got %v", err)
	}
}

// A value that does not fit MySQL's VARCHAR(255) fails before anything
// is changed, since MySQL schema changes cannot be rolled back.
func TestRestoreChecksMySQLTextLength(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	if os.Getenv("PB_TEST_DB_DRIVER") != "mysql" {
		t.Skip("MySQL only")
	}

	if err := app.CreateBackup(context.Background(), "long.zip"); err != nil {
		t.Fatal(err)
	}

	demo1, err := app.FindCollectionByNameOrId("demo1")
	if err != nil {
		t.Fatal(err)
	}
	entries := readBackupEntries(t, app, "long.zip")
	recordsFile := "records/" + demo1.Id + ".jsonl"
	lines := strings.SplitN(string(entries[recordsFile]), "\n", 2)
	row := map[string]any{}
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatal(err)
	}
	row["text"] = strings.Repeat("x", 300)
	first, _ := json.Marshal(row)
	entries[recordsFile] = []byte(string(first) + "\n" + lines[1])
	uploadZip(t, app, "long_edited.zip", entries)

	before := recordCounts(t, app)
	err = app.RestoreBackup(context.Background(), "long_edited.zip")
	if err == nil || !strings.Contains(err.Error(), "VARCHAR(255)") {
		t.Fatalf("expected a VARCHAR(255) error, got %v", err)
	}
	if after := recordCounts(t, app); after["demo1"] != before["demo1"] {
		t.Fatal("expected the data to be unchanged")
	}
}

// -------------------------------------------------------------------

func recordCounts(t testing.TB, app core.App) map[string]int {
	collections, err := app.FindAllCollections()
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]int{}
	for _, c := range collections {
		if c.IsView() {
			continue
		}
		var n int
		if err := app.DB().Select("count(*)").From(c.Name).Row(&n); err != nil {
			t.Fatal(err)
		}
		result[c.Name] = n
	}
	return result
}

func sqliteRecordCounts(t testing.TB, path string) map[string]int {
	sqlDB, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	db := dbx.NewFromDB(sqlDB, "sqlite")
	defer db.Close()

	rows := []struct {
		Name string `db:"name"`
	}{}
	if err := db.Select("name").From("_collections").Where(dbx.NewExp("type != 'view'")).All(&rows); err != nil {
		t.Fatal(err)
	}
	result := map[string]int{}
	for _, r := range rows {
		var n int
		if err := db.Select("count(*)").From(r.Name).Row(&n); err != nil {
			t.Fatal(err)
		}
		result[r.Name] = n
	}
	return result
}

func readBackupEntries(t testing.TB, app core.App, name string) map[string][]byte {
	fsys, err := app.NewBackupsFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	defer fsys.Close()
	r, err := fsys.GetReader(name)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	result := map[string][]byte{}
	for _, f := range zr.File {
		fr, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		result[f.Name], err = io.ReadAll(fr)
		fr.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func readBackupManifest(t testing.TB, app core.App, name string) map[string]any {
	manifest := map[string]any{}
	if err := json.Unmarshal(readBackupEntries(t, app, name)["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func uploadZip(t testing.TB, app core.App, name string, entries map[string][]byte) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for entryName, content := range entries {
		w, err := zw.Create(entryName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	fsys, err := app.NewBackupsFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	defer fsys.Close()
	if err := fsys.Upload(buf.Bytes(), name); err != nil {
		t.Fatal(err)
	}
}

// writePocketBaseBackup zips data.db and storage/ the way a pb_data
// archive looks, under prefix.
func writePocketBaseBackup(t testing.TB, app core.App, name, dataDir, prefix string) {
	entries := map[string][]byte{prefix + "data.db": mustRead(t, filepath.Join(dataDir, "data.db"))}
	storage := filepath.Join(dataDir, "storage")
	err := filepath.Walk(storage, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}
		entries[prefix+filepath.ToSlash(rel)] = mustRead(t, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	uploadZip(t, app, name, entries)
}

func mustRead(t testing.TB, path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// View queries in a PocketBase import are translated from SQLite; a view
// that still fails is skipped and reported, and the rest is restored.
func TestRestorePocketBaseTranslatesViews(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	_, currentFile, _, _ := runtime.Caller(0)
	dataDir := filepath.Join(filepath.Dir(currentFile), "..", "tests", "data")

	// a copy of the PocketBase data dir with SQLite-only view queries
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "data.db"), mustRead(t, filepath.Join(dataDir, "data.db")), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(srcDir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	// view2 selects from view1, so view1 must keep these columns.
	translatable := `select id, "bool", file_many, rel_many, (ifnull(text, '') || '-' || strftime('%Y', created)) as label from demo1 where text like 'TEST%'`
	untranslatable := `select id, unixepoch(created) as ts from clients`
	for name, query := range map[string]string{"view1": translatable, "numeric_id_view": untranslatable} {
		if _, err := db.Exec("UPDATE _collections SET options = json_set(options, '$.viewQuery', ?) WHERE name = ?", query, name); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	entries := map[string][]byte{"data.db": mustRead(t, filepath.Join(srcDir, "data.db"))}
	uploadZip(t, app, "views.zip", entries)

	if err := app.RestoreBackup(context.Background(), "views.zip"); err != nil {
		t.Fatal(err)
	}

	report, _ := app.Store().Get(core.StoreKeyLastRestoreReport).(*core.RestoreReport)
	if report == nil {
		t.Fatal("expected a restore report")
	}

	translated := map[string]bool{}
	for _, v := range report.TranslatedViews {
		translated[v.Name] = true
	}
	if !translated["view1"] {
		t.Fatalf("expected view1 to be translated, got %+v", report)
	}
	if len(report.SkippedViews) != 1 || report.SkippedViews[0].Name != "numeric_id_view" || report.SkippedViews[0].OriginalQuery != untranslatable {
		t.Fatalf("expected only numeric_id_view to be skipped, got %+v", report.SkippedViews)
	}

	// the translated view and the view built on it both work
	records, err := app.FindAllRecords("view1")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 {
		t.Fatal("expected view1 to return records (LIKE should ignore case like SQLite)")
	}
	for _, r := range records {
		if label := r.GetString("label"); !strings.Contains(label, "-20") {
			t.Fatalf("unexpected label %q", label)
		}
	}
	if _, err := app.FindAllRecords("view2"); err != nil {
		t.Fatalf("expected view2 to work: %v", err)
	}

	if _, err := app.FindCollectionByNameOrId("numeric_id_view"); err == nil {
		t.Fatal("expected the skipped view to be absent")
	}
	if _, err := app.FindCollectionByNameOrId("clients"); err != nil {
		t.Fatalf("expected the rest to be restored: %v", err)
	}
}
