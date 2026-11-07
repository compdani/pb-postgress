package filesystem

import (
	"encoding/json"
	"fmt"
	"github.com/pocketbase/pocketbase/tools/filesystem/blob"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestRemoteFileHost(t *testing.T) {
	var mu sync.Mutex
	files := map[string][]byte{}
	puts := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer shared" {
			http.Error(w, "unauthorized", 401)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/files/")
		if r.Method == "GET" && r.URL.Query().Has("prefix") {
			rows := []*blob.ListObject{}
			for k, v := range files {
				if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
					rows = append(rows, &blob.ListObject{Key: k, Size: int64(len(v))})
				}
			}
			json.NewEncoder(w).Encode(rows)
			return
		}
		switch r.Method {
		case "PUT":
			puts++
			if puts == 1 {
				http.Error(w, "retry", 503)
				return
			}
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			files[key] = b
		case "HEAD", "GET":
			b, ok := files[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", fmt.Sprint(len(b)))
			if r.Method == "GET" {
				w.Write(b)
			}
		case "DELETE":
			if _, ok := files[key]; !ok {
				http.NotFound(w, r)
				return
			}
			delete(files, key)
		default:
			http.Error(w, "bad method", 405)
		}
	}))
	defer host.Close()
	fs, err := NewRemote(host.URL, "shared")
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	if err = fs.Upload([]byte("content"), "collection/record/file.txt"); err != nil {
		t.Fatal(err)
	}
	if puts != 2 {
		t.Fatalf("PUT retries %d", puts)
	}
	if err = fs.Copy("collection/record/file.txt", "collection/record/copy.txt"); err != nil {
		t.Fatal(err)
	}
	r, err := fs.GetReader("collection/record/copy.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil || string(got) != "content" {
		t.Fatalf("read %q %v", got, err)
	}
	rows, err := fs.List("collection/record/")
	if err != nil || len(rows) != 2 {
		t.Fatalf("list %+v %v", rows, err)
	}
	if failures := fs.DeletePrefix("collection/record/"); len(failures) != 0 {
		t.Fatal(failures)
	}
	exists, err := fs.Exists("collection/record/file.txt")
	if err != nil || exists {
		t.Fatalf("exists %v %v", exists, err)
	}
}
