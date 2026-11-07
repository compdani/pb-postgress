package bucket

import (
	"bytes"
	"context"
	"errors"
	"os"
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
