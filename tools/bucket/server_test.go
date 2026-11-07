package bucket

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, maxBytes int64) (*httptest.Server, *Store) {
	t.Helper()
	store, err := OpenStore(t.TempDir(), maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, Options{APIKey: "test-secret", MaxBytes: maxBytes, CORSOrigins: []string{"*"}})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, store
}

func putFile(t *testing.T, ts *httptest.Server, path string, body []byte, header map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-secret")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func mustStatus(t *testing.T, res *http.Response, want int) []byte {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != want {
		t.Fatalf("status %d, want %d, body %s", res.StatusCode, want, b)
	}
	return b
}

func TestHealthAndAuth(t *testing.T) {
	ts, _ := newTestServer(t, 1<<20)

	res, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body := mustStatus(t, res, http.StatusOK)
	if !strings.Contains(string(body), `"ok":true`) && !strings.Contains(string(body), `"ok": true`) {
		t.Fatalf("health body %s", body)
	}

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/files/col/rec/a.txt", strings.NewReader("x"))
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, res, http.StatusUnauthorized)

	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/files/col/rec/a.txt", strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer wrong")
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, res, http.StatusUnauthorized)
}

func TestPutGetHeadRangeAndList(t *testing.T) {
	ts, store := newTestServer(t, 1<<20)
	payload := []byte("abcdefghijklmnopqrstuvwxyz0123456789")

	res := putFile(t, ts, "/files/demo/rec1/note.txt", payload, nil)
	mustStatus(t, res, http.StatusCreated)

	res = putFile(t, ts, "/files/demo/rec1/note.txt", payload, nil)
	mustStatus(t, res, http.StatusOK)

	metaPath, err := store.metaPath("demo", "rec1", "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta ObjectMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Size != int64(len(payload)) || meta.ContentType == "" || !strings.HasPrefix(meta.ETag, `"`) {
		t.Fatalf("metadata %+v", meta)
	}

	res, err = ts.Client().Get(ts.URL + "/files/demo/rec1/note.txt")
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, res, http.StatusUnauthorized)

	res = authed(t, ts, http.MethodGet, "/files/demo/rec1/note.txt", nil)
	body := mustStatus(t, res, http.StatusOK)
	if !bytes.Equal(body, payload) {
		t.Fatalf("body %q", body)
	}
	if res.Header.Get("ETag") == "" || res.Header.Get("Cache-Control") == "" || res.Header.Get("Content-Type") == "" {
		t.Fatalf("headers %+v", res.Header)
	}

	head := authed(t, ts, http.MethodHead, "/files/demo/rec1/note.txt", nil)
	if head.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status %d", head.StatusCode)
	}
	if head.ContentLength != int64(len(payload)) {
		t.Fatalf("HEAD length %d", head.ContentLength)
	}
	headBody, _ := io.ReadAll(head.Body)
	head.Body.Close()
	if len(headBody) != 0 {
		t.Fatalf("HEAD body %q", headBody)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/files/demo/rec1/note.txt", nil)
	req.Header.Set("Range", "bytes=0-3")
	req.Header.Set("Authorization", "Bearer test-secret")
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body = mustStatus(t, res, http.StatusPartialContent)
	if string(body) != "abcd" {
		t.Fatalf("range body %q", body)
	}
	if res.Header.Get("Content-Range") != "bytes 0-3/36" {
		t.Fatalf("content-range %q", res.Header.Get("Content-Range"))
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/files/demo/rec1/note.txt", nil)
	req.Header.Set("Range", "bytes=100-120")
	req.Header.Set("Authorization", "Bearer test-secret")
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, res, http.StatusRequestedRangeNotSatisfiable)

	res = putFile(t, ts, "/files/demo/rec2/other.txt", []byte("zz"), nil)
	mustStatus(t, res, http.StatusCreated)

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/files/?prefix=demo/rec1/", nil)
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, res, http.StatusUnauthorized)

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/files/?prefix=demo/rec1/", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body = mustStatus(t, res, http.StatusOK)
	var listed struct {
		Items []ObjectInfo `json:"items"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 || listed.Items[0].Filename != "note.txt" {
		t.Fatalf("list %+v", listed.Items)
	}
}

func TestContentTypeAndProtectedSignedURL(t *testing.T) {
	ts, _ := newTestServer(t, 1<<20)
	js := []byte("console.log(1)")
	res := putFile(t, ts, "/files/col/rec/app.js", js, nil)
	mustStatus(t, res, http.StatusCreated)
	res = authed(t, ts, http.MethodGet, "/files/col/rec/app.js", nil)
	mustStatus(t, res, http.StatusOK)
	if ct := res.Header.Get("Content-Type"); ct != "text/javascript" {
		t.Fatalf("content type %q", ct)
	}

	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	res = putFile(t, ts, "/files/col/rec/pic.png", png, map[string]string{"X-Protected": "true"})
	mustStatus(t, res, http.StatusCreated)

	res, err := ts.Client().Get(ts.URL + "/files/col/rec/pic.png")
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, res, http.StatusUnauthorized)

	token := SignToken("test-secret", "col/rec/pic.png", time.Now().Add(time.Minute))
	res, err = ts.Client().Get(ts.URL + "/files/col/rec/pic.png?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	body := mustStatus(t, res, http.StatusOK)
	if !bytes.Equal(body, png) {
		t.Fatalf("png body %q", body)
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("cache-control %q", cc)
	}

	expired := SignToken("test-secret", "col/rec/pic.png", time.Now().Add(-time.Minute))
	res, err = ts.Client().Get(ts.URL + "/files/col/rec/pic.png?token=" + expired)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, res, http.StatusUnauthorized)

	other := SignToken("test-secret", "col/rec/other.png", time.Now().Add(time.Minute))
	res, err = ts.Client().Get(ts.URL + "/files/col/rec/pic.png?token=" + other)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, res, http.StatusUnauthorized)

	tampered := token[:len(token)-2] + "aa"
	res, err = ts.Client().Get(ts.URL + "/files/col/rec/pic.png?token=" + tampered)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, res, http.StatusUnauthorized)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/files/col/rec/pic.png", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, res, http.StatusOK)
}

