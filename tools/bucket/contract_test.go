package bucket_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/pocketbase/pocketbase/tools/bucket"
	"github.com/pocketbase/pocketbase/tools/filesystem"
)

// TestRemoteClientAgainstBucketServer runs the core remote driver against the
// real bucket server, so the two sides of the HTTP contract cannot drift.
func TestRemoteClientAgainstBucketServer(t *testing.T) {
	store, err := bucket.OpenStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(bucket.NewServer(store, bucket.Options{APIKey: "secret", ListPageSize: 2}).Handler())
	defer srv.Close()

	fs, err := filesystem.NewRemote(srv.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	keys := []string{
		"col/rec/a.png",
		"col/rec/thumbs_a.png/100x100_a.png",
		"col/rec/thumbs_a.png/0x50_a.png",
		"col/rec/b.txt",
		"col/other/c.txt",
	}
	for i, key := range keys {
		if err := fs.Upload([]byte("data-"+strconv.Itoa(i)), key); err != nil {
			t.Fatalf("upload %s: %v", key, err)
		}
	}

	// A thumbnail key must round-trip.
	r, err := fs.GetReader("col/rec/thumbs_a.png/100x100_a.png")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r)
	r.Close()
	if string(body) != "data-1" {
		t.Fatalf("thumb body %q", body)
	}

	// Listing pages through every object (page size 2 forces 3 pages).
	listed, err := fs.List("col/rec/")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 4 {
		t.Fatalf("listed %d objects, want 4", len(listed))
	}

	// DeletePrefix lists and deletes nested thumbnail keys.
	if failures := fs.DeletePrefix("col/rec/thumbs_a.png/"); len(failures) > 0 {
		t.Fatalf("delete prefix: %v", failures)
	}
	for _, key := range keys[1:3] {
		if exists, err := fs.Exists(key); err != nil || exists {
			t.Fatalf("%s still exists (err %v)", key, err)
		}
	}
	if exists, err := fs.Exists("col/rec/a.png"); err != nil || !exists {
		t.Fatalf("original removed with thumbs (err %v)", err)
	}

	// The bucket never serves bytes anonymously.
	res, err := http.Get(srv.URL + "/files/col/rec/a.png")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous GET status %d", res.StatusCode)
	}
}
