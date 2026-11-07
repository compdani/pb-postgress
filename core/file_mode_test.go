package core

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestS3ConnectionProbe(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	deletes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !strings.HasPrefix(r.URL.Path, "/bucket/.pb-probe/") {
			http.Error(w, "wrong path", 400)
			return
		}
		switch r.Method {
		case "PUT":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			objects[r.URL.Path] = body
			w.Header().Set("ETag", `"test"`)
		case "GET":
			b, ok := objects[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(b)))
			w.Write(b)
		case "DELETE":
			delete(objects, r.URL.Path)
			deletes++
			w.WriteHeader(204)
		default:
			http.Error(w, "method", 405)
		}
	}))
	defer server.Close()
	err := TestS3Connection(S3Config{Endpoint: server.URL, Bucket: "bucket", Region: "us-east-1", AccessKey: "key", Secret: "secret", ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 || deletes == 0 {
		t.Fatalf("probe not cleaned up: %d %d", len(objects), deletes)
	}
}
