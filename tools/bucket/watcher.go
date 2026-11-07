package bucket

import (
	"context"
	"errors"
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
		log.Printf("bucket watcher: resetting cursor: %v", err)
		cursor = 0
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

func (w *Watcher) pollEvery() time.Duration {
	if w.PollEvery > 0 {
		return w.PollEvery
	}
	return 2 * time.Second
}