func TestDeleteFileAndRecordPrefix(t *testing.T) {
	ts, store := newTestServer(t, 1<<20)
	res := putFile(t, ts, "/files/col/rec/a.txt", []byte("a"), nil)
	mustStatus(t, res, http.StatusCreated)
	res = putFile(t, ts, "/files/col/rec/b.txt", []byte("b"), nil)
	mustStatus(t, res, http.StatusCreated)
	res = putFile(t, ts, "/files/col/keep/c.txt", []byte("c"), nil)
	mustStatus(t, res, http.StatusCreated)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/files/col/rec/a.txt", nil)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, res, http.StatusUnauthorized)

	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/files/col/rec/a.txt", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, res, http.StatusNoContent)
	if _, err := store.Stat("col", "rec", "a.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stat after delete: %v", err)
	}

	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/files/col/rec/", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, res, http.StatusNoContent)
	if _, err := store.Stat("col", "rec", "b.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("record file remains: %v", err)
	}
	if _, err := store.Stat("col", "keep", "c.txt"); err != nil {
		t.Fatalf("kept file: %v", err)
	}

	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/files/col/rec/a.txt", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, res, http.StatusNoContent)
}

func TestRejectTraversalAndOversize(t *testing.T) {
	ts, store := newTestServer(t, 8)
	canaryDir := t.TempDir()
	canary := filepath.Join(canaryDir, "canary.txt")
	if err := os.WriteFile(canary, []byte("secret-canary"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/files/../" + filepath.Base(canaryDir) + "/canary.txt/x",
		"/files/col/../../etc/passwd",
		"/files/..%2F..%2Fetc/passwd/x",
		"/files/col/rec/..",
		"/files/%2e%2e/rec/file.txt",
	} {
		res := putFile(t, ts, path, []byte("pwn"), nil)
		res.Body.Close()
		if res.StatusCode == http.StatusCreated || res.StatusCode == http.StatusOK {
			t.Fatalf("path %s was stored, status %d", path, res.StatusCode)
		}
	}
	got, err := os.ReadFile(canary)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "secret-canary" {
		t.Fatalf("canary changed to %q", got)
	}

	if _, _, err := store.Put("..", "rec", "a.txt", strings.NewReader("x"), false, ""); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("put ..: %v", err)
	}
	if err := store.Delete("col", "..", "a.txt"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("delete ..: %v", err)
	}
	if _, _, err := store.List("../", "", 10); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("list traversal: %v", err)
	}

	res := putFile(t, ts, "/files/col/rec/big.txt", []byte("0123456789"), nil)
	mustStatus(t, res, http.StatusRequestEntityTooLarge)
	if _, err := store.Stat("col", "rec", "big.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oversize was stored: %v", err)
	}
}

func TestCORS(t *testing.T) {
	ts, _ := newTestServer(t, 1<<20)
	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/files/col/rec/a.txt", nil)
	req.Header.Set("Origin", "https://example.test")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("allow-origin %q", got)
	}
	if !strings.Contains(res.Header.Get("Access-Control-Allow-Methods"), "PUT") {
		t.Fatalf("methods %q", res.Header.Get("Access-Control-Allow-Methods"))
	}
}

func TestPutAtomicAndIdempotent(t *testing.T) {
	store, err := OpenStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Put("col", "rec", "a.txt", strings.NewReader("hello"), false, "text/plain"); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Put("col", "rec", "a.txt", io.MultiReader(strings.NewReader("nope"), errReader{}), false, "text/plain")
	if err == nil {
		t.Fatal("expected write error")
	}
	path, err := store.objectPath("col", "rec", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("torn write left %q", got)
	}

	meta1, created, err := store.Put("col", "rec", "a.txt", strings.NewReader("hello"), false, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second put created a new object")
	}
	meta2, _, err := store.Put("col", "rec", "a.txt", strings.NewReader("hello"), false, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if meta1.ETag != meta2.ETag {
		t.Fatalf("etag changed %s %s", meta1.ETag, meta2.ETag)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestSymlinkEscape(t *testing.T) {
	store, err := OpenStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(store.objectsDir, "col", "rec")
	if err := os.MkdirAll(filepath.Dir(linkDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), linkDir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Put("col", "rec", "outside.txt", strings.NewReader("x"), false, ""); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("symlink put: %v", err)
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside" {
		t.Fatalf("outside changed to %q", got)
	}
}

func authed(t *testing.T, ts *httptest.Server, method, path string, header map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-secret")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}
