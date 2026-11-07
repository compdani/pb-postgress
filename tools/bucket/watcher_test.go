package bucket

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	mu   sync.Mutex
	rows []Change
	ch   chan struct{}
}

func (f *fakeSource) FetchAfter(ctx context.Context, afterID int64, limit int) ([]Change, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Change
	for _, row := range f.rows {
		if row.ID > afterID {
			out = append(out, row)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	if out == nil {
		out = []Change{}
	}
	return out, nil
}

func (f *fakeSource) Notify(ctx context.Context) <-chan struct{} {
	return f.ch
}

func TestWatcherPrunesDeletesAndSkipsBadKeys(t *testing.T) {
	store, err := OpenStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Put("col", "rec", "a.txt", bytes.NewReader([]byte("a")), false, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Put("col", "rec", "b.txt", bytes.NewReader([]byte("b")), false, "text/plain"); err != nil {
		t.Fatal(err)
	}

	src := &fakeSource{ch: make(chan struct{}, 1), rows: []Change{
		{ID: 1, Op: "delete", Collection: "..", RecordID: "rec", Filename: "a.txt"},
		{ID: 2, Op: "delete", Collection: "col", RecordID: "rec", Filename: "a.txt"},
		{ID: 3, Op: "delete", Collection: "col", RecordID: "rec", Filename: ""},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- (&Watcher{Source: src, Store: store, PollEvery: time.Hour}).Run(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, errA := store.Stat("col", "rec", "a.txt")
		_, errB := store.Stat("col", "rec", "b.txt")
		cursor, _ := store.LoadCursor()
		if errors.Is(errA, ErrNotFound) && errors.Is(errB, ErrNotFound) && cursor == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop")
	}
	if _, err := store.Stat("col", "rec", "b.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("record dir was not pruned: %v", err)
	}
	cursor, err := store.LoadCursor()
	if err != nil {
		t.Fatal(err)
	}
	if cursor != 3 {
		t.Fatalf("cursor %d", cursor)
	}
}

func TestUpsertInvalidatesStaleETag(t *testing.T) {
	store, err := OpenStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := store.Put("col", "rec", "a.txt", bytes.NewReader([]byte("hello")), true, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	stale := meta
	stale.ETag = `"stale"`
	if err := store.saveMeta("col", "rec", "a.txt", stale); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.cache[objectKey("col", "rec", "a.txt")] = stale
	store.mu.Unlock()

	if err := store.ApplyChange(Change{
		ID:         7,
		Op:         "upsert",
		Collection: "col",
		RecordID:   "rec",
		Filename:   "a.txt",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Stat("col", "rec", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got.ETag == `"stale"` || got.ETag == "" {
		t.Fatalf("etag was not recomputed: %s", got.ETag)
	}
	if !got.Protected {
		t.Fatal("protected flag was dropped")
	}
	if _, err := os.Stat(mustMeta(t, store, "col", "rec", "a.txt")); err != nil {
		t.Fatal(err)
	}
}

func mustMeta(t *testing.T, store *Store, collection, recordID, filename string) string {
	t.Helper()
	path, err := store.metaPath(collection, recordID, filename)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestApplyDeleteKeepsNewerObject(t *testing.T) {
	store, err := OpenStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	logged := time.Now().Add(-time.Minute)
	if _, _, err := store.Put("col", "rec", "a.txt", bytes.NewReader([]byte("new")), false, ""); err != nil {
		t.Fatal(err)
	}
	// A delete logged before the current upload must not remove it.
	if err := store.ApplyChange(Change{ID: 1, Op: "delete", Collection: "col", RecordID: "rec", Filename: "a.txt", CreatedAt: logged}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat("col", "rec", "a.txt"); err != nil {
		t.Fatalf("newer object deleted: %v", err)
	}
	// A delete logged after the upload removes it.
	if err := store.ApplyChange(Change{ID: 2, Op: "delete", Collection: "col", RecordID: "rec", Filename: "a.txt", CreatedAt: time.Now().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat("col", "rec", "a.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale object kept: %v", err)
	}
}

func TestWatcherWaitsForUncommittedGap(t *testing.T) {
	store, err := OpenStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{rows: []Change{
		{ID: 1, Op: "upsert", Collection: "col", RecordID: "rec", Filename: "a.txt", CreatedAt: time.Now()},
		// id 2 is allocated but not committed yet
		{ID: 3, Op: "upsert", Collection: "col", RecordID: "rec", Filename: "b.txt", CreatedAt: time.Now()},
	}}
	w := &Watcher{Source: src, Store: store, GapGrace: time.Hour}
	cursor := int64(0)
	if err := w.drain(context.Background(), &cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != 1 {
		t.Fatalf("cursor %d moved past an uncommitted gap", cursor)
	}

	// Once id 2 commits, the watcher continues in order.
	src.mu.Lock()
	src.rows = append(src.rows, Change{ID: 2, Op: "upsert", Collection: "col", RecordID: "rec", Filename: "c.txt", CreatedAt: time.Now()})
	sort.Slice(src.rows, func(i, j int) bool { return src.rows[i].ID < src.rows[j].ID })
	src.mu.Unlock()
	if err := w.drain(context.Background(), &cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != 3 {
		t.Fatalf("cursor %d, want 3", cursor)
	}

	// An old gap (a rolled-back id) does not block forever.
	src.mu.Lock()
	src.rows = append(src.rows, Change{ID: 5, Op: "upsert", Collection: "col", RecordID: "rec", Filename: "d.txt", CreatedAt: time.Now().Add(-2 * time.Hour)})
	src.mu.Unlock()
	if err := w.drain(context.Background(), &cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != 5 {
		t.Fatalf("cursor %d, want 5", cursor)
	}
}
