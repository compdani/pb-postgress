package bucket

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Server is the HTTP API other PocketBase instances use to read and write bytes.
// Every call needs Authorization: Bearer <secret>. GET and HEAD also accept a
// signed ?token= for one key (see SignToken). PocketBase enforces record and
// field access rules before it proxies a download, so the bucket never serves
// objects anonymously.
type Server struct {
	store    *Store
	apiKey   string
	maxBytes int64
	pageSize int
	cors     corsPolicy
}

// Options configures the HTTP server. APIKey is the bearer secret.
type Options struct {
	APIKey      string
	MaxBytes    int64
	CORSOrigins []string
	// ListPageSize caps one list response. Non-positive means 1000.
	ListPageSize int
}

// NewServer serves store. A non-positive MaxBytes uses the store limit.
func NewServer(store *Store, opts Options) *Server {
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 && store != nil {
		maxBytes = store.maxBytes
	}
	if maxBytes <= 0 {
		maxBytes = 100 << 20
	}
	return &Server{
		store:    store,
		apiKey:   opts.APIKey,
		maxBytes: maxBytes,
		pageSize: opts.ListPageSize,
		cors:     newCORS(opts.CORSOrigins),
	}
}

// Handler is the bucket HTTP handler.
//
//	GET  /healthz
//	PUT  /files/{collection}/{recordId}/{filename...}
//	GET  /files/{collection}/{recordId}/{filename...}
//	HEAD /files/{collection}/{recordId}/{filename...}
//	DELETE /files/{collection}/{recordId}/{filename...}
//	DELETE /files/{collection}/{recordId}/
//	GET  /files/?prefix=&after=
//
// {filename...} may contain nested segments, e.g. thumbs_a.png/100x100_a.png.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /files/{$}", s.list)
	mux.HandleFunc("GET /files", s.list)
	mux.HandleFunc("PUT /files/{collection}/{recordId}/{filename...}", s.put)
	mux.HandleFunc("GET /files/{collection}/{recordId}/{filename...}", s.download)
	mux.HandleFunc("DELETE /files/{rest...}", s.del)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.cors.apply(w, r)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) put(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized"))
		return
	}
	if s.maxBytes > 0 && r.ContentLength > s.maxBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errBody("object exceeds size limit"))
		return
	}
	body := http.MaxBytesReader(w, r.Body, s.maxBytes)
	defer body.Close()

	meta, created, err := s.store.Put(
		r.PathValue("collection"),
		r.PathValue("recordId"),
		r.PathValue("filename"),
		body,
		protectedRequest(r),
		r.Header.Get("Content-Type"),
	)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, fileBody(r.PathValue("collection"), r.PathValue("recordId"), r.PathValue("filename"), meta))
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	recordID := r.PathValue("recordId")
	filename := r.PathValue("filename")

	if !s.allowRead(r, objectKey(collection, recordID, filename)) {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized"))
		return
	}

	meta, f, err := s.store.Open(collection, recordID, filename)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	defer f.Close()

	disposition := "attachment"
	force, _ := strconv.ParseBool(r.URL.Query().Get("download"))
	if !force && inlineContent(meta.ContentType) {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", disposition+"; filename="+strconv.Quote(safeDisposition(path.Base(filename))))
	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; media-src 'self'; style-src 'unsafe-inline'; sandbox")
	if meta.Protected {
		w.Header().Set("Cache-Control", "private, no-store")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=2592000, stale-while-revalidate=86400")
	}
	http.ServeContent(w, r, path.Base(filename), meta.Updated, f)
}

func (s *Server) del(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized"))
		return
	}
	parts := splitRest(r.PathValue("rest"))
	var err error
	switch len(parts) {
	case 2:
		err = s.store.DeleteRecord(parts[0], parts[1])
	case 0, 1:
		writeJSON(w, http.StatusBadRequest, errBody("invalid object key"))
		return
	default:
		err = s.store.Delete(parts[0], parts[1], strings.Join(parts[2:], "/"))
	}
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized"))
		return
	}
	prefix := r.URL.Query().Get("prefix")
	items, truncated, err := s.store.List(prefix, r.URL.Query().Get("after"), s.pageSize)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"prefix":    prefix,
		"items":     items,
		"truncated": truncated,
	})
}

func (s *Server) authorized(r *http.Request) bool {
	return bearerMatch(r.Header.Get("Authorization"), s.apiKey)
}

func (s *Server) allowRead(r *http.Request, key string) bool {
	if s.authorized(r) {
		return true
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		return false
	}
	return VerifyToken(s.apiKey, key, token, time.Now()) == nil
}

func bearerMatch(header, secret string) bool {
	const prefix = "Bearer "
	if secret == "" || len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	got := strings.TrimSpace(header[len(prefix):])
	if len(got) != len(secret) {
		return false
	}
	var diff byte
	for i := 0; i < len(secret); i++ {
		diff |= got[i] ^ secret[i]
	}
	return diff == 0
}

func protectedRequest(r *http.Request) bool {
	v := strings.TrimSpace(r.Header.Get("X-Protected"))
	if v == "" {
		v = r.URL.Query().Get("protected")
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func splitRest(rest string) []string {
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return nil
	}
	return strings.Split(rest, "/")
}

func safeDisposition(name string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
}

func fileBody(collection, recordID, filename string, meta ObjectMeta) map[string]any {
	return map[string]any{
		"key":         objectKey(collection, recordID, filename),
		"collection":  collection,
		"recordId":    recordID,
		"filename":    filename,
		"size":        meta.Size,
		"contentType": meta.ContentType,
		"etag":        meta.ETag,
		"protected":   meta.Protected,
		"updated":     meta.Updated,
	}
}

func writeStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidKey):
		writeJSON(w, http.StatusBadRequest, errBody("invalid object key"))
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody("not found"))
	case errors.Is(err, ErrTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, errBody("object exceeds size limit"))
	default:
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody("object exceeds size limit"))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errBody("storage error"))
	}
}

func errBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

type corsPolicy struct {
	allowAll bool
	origins  map[string]struct{}
}

func newCORS(origins []string) corsPolicy {
	if len(origins) == 0 {
		return corsPolicy{allowAll: true}
	}
	p := corsPolicy{origins: map[string]struct{}{}}
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		if origin == "*" {
			return corsPolicy{allowAll: true}
		}
		p.origins[origin] = struct{}{}
	}
	if len(p.origins) == 0 {
		p.allowAll = true
	}
	return p
}

func (p corsPolicy) apply(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	allow := ""
	if p.allowAll {
		allow = "*"
	} else if origin != "" {
		if _, ok := p.origins[origin]; ok {
			allow = origin
		}
	}
	if allow == "" {
		return
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", allow)
	if allow != "*" {
		h.Add("Vary", "Origin")
	}
	h.Set("Access-Control-Allow-Methods", "GET, HEAD, PUT, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Protected, Range")
	h.Set("Access-Control-Expose-Headers", "Accept-Ranges, Content-Length, Content-Range, Content-Type, ETag")
	h.Set("Access-Control-Max-Age", "600")
}

// FileURL is the path a client uses to fetch an object, including an optional token.
func FileURL(collection, recordID, filename, token string) string {
	u := "/files/" + url.PathEscape(collection) + "/" + url.PathEscape(recordID) + "/" + url.PathEscape(filename)
	if token != "" {
		u += "?token=" + url.QueryEscape(token)
	}
	return u
}
