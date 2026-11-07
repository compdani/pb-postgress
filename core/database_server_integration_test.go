package core_test

import (
	"errors"
	"os"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	_ "github.com/pocketbase/pocketbase/migrations"
)

// PB_TEST_DB_DSN must name an empty, disposable database.
func TestServerDatabaseFileChanges(t *testing.T) {
	dsn := os.Getenv("PB_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("set PB_TEST_DB_DRIVER and PB_TEST_DB_DSN to an empty disposable database")
	}
	driver := os.Getenv("PB_TEST_DB_DRIVER")
	if driver == "" {
		driver = "postgres"
	}
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir(), DBDriver: driver, DBDSN: dsn})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer app.ClearBootstrap()
	c := core.NewBaseCollection("file_feed_test")
	c.Fields.Add(&core.FileField{Name: "files", MaxSelect: 5})
	if err := app.Save(c); err != nil {
		t.Fatal(err)
	}
	defer app.Delete(c)
	record := core.NewRecord(c)
	record.Set("files", []string{"first.txt"})
	if err := app.SaveNoValidate(record); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := app.DB().Select("count(*)").From("_fileChanges").Row(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	if before != 1 {
		t.Fatalf("create events: %d", before)
	}
	rollback := errors.New("rollback")
	err := app.RunInTransaction(func(tx core.App) error {
		record.Set("files", []string{"second.txt"})
		if err := tx.SaveNoValidate(record); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if count() != before {
		t.Fatal("rolled back file changes persisted")
	}
	record, err = app.FindRecordById(c, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	record.Set("files", []string{"second.txt"})
	if err = app.SaveNoValidate(record); err != nil {
		t.Fatal(err)
	}
	if count() != 3 {
		t.Fatal("replace should emit delete and upsert")
	}
	if err = app.Delete(record); err != nil {
		t.Fatal(err)
	}
	if count() != 4 {
		t.Fatal("record delete should emit file deletion")
	}
	host := core.FileHost{InstanceID: "other-instance", URL: "https://files.example.test", Mode: "bucket", Secret: "shared"}
	if err = app.SetFileHost(host); err != nil {
		t.Fatal(err)
	}
	got, err := app.FileHost()
	if err != nil || got == nil || got.Secret != "shared" {
		t.Fatalf("file host: %+v %v", got, err)
	}
}
