package apis

import (
	"crypto/subtle"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"io"
	"net/http"
	"path"
	"strings"
)

// sharedFileHost serves trusted inter-instance storage operations. Record access
// checks happen at the originating instance; the shared secret grants storage access.
func sharedFileHost(e *core.RequestEvent) error {
	ba := core.AsBaseApp(e.App)
	host, err := ba.FileHost()
	if err != nil {
		return err
	}
	if host == nil || host.Mode != "full" {
		return e.NotFoundError("", nil)
	}
	id, err := ba.InstanceID()
	if err != nil {
		return err
	}
	if host.InstanceID != id {
		return e.NotFoundError("", nil)
	}
	expected := "Bearer " + host.Secret
	if host.Secret == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(e.Request.Header.Get("Authorization"))) != 1 {
		return e.UnauthorizedError("Invalid storage credentials", nil)
	}
	key := e.Request.PathValue("storagePath")
	prefix, hasPrefix := e.Request.URL.Query()["prefix"]
	valid := func(s string) bool {
		return !strings.HasPrefix(s, "/") && !strings.Contains(s, "\\") && !strings.Contains(s, "\x00") && (s == "" || strings.TrimSuffix(s, "/") == path.Clean(s)) && s != "." && s != ".." && !strings.HasPrefix(s, "../")
	}
	if !valid(key) || (hasPrefix && (len(prefix) != 1 || !valid(prefix[0]))) {
		return e.BadRequestError("Invalid storage path", nil)
	}
	fs, err := e.App.NewFilesystem()
	if err != nil {
		return err
	}
	defer fs.Close()
	fs.SetContext(e.Request.Context())
	if hasPrefix && key == "" {
		switch e.Request.Method {
		case "GET":
			rows, err := fs.List(prefix[0])
			if err != nil {
				return err
			}
			return e.JSON(200, rows)
		case "DELETE":
			if prefix[0] == "" {
				return e.BadRequestError("Nonempty prefix required", nil)
			}
			failures := fs.DeletePrefix(prefix[0])
			if len(failures) > 0 {
				return e.InternalServerError("Storage delete failed", failures[0])
			}
			return e.NoContent(204)
		}
	}
	if len(strings.Split(key, "/")) < 3 {
		return e.BadRequestError("Expected collection/recordId/filename", nil)
	}
	switch e.Request.Method {
	case "PUT":
		w, err := fs.NewWriter(key, nil)
		if err != nil {
			return err
		}
		if _, err = io.Copy(w, e.Request.Body); err != nil {
			w.Close()
			return err
		}
		if err = w.Close(); err != nil {
			return err
		}
		return e.NoContent(204)
	case "GET", "HEAD":
		return fs.Serve(e.Response, e.Request, key, path.Base(key))
	case "DELETE":
		if err = fs.Delete(key); err != nil && err != filesystem.ErrNotFound {
			return err
		}
		return e.NoContent(204)
	default:
		return e.NoContent(http.StatusMethodNotAllowed)
	}
}
