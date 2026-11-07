package bucket

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ObjectMeta is stored next to each object (under meta/, not in the public tree).
type ObjectMeta struct {
	Size        int64     `json:"size"`
	ContentType string    `json:"contentType"`
	ETag        string    `json:"etag"`
	Protected   bool      `json:"protected"`
	Updated     time.Time `json:"updated"`
}

// ObjectInfo is one listed object.
type ObjectInfo struct {
	Key         string    `json:"key"`
	Collection  string    `json:"collection"`
	RecordID    string    `json:"recordId"`
	Filename    string    `json:"filename"`
	Size        int64     `json:"size"`
	ContentType string    `json:"contentType"`
	ETag        string    `json:"etag"`
	Protected   bool      `json:"protected"`
	Updated     time.Time `json:"updated"`
}

// Store is a filesystem object store. Uploads are atomic (temp file + rename)
// and idempotent: repeating a PUT leaves a single complete object.
type Store struct {
	root       string
	objectsDir string
	metaDir    string
	maxBytes   int64

	mu    sync.Mutex
	cache map[string]ObjectMeta
}

// OpenStore creates the object and metadata directories inside dir.
// maxBytes <= 0 selects the default limit (100 MiB).
func OpenStore(dir string, maxBytes int64) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, os.ErrInvalid
	}
	if maxBytes <= 0 {
		maxBytes = 100 << 20
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	objects := filepath.Join(abs, "objects")
	meta := filepath.Join(abs, "meta")
	if err := os.MkdirAll(objects, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(meta, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		root:       abs,
		objectsDir: objects,
		metaDir:    meta,
		maxBytes:   maxBytes,
		cache:      map[string]ObjectMeta{},
	}
	s.cleanupTemps(objects)
	s.cleanupTemps(meta)
	return s, nil
}

// Put writes or replaces one object. created is false when an object was already there.
func (s *Store) Put(collection, recordID, filename string, r io.Reader, protected bool, declaredType string) (ObjectMeta, bool, error) {
	dest, err := s.objectPath(collection, recordID, filename)
	if err != nil {
		return ObjectMeta{}, false, err
	}

	// Stream the body into a temp file without the store lock, so a slow
	// upload does not block reads. Only the rename and metadata are locked.
	tmp, n, sum, sniff, err := writeTemp(filepath.Dir(dest), r, s.maxBytes)
	if err != nil {
		return ObjectMeta{}, false, err
	}
	defer os.Remove(tmp) // no-op after a successful rename

	s.mu.Lock()
	defer s.mu.Unlock()

	created := true
	if info, statErr := os.Lstat(dest); statErr == nil {
		if !info.Mode().IsRegular() {
			return ObjectMeta{}, false, ErrInvalidKey
		}
		created = false
	} else if !os.IsNotExist(statErr) {
		return ObjectMeta{}, false, statErr
	}
	if err := os.Rename(tmp, dest); err != nil {
		return ObjectMeta{}, false, err
	}
	syncDir(filepath.Dir(dest))
	meta := ObjectMeta{
		Size:        n,
		ContentType: contentType(filename, sniff, declaredType),
		ETag:        quoteETag(sum),
		Protected:   protected,
		Updated:     time.Now().UTC(),
	}
	if err := s.saveMeta(collection, recordID, filename, meta); err != nil {
		return ObjectMeta{}, false, err
	}
	s.cache[objectKey(collection, recordID, filename)] = meta
	return meta, created, nil
}

// Open returns the object's metadata and a reader positioned at the start.
// The caller closes the file. Metadata may be served from cache until Invalidate.
func (s *Store) Open(collection, recordID, filename string) (ObjectMeta, *os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	meta, err := s.statLocked(collection, recordID, filename)
	if err != nil {
		return ObjectMeta{}, nil, err
	}
	path, err := s.objectPath(collection, recordID, filename)
	if err != nil {
		return ObjectMeta{}, nil, err
	}
	f, err := openRegular(path)
	if err != nil {
		return ObjectMeta{}, nil, err
	}
	return meta, f, nil
}

// Stat returns metadata, refreshing it from disk when the cache was invalidated.
func (s *Store) Stat(collection, recordID, filename string) (ObjectMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statLocked(collection, recordID, filename)
}

// Delete removes one object and its metadata. Missing objects are success.
func (s *Store) Delete(collection, recordID, filename string) error {
	return s.deleteIfNotNewer(collection, recordID, filename, time.Time{})
}

// clockSkew is the tolerance between the bucket's file times and the
// created_at written by a PocketBase instance on another host.
const clockSkew = 5 * time.Second

// deleteIfNotNewer is Delete, but keeps an object updated after since.
// A zero since deletes unconditionally.
func (s *Store) deleteIfNotNewer(collection, recordID, filename string, since time.Time) error {
	path, err := s.objectPath(collection, recordID, filename)
	if err != nil {
		return err
	}
	metaPath, err := s.metaPath(collection, recordID, filename)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !since.IsZero() {
		meta, err := s.statLocked(collection, recordID, filename)
		if err == ErrNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		// The bucket's clock and the database writer's clock can differ;
		// keep the object only when it is clearly newer than the change.
		if meta.Updated.After(since.Add(clockSkew)) {
			return nil
		}
	}
	if err := removeFile(path); err != nil {
		return err
	}
	if err := removeFile(metaPath); err != nil {
		return err
	}
	delete(s.cache, objectKey(collection, recordID, filename))
	removeEmptyParents(filepath.Dir(path), s.objectsDir)
	removeEmptyParents(filepath.Dir(metaPath), s.metaDir)
	return nil
}

// DeleteRecord removes every object under collection/recordID.
func (s *Store) DeleteRecord(collection, recordID string) error {
	objDir, err := s.absUnder(s.objectsDir, collection, recordID)
	if err != nil {
		return err
	}
	metaDir, err := s.absUnder(s.metaDir, collection, recordID)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := removeTree(objDir); err != nil {
		return err
	}
	if err := removeTree(metaDir); err != nil {
		return err
	}
	prefix := collection + "/" + recordID + "/"
	for key := range s.cache {
		if strings.HasPrefix(key, prefix) {
			delete(s.cache, key)
		}
	}
	removeEmptyParents(filepath.Dir(objDir), s.objectsDir)
	removeEmptyParents(filepath.Dir(metaDir), s.metaDir)
	return nil
}

// List returns objects whose key has prefix and sorts after the after key,
// in key order, up to limit. truncated=true means more objects follow the
// last returned key; pass it as after to fetch the next page.
func (s *Store) List(prefix, after string, limit int) ([]ObjectInfo, bool, error) {
	prefix, err := cleanPrefix(prefix)
	if err != nil {
		return nil, false, err
	}
	if limit <= 0 {
		limit = 1000
	}

	// Walk without the store lock; metadata is read under it per item.
	var keys [][3]string
	err = filepath.WalkDir(s.objectsDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".tmp-") || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(s.objectsDir, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 3 {
			return nil
		}
		parts = []string{parts[0], parts[1], strings.Join(parts[2:], "/")}
		key := objectKey(parts[0], parts[1], parts[2])
		if (prefix != "" && !strings.HasPrefix(key, prefix)) || (after != "" && key <= after) {
			return nil
		}
		keys = append(keys, [3]string{parts[0], parts[1], parts[2]})
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	sort.Slice(keys, func(i, j int) bool {
		return objectKey(keys[i][0], keys[i][1], keys[i][2]) < objectKey(keys[j][0], keys[j][1], keys[j][2])
	})

	items := []ObjectInfo{}
	truncated := false
	for _, k := range keys {
		if len(items) >= limit {
			truncated = true
			break
		}
		s.mu.Lock()
		meta, err := s.statLocked(k[0], k[1], k[2])
		s.mu.Unlock()
		if err == ErrNotFound {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		items = append(items, ObjectInfo{
			Key:         objectKey(k[0], k[1], k[2]),
			Collection:  k[0],
			RecordID:    k[1],
			Filename:    k[2],
			Size:        meta.Size,
			ContentType: meta.ContentType,
			ETag:        meta.ETag,
			Protected:   meta.Protected,
			Updated:     meta.Updated,
		})
	}
	return items, truncated, nil
}

// Invalidate drops cached metadata and recomputes the ETag from the file bytes.
// A missing file clears leftover metadata.
func (s *Store) Invalidate(collection, recordID, filename string) error {
	if err := validSegment(collection); err != nil {
		return err
	}
	if err := validSegment(recordID); err != nil {
		return err
	}
	if _, err := splitName(filename); err != nil {
		return err
	}
	path, err := s.objectPath(collection, recordID, filename)
	if err != nil {
		return err
	}
	metaPath, err := s.metaPath(collection, recordID, filename)
	if err != nil {
		return err
	}
	key := objectKey(collection, recordID, filename)

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, key)

	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return removeFile(metaPath)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrInvalidKey
	}
	old, _ := s.readMeta(collection, recordID, filename)
	meta, err := hashFile(path, filename, old)
	if err != nil {
		return err
	}
	if err := s.saveMeta(collection, recordID, filename, meta); err != nil {
		return err
	}
	s.cache[key] = meta
	return nil
}

// ApplyChange reconciles one _fileChanges row.
// delete removes the object (or the whole record directory when filename is empty or "*").
// A file delete is skipped when the object was written after the change was
// logged, so a replayed or late delete cannot remove a newer upload.
// upsert invalidates the cached ETag. Unknown ops are ignored.
func (s *Store) ApplyChange(c Change) error {
	switch c.Op {
	case "delete":
		if c.Filename == "" || c.Filename == "*" {
			return s.DeleteRecord(c.Collection, c.RecordID)
		}
		return s.deleteIfNotNewer(c.Collection, c.RecordID, c.Filename, c.CreatedAt)
	case "upsert":
		if c.Filename == "" || c.Filename == "*" {
			return s.invalidateRecord(c.Collection, c.RecordID)
		}
		return s.Invalidate(c.Collection, c.RecordID, c.Filename)
	default:
		return nil
	}
}

func (s *Store) invalidateRecord(collection, recordID string) error {
	dir, err := s.absUnder(s.objectsDir, collection, recordID)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".tmp-") {
			continue
		}
		if err := s.Invalidate(collection, recordID, entry.Name()); err != nil && err != ErrInvalidKey {
			return err
		}
	}
	return nil
}

