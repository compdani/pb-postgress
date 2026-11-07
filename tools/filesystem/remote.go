package filesystem

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/tools/filesystem/blob"
)

// NewRemote uses the shared file host API. Credentials are sent only to that host.
func NewRemote(baseURL, secret string) (*System, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid remote file host URL")
	}
	if secret == "" {
		return nil, fmt.Errorf("remote file host secret is required")
	}
	d := &remoteDriver{base: strings.TrimRight(baseURL, "/"), secret: secret, client: &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}}
	return &System{ctx: context.Background(), bucket: blob.NewBucket(d)}, nil
}

type remoteDriver struct {
	base, secret string
	client       *http.Client
}

func (d *remoteDriver) NormalizeError(err error) error { return err }
func (d *remoteDriver) Close() error                   { return nil }
func (d *remoteDriver) endpoint(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return d.base + "/files/" + strings.Join(parts, "/")
}
func (d *remoteDriver) request(ctx context.Context, method, target string, body io.ReadSeeker, contentType, byteRange string) (*http.Response, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if body != nil {
			if _, err := body.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
		}
		var reader io.Reader
		if body != nil {
			reader = struct{ io.Reader }{body}
		}
		req, err := http.NewRequestWithContext(ctx, method, target, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+d.secret)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if byteRange != "" {
			req.Header.Set("Range", byteRange)
		}
		res, err := d.client.Do(req)
		retry := err != nil || res.StatusCode == 429 || res.StatusCode >= 500
		if retry {
			if res != nil {
				res.Body.Close()
			}
			if attempt == 2 {
				return nil, fmt.Errorf("remote file host %s failed after retries", method)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
			}
			continue
		}
		if res.StatusCode == 404 {
			res.Body.Close()
			return nil, blob.ErrNotFound
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			res.Body.Close()
			return nil, fmt.Errorf("remote file host %s returned HTTP %d", method, res.StatusCode)
		}
		return res, nil
	}
	return nil, fmt.Errorf("remote file host unavailable")
}
func remoteAttrs(r *http.Response) *blob.Attributes {
	t, _ := http.ParseTime(r.Header.Get("Last-Modified"))
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	return &blob.Attributes{Size: r.ContentLength, ContentType: ct, ModTime: t, ETag: r.Header.Get("ETag")}
}
func (d *remoteDriver) Attributes(ctx context.Context, key string) (*blob.Attributes, error) {
	r, e := d.request(ctx, "HEAD", d.endpoint(key), nil, "", "")
	if e != nil {
		return nil, e
	}
	defer r.Body.Close()
	return remoteAttrs(r), nil
}

type remoteReader struct {
	io.ReadCloser
	attrs blob.ReaderAttributes
}

func (r *remoteReader) Attributes() *blob.ReaderAttributes { return &r.attrs }
func (d *remoteDriver) NewRangeReader(ctx context.Context, key string, offset, length int64) (blob.DriverReader, error) {
	byteRange := ""
	if offset > 0 || length >= 0 {
		if length == 0 {
			return &remoteReader{ReadCloser: io.NopCloser(strings.NewReader(""))}, nil
		}
		byteRange = fmt.Sprintf("bytes=%d-", offset)
		if length > 0 {
			byteRange += fmt.Sprint(offset + length - 1)
		}
	}
	r, e := d.request(ctx, "GET", d.endpoint(key), nil, "", byteRange)
	if e != nil {
		return nil, e
	}
	a := remoteAttrs(r)
	if contentRange := r.Header.Get("Content-Range"); contentRange != "" {
		if i := strings.LastIndex(contentRange, "/"); i >= 0 {
			if size, err := strconv.ParseInt(contentRange[i+1:], 10, 64); err == nil {
				a.Size = size
			}
		}
	}
	var reader io.ReadCloser = r.Body
	if byteRange != "" && r.StatusCode != http.StatusPartialContent {
		if _, e = io.CopyN(io.Discard, r.Body, offset); e != nil {
			r.Body.Close()
			return nil, e
		}
		if length >= 0 {
			reader = &limitedRemoteReader{Reader: io.LimitReader(r.Body, length), Closer: r.Body}
		}
	}
	return &remoteReader{ReadCloser: reader, attrs: blob.ReaderAttributes{ContentType: a.ContentType, Size: a.Size, ModTime: a.ModTime}}, nil
}

type limitedRemoteReader struct {
	io.Reader
	io.Closer
}
type remoteWriter struct {
	*os.File
	d                *remoteDriver
	ctx              context.Context
	key, contentType string
}

func (w *remoteWriter) Close() error {
	defer os.Remove(w.File.Name())
	defer w.File.Close()
	r, e := w.d.request(w.ctx, "PUT", w.d.endpoint(w.key), w.File, w.contentType, "")
	if r != nil {
		r.Body.Close()
	}
	return e
}
func (d *remoteDriver) NewTypedWriter(ctx context.Context, key, ct string, opts *blob.WriterOptions) (blob.DriverWriter, error) {
	f, e := os.CreateTemp("", "pb-remote-upload-*")
	if e != nil {
		return nil, e
	}
	return &remoteWriter{File: f, d: d, ctx: ctx, key: key, contentType: ct}, nil
}
func (d *remoteDriver) Delete(ctx context.Context, key string) error {
	r, e := d.request(ctx, "DELETE", d.endpoint(key), nil, "", "")
	if r != nil {
		r.Body.Close()
	}
	return e
}
func (d *remoteDriver) Copy(ctx context.Context, dst, src string) error {
	r, e := d.NewRangeReader(ctx, src, 0, -1)
	if e != nil {
		return e
	}
	defer r.Close()
	w, e := d.NewTypedWriter(ctx, dst, r.Attributes().ContentType, &blob.WriterOptions{})
	if e != nil {
		return e
	}
	if _, e = io.Copy(w, r); e != nil {
		rw := w.(*remoteWriter)
		rw.File.Close()
		os.Remove(rw.File.Name())
		return e
	}
	return w.Close()
}

// The host returns a JSON array of blob.ListObject (keys relative to /files/).
func (d *remoteDriver) ListPaged(ctx context.Context, opts *blob.ListOptions) (*blob.ListPage, error) {
	r, e := d.request(ctx, "GET", d.base+"/files/?prefix="+url.QueryEscape(opts.Prefix), nil, "", "")
	if e != nil {
		return nil, e
	}
	defer r.Body.Close()
	var rows []*blob.ListObject
	if e = json.NewDecoder(r.Body).Decode(&rows); e != nil {
		return nil, e
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return &blob.ListPage{Objects: rows}, nil
}
