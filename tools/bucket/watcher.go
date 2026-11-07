package bucket

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

// Change is one row from the _fileChanges log written by a PocketBase instance.
type Change struct {
	ID         int64
	Op         string
	Collection string
	RecordID   string
	Filename   string
	CreatedAt  time.Time
}

// ChangeSource is the durable reconciliation feed.
// FetchAfter returns rows with id greater than afterID, ordered by id.
// Notify may hint that new rows exist. A nil channel means the caller only polls.
type ChangeSource interface {
	FetchAfter(ctx context.Context, afterID int64, limit int) ([]Change, error)
	Notify(ctx context.Context) <-chan struct{}
}

// Watcher applies _fileChanges rows to a Store. Postgres can wake it with
// LISTEN/NOTIFY; MySQL and a quiet Postgres connection rely on polling.
type Watcher struct {
	Source    ChangeSource
	Store     *Store
	PollEvery time.Duration
	// GapGrace is how long a missing id may hold back newer rows. Ids are
	// allocated at insert but become visible at commit, so a row behind a gap
	// may belong to a transaction that has not committed yet. The wait runs
	// from when this watcher first saw the gap (and at least GapGrace after
	// the next row's created_at); after it the gap is treated as a rollback.
	// A transaction open longer than that is still skipped; PocketBase's own
	// delete queue covers deletes in that case. Non-positive means 30s.
	GapGrace time.Duration

	gapAt    int64     // cursor value the current gap follows
	gapSince time.Time // when that gap was first seen
}

// Run consumes the feed until ctx is cancelled. Apply errors other than
// ErrInvalidKey are retried. Invalid keys are skipped so one bad row cannot
// stall the cursor.
func (w *Watcher) Run(ctx context.Context) error {
	if w.Source == nil || w.Store == nil {
		return errors.New("watcher is missing a source or store")
	}
	cursor, err := w.Store.LoadCursor()
	if err != nil {
		// Replaying the whole log from 0 is not safe to do silently.
		return fmt.Errorf("read watcher cursor (fix or remove %s to replay the log): %w", w.Store.cursorPath(), err)
	}
	notify := w.Source.Notify(ctx)
	poll := time.NewTicker(w.pollEvery())
	defer poll.Stop()

	for {
		if err := w.drain(ctx, &cursor); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("bucket watcher: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notify:
		case <-poll.C:
		}
	}
}

func (w *Watcher) drain(ctx context.Context, cursor *int64) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, err := w.Source.FetchAfter(ctx, *cursor, 100)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, change := range batch {
			if change.ID > *cursor+1 && w.waitForGap(*cursor, change) {
				// An earlier id may still commit. Wait for it (a commit
				// notifies, and polling retries) before moving past it.
				return nil
			}
			if err := w.Store.ApplyChange(change); err != nil {
				if errors.Is(err, ErrInvalidKey) {
					log.Printf("bucket watcher: skip change %d: %v", change.ID, err)
				} else {
					return err
				}
			}
			*cursor = change.ID
			if err := w.Store.SaveCursor(*cursor); err != nil {
				return err
			}
		}
	}
}

// waitForGap reports whether the watcher should keep waiting at cursor for
// the ids missing before change.
func (w *Watcher) waitForGap(cursor int64, change Change) bool {
	now := time.Now()
	if w.gapSince.IsZero() || w.gapAt != cursor {
		w.gapAt, w.gapSince = cursor, now
	}
	grace := w.gapGrace()
	if now.Sub(w.gapSince) < grace {
		return true
	}
	return !change.CreatedAt.IsZero() && now.Sub(change.CreatedAt) < grace
}

func (w *Watcher) gapGrace() time.Duration {
	if w.GapGrace > 0 {
		return w.GapGrace
	}
	return 30 * time.Second
}

func (w *Watcher) pollEvery() time.Duration {
	if w.PollEvery > 0 {
		return w.PollEvery
	}
	return 2 * time.Second
}
