package bucket

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestPostgresFileFeed(t *testing.T) {
	dsn := os.Getenv("PB_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("PB_TEST_PG_DSN not set")
	}
	testFileFeed(t, "postgres", dsn, true)
}

func TestMySQLFileFeed(t *testing.T) {
	dsn := os.Getenv("PB_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("PB_TEST_MYSQL_DSN not set")
	}
	testFileFeed(t, "mysql", dsn, false)
}

func testFileFeed(t *testing.T, driver, dsn string, notify bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	db, err := OpenDB(ctx, driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatalf("second ensure: %v", err)
	}

	hostID := "bkt_test_" + t.Name()
	collection := "bkt_test_col"
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanCancel()
		_, _ = db.sql.ExecContext(cleanCtx, "DELETE FROM "+db.changesTable+" WHERE collection = "+db.ph(1), collection)
		_, _ = db.sql.ExecContext(cleanCtx, "DELETE FROM "+db.hostsTable+" WHERE id = "+db.ph(1), hostID)
	})

	if err := db.Register(ctx, Host{
		ID:     hostID,
		URL:    "http://127.0.0.1:8091",
		Mode:   "bucket",
		Secret: "test-secret",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Register(ctx, Host{
		ID:     hostID,
		URL:    "http://127.0.0.1:8092",
		Mode:   "bucket",
		Secret: "test-secret-2",
	}); err != nil {
		t.Fatal(err)
	}
	var gotURL, gotMode, gotSecret string
	q := "SELECT url, mode, secret FROM " + db.hostsTable + " WHERE id = " + db.ph(1)
	if err := db.sql.QueryRowContext(ctx, q, hostID).Scan(&gotURL, &gotMode, &gotSecret); err != nil {
		t.Fatal(err)
	}
	if gotURL != "http://127.0.0.1:8092" || gotMode != "bucket" || gotSecret != "test-secret-2" {
		t.Fatalf("registered url=%s mode=%s secret=%s", gotURL, gotMode, gotSecret)
	}

	var insert string
	if driver == "mysql" {
		insert = "INSERT INTO " + db.changesTable + " (op, collection, record_id, filename) VALUES (?, ?, ?, ?)"
	} else {
		insert = "INSERT INTO " + db.changesTable + " (op, collection, record_id, filename) VALUES ($1, $2, $3, $4)"
	}
	res, err := db.sql.ExecContext(ctx, insert, "upsert", collection, "rec", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	var insertedID int64
	if driver == "mysql" {
		insertedID, err = res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
	} else {
		if err := db.sql.QueryRowContext(ctx, "SELECT id FROM "+db.changesTable+" WHERE collection = $1 ORDER BY id DESC LIMIT 1", collection).Scan(&insertedID); err != nil {
			t.Fatal(err)
		}
	}

	changes, err := db.FetchAfter(ctx, insertedID-1, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range changes {
		if change.ID == insertedID && change.Op == "upsert" && change.Collection == collection && change.Filename == "a.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing change %d in %+v", insertedID, changes)
	}

	if !notify {
		return
	}
	ch := db.Notify(ctx)
	deadline := time.Now().Add(4 * time.Second)
	signaled := false
	for time.Now().Before(deadline) && !signaled {
		if _, err := db.sql.ExecContext(ctx, "NOTIFY pb_file_changes"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ch:
			signaled = true
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !signaled {
		t.Fatal("timed out waiting for pb_file_changes")
	}
}