func (s *Store) LoadCursor() (int64, error) {
	b, err := os.ReadFile(s.cursorPath())
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return 0, nil
	}
	return strconv.ParseInt(text, 10, 64)
}

func (s *Store) SaveCursor(id int64) error {
	return writeFileAtomic(s.cursorPath(), []byte(strconv.FormatInt(id, 10)))
}

func (s *Store) cursorPath() string {
	return filepath.Join(s.root, ".cursor")
}

func (s *Store) statLocked(collection, recordID, filename string) (ObjectMeta, error) {
	key := objectKey(collection, recordID, filename)
	if meta, ok := s.cache[key]; ok {
		return meta, nil
	}
	path, err := s.objectPath(collection, recordID, filename)
	if err != nil {
		return ObjectMeta{}, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return ObjectMeta{}, ErrNotFound
	}
	if err != nil {
		return ObjectMeta{}, err
	}
	if !info.Mode().IsRegular() {
		return ObjectMeta{}, ErrNotFound
	}
	meta, err := s.readMeta(collection, recordID, filename)
	if err == nil && meta.ETag != "" && meta.Size == info.Size() {
		s.cache[key] = meta
		return meta, nil
	}
	meta, err = hashFile(path, filename, meta)
	if err != nil {
		return ObjectMeta{}, err
	}
	if err := s.saveMeta(collection, recordID, filename, meta); err != nil {
		return ObjectMeta{}, err
	}
	s.cache[key] = meta
	return meta, nil
}

func (s *Store) readMeta(collection, recordID, filename string) (ObjectMeta, error) {
	path, err := s.metaPath(collection, recordID, filename)
	if err != nil {
		return ObjectMeta{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ObjectMeta{}, err
	}
	var meta ObjectMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return ObjectMeta{}, err
	}
	return meta, nil
}

func (s *Store) saveMeta(collection, recordID, filename string, meta ObjectMeta) error {
	path, err := s.metaPath(collection, recordID, filename)
	if err != nil {
		return err
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

func (s *Store) objectPath(collection, recordID, filename string) (string, error) {
	parts, err := splitName(filename)
	if err != nil {
		return "", err
	}
	return s.absUnder(s.objectsDir, append([]string{collection, recordID}, parts...)...)
}

func (s *Store) metaPath(collection, recordID, filename string) (string, error) {
	parts, err := splitName(filename)
	if err != nil {
		return "", err
	}
	dirParts := append([]string{collection, recordID}, parts[:len(parts)-1]...)
	dir, err := s.absUnder(s.metaDir, dirParts...)
	if err != nil {
		return "", err
	}
	full := filepath.Join(dir, parts[len(parts)-1]+".pbmeta")
	if err := withinRoot(s.metaDir, full); err != nil {
		return "", err
	}
	return full, nil
}

func (s *Store) absUnder(root string, parts ...string) (string, error) {
	for _, part := range parts {
		if err := validSegment(part); err != nil {
			return "", err
		}
	}
	full := root
	for _, part := range parts {
		full = filepath.Join(full, part)
	}
	if err := withinRoot(root, full); err != nil {
		return "", err
	}
	cur := root
	info, err := os.Lstat(cur)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", ErrInvalidKey
	}
	for _, part := range parts {
		cur = filepath.Join(cur, part)
		info, err = os.Lstat(cur)
		if os.IsNotExist(err) {
			return full, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", ErrInvalidKey
		}
	}
	return full, nil
}

func (s *Store) cleanupTemps(root string) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".tmp-") {
			_ = os.Remove(path)
		}
		return nil
	})
}

func writeFileAtomic(path string, data []byte) error {
	_, _, _, err := writeAtomic(path, bytes.NewReader(data), int64(len(data))+1)
	return err
}

func writeAtomic(dest string, r io.Reader, maxBytes int64) (int64, string, []byte, error) {
	dir := filepath.Dir(dest)
	tmpName, n, sum, sniff, err := writeTemp(dir, r, maxBytes)
	if err != nil {
		return 0, "", nil, err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)
		return 0, "", nil, err
	}
	syncDir(dir)
	return n, sum, sniff, nil
}

// writeTemp streams r into a synced temp file in dir and returns its path.
// The caller renames or removes it.
func writeTemp(dir string, r io.Reader, maxBytes int64) (string, int64, string, []byte, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, "", nil, err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", 0, "", nil, err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	var sniff []byte
	buf := make([]byte, 32*1024)
	var n int64
	for {
		nr, er := r.Read(buf)
		if nr > 0 {
			if maxBytes > 0 && n+int64(nr) > maxBytes {
				return "", 0, "", nil, ErrTooLarge
			}
			if len(sniff) < 512 {
				take := 512 - len(sniff)
				if take > nr {
					take = nr
				}
				sniff = append(sniff, buf[:take]...)
			}
			nw, ew := tmp.Write(buf[:nr])
			_, _ = h.Write(buf[:nw])
			n += int64(nw)
			if ew != nil {
				return "", 0, "", nil, ew
			}
			if nw != nr {
				return "", 0, "", nil, io.ErrShortWrite
			}
		}
		if er == io.EOF {
			break
		}
		if er != nil {
			return "", 0, "", nil, er
		}
	}
	if err := tmp.Sync(); err != nil {
		return "", 0, "", nil, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, "", nil, err
	}
	committed = true
	return tmpName, n, hex.EncodeToString(h.Sum(nil)), sniff, nil
}

func hashFile(path, filename string, old ObjectMeta) (ObjectMeta, error) {
	f, err := openRegular(path)
	if err != nil {
		return ObjectMeta{}, err
	}
	defer f.Close()

	sniff := make([]byte, 512)
	nSniff, err := f.Read(sniff)
	if err != nil && err != io.EOF {
		return ObjectMeta{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ObjectMeta{}, err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return ObjectMeta{}, err
	}
	info, err := f.Stat()
	if err != nil {
		return ObjectMeta{}, err
	}
	updated := info.ModTime().UTC()
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	return ObjectMeta{
		Size:        n,
		ContentType: contentType(filename, sniff[:nSniff], old.ContentType),
		ETag:        quoteETag(hex.EncodeToString(h.Sum(nil))),
		Protected:   old.Protected,
		Updated:     updated,
	}, nil
}

func quoteETag(hexSum string) string {
	return `"` + hexSum + `"`
}

func openRegular(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidKey
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotFound
	}
	return os.Open(path)
}

func removeFile(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidKey
	}
	if info.IsDir() {
		return ErrInvalidKey
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func removeTree(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidKey
	}
	return os.RemoveAll(path)
}

func removeEmptyParents(dir, root string) {
	for {
		if dir == root || dir == filepath.Dir(dir) {
			return
		}
		if err := withinRoot(root, dir); err != nil {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}
