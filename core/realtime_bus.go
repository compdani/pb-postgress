package core

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/security"
)

// The realtime bus carries the record events of one instance to the realtime
// clients of the others.
//
// A transaction that writes records buffers a thin event (collection id,
// record id, action) for each of them and, as its last statements, inserts them
// into _realtimeEvents, numbered with a per-process sequence. Every process has
// exactly one write connection, so its transactions run, and commit, one after
// the other: for each process the sequence is therefore in commit order, and no
// lock shared with the other instances is needed (a global lock would
// serialize the commits of the whole cluster). Every other instance polls the
// events of each live process after its own cursor, reloads the record and
// broadcasts it to its own clients with its own rules (never before the
// collections cache has caught up with the database: an event is held back, and
// retried on the next poll, while a collections change is not published yet). The instance that wrote the record already broadcast it locally and
// doesn't read its own events.
//
// The bus is best effort: a failure to publish never fails the write. It
// signals a gap instead (a bump of the shared "realtimeGap" version), which makes
// every instance resynchronize its clients' auth. Events about auth records are
// never dropped by the per transaction cap, and a periodic sweep re-validates
// the authenticated realtime clients independently of the bus.
const (
	realtimeEventsTable = "_realtimeEvents"
	realtimeBootsTable  = "_realtimeBoots"

	cacheVersionRealtimeGap = "realtimeGap"

	realtimeActionCreate = "create"
	realtimeActionUpdate = "update"
	realtimeActionDelete = "delete"
)

// realtimeBusAfterRead is a test hook, called after a page of events was read and
// before it is dispatched (see SetRealtimeBusAfterReadHook).
var realtimeBusAfterRead atomic.Pointer[func()]

// SetRealtimeBusAfterReadHook sets (nil removes) a func called by the realtime bus
// consumers after a page of events was read and before it is dispatched. For tests.
func SetRealtimeBusAfterReadHook(fn func()) {
	if fn == nil {
		realtimeBusAfterRead.Store(nil)
		return
	}
	realtimeBusAfterRead.Store(&fn)
}

// StoreKeyRealtimeDispatcher can hold a [RealtimeDispatcherFunc]: it receives
// the events of the other instances.
const StoreKeyRealtimeDispatcher = "pbRealtimeDispatcher"

// StoreKeyRealtimeResync can hold a func(App) that is called when events may
// have been lost (a gap): it should re-validate the auth of the realtime clients.
const StoreKeyRealtimeResync = "pbRealtimeResync"

// StoreKeyRealtimeAuthSweep can hold a func(App) that is called periodically
// to re-validate the auth of the realtime clients.
const StoreKeyRealtimeAuthSweep = "pbRealtimeAuthSweep"

// RealtimeDispatcherFunc handles a record event that another instance published.
type RealtimeDispatcherFunc func(app App, collectionId, recordId, action string)

var (
	// RealtimeBusInterval is how often the events table is polled.
	RealtimeBusInterval = 250 * time.Millisecond

	// RealtimeBusRetention is how long published events are kept.
	RealtimeBusRetention = 10 * time.Minute

	// RealtimeBusMaxEventsPerTx caps the non-auth events of one transaction
	// (above it the transaction signals a gap instead).
	RealtimeBusMaxEventsPerTx = 1000

	// RealtimeAuthSweepInterval is the default interval of the auth sweep
	// (the PB_REALTIME_AUTH_SWEEP environment variable overrides it, in seconds, min 5).
	RealtimeAuthSweepInterval = 30 * time.Second
)

type realtimeBusEvent struct {
	collectionId string
	recordId     string
	action       string
}

// InitRealtimeBus creates the tables of the realtime bus.
func InitRealtimeBus(app App) error {
	ba := asBaseApp(app)

	if ba.SQLDialect() == DialectMySQL {
		return ExecuteSchema(app, "CREATE TABLE IF NOT EXISTS {{_realtimeEvents}} ("+
			"[[boot]] VARCHAR(64) NOT NULL, "+
			"[[seq]] BIGINT NOT NULL, "+
			"[[collection_id]] VARCHAR(255) NOT NULL, "+
			"[[record_id]] VARCHAR(255) NOT NULL, "+
			"[[action]] VARCHAR(16) NOT NULL, "+
			"[[created_at]] DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), "+
			"PRIMARY KEY ([[boot]], [[seq]]));"+
			"CREATE INDEX IF NOT EXISTS idx_realtime_events_created ON {{_realtimeEvents}} ([[created_at]]);"+
			"CREATE TABLE IF NOT EXISTS {{_realtimeBoots}} ("+
			"[[boot]] VARCHAR(64) NOT NULL PRIMARY KEY, "+
			"[[beat]] DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6))")
	}

	return ExecuteSchema(app, "CREATE TABLE IF NOT EXISTS {{_realtimeEvents}} ("+
		"[[boot]] VARCHAR(64) NOT NULL, "+
		"[[seq]] BIGINT NOT NULL, "+
		"[[collection_id]] VARCHAR(255) NOT NULL, "+
		"[[record_id]] VARCHAR(255) NOT NULL, "+
		"[[action]] VARCHAR(16) NOT NULL, "+
		"[[created_at]] TIMESTAMPTZ NOT NULL DEFAULT NOW(), "+
		"PRIMARY KEY ([[boot]], [[seq]]));"+
		"CREATE INDEX IF NOT EXISTS idx_realtime_events_created ON {{_realtimeEvents}} ([[created_at]]);"+
		"CREATE TABLE IF NOT EXISTS {{_realtimeBoots}} ("+
		"[[boot]] VARCHAR(64) NOT NULL PRIMARY KEY, "+
		"[[beat]] TIMESTAMPTZ NOT NULL DEFAULT NOW())")
}

func (app *BaseApp) realtimeBusEnabled() bool {
	return app.config.RealtimeBus && app.cacheSync != nil
}

// bufferRealtimeEvent records an event for a record written inside the
// current transaction (published when the outermost transaction ends).
func (app *BaseApp) bufferRealtimeEvent(model Model, action string) {
	if !app.realtimeBusEnabled() || app.txInfo == nil || app.txInfo.isForAuxDB {
		return
	}

	var record *Record
	switch m := model.(type) {
	case *Record:
		record = m
	case RecordProxy:
		record = m.ProxyRecord()
	}
	if record == nil || record.Collection() == nil {
		return
	}

	app.txInfo.addRealtimeEvent(record.Collection().Id, record.Id, action, record.Collection().IsAuth())
}

// addRealtimeEvent buffers an event, collapsing duplicates. Events of auth
// collections are never dropped by the cap (they carry the revocations).
func (a *TxAppInfo) addRealtimeEvent(collectionId, recordId, action string, auth bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	key := collectionId + "\x00" + recordId + "\x00" + action
	if _, ok := a.realtimeIndex[key]; ok {
		return
	}

	if !auth && a.realtimeNonAuth >= RealtimeBusMaxEventsPerTx {
		a.realtimeOverflow = true
		return
	}

	if a.realtimeIndex == nil {
		a.realtimeIndex = map[string]struct{}{}
	}
	a.realtimeIndex[key] = struct{}{}
	if !auth {
		a.realtimeNonAuth++
	}

	a.realtimeEvents = append(a.realtimeEvents, realtimeBusEvent{collectionId: collectionId, recordId: recordId, action: action})
}

func (a *TxAppInfo) takeRealtimeEvents() (events []realtimeBusEvent, overflow bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	events, overflow = a.realtimeEvents, a.realtimeOverflow
	a.realtimeEvents, a.realtimeIndex, a.realtimeNonAuth, a.realtimeOverflow = nil, nil, 0, false

	return events, overflow
}

// flushRealtimeEvents publishes the buffered events inside the transaction, as its
// last statements. It reports whether events may have been lost (a gap must
// be signalled once the transaction committed). It fails only when the
// transaction itself is unusable.
func (app *BaseApp) flushRealtimeEvents(tx dbx.Builder, events []realtimeBusEvent, overflow bool) (gap bool, err error) {
	cs := app.cacheSync
	if cs == nil || (len(events) == 0 && !overflow) {
		return false, nil
	}

	gap = overflow

	if len(events) == 0 {
		return gap, nil
	}

	const savepoint = "pb_rt_flush"

	if _, spErr := tx.NewQuery("SAVEPOINT " + savepoint).Execute(); spErr != nil {
		// a callee that already ended the transaction itself (e.g. the deprecated
		// dry submit rolls back manually) has nothing left to publish
		if errors.Is(spErr, sql.ErrTxDone) {
			return false, nil
		}

		// publishing is best effort: the events are dropped and a gap is signalled
		// once (and if) the transaction commits; a broken transaction fails on its own
		app.Logger().Warn("Failed to create the realtime bus savepoint", "error", spErr)
		return true, nil
	}

	if publishErr := app.publishRealtimeEvents(tx, events, cs); publishErr != nil {
		app.Logger().Warn("Failed to publish the realtime events", "events", len(events), "error", publishErr)

		gap = true

		if _, rbErr := tx.NewQuery("ROLLBACK TO SAVEPOINT " + savepoint).Execute(); rbErr != nil {
			return gap, errors.Join(publishErr, rbErr)
		}
	}

	if _, relErr := tx.NewQuery("RELEASE SAVEPOINT " + savepoint).Execute(); relErr != nil {
		app.Logger().Debug("Failed to release the realtime bus savepoint", "error", relErr)
	}

	return gap, nil
}

// publishRealtimeEvents inserts the events with consecutive numbers of this process.
//
// The process has a single write connection, so no other transaction of it can
// be between its numbering and its commit: the numbers are in commit order.
func (app *BaseApp) publishRealtimeEvents(tx dbx.Builder, events []realtimeBusEvent, cs *cacheSync) error {
	const chunkSize = 200

	for start := 0; start < len(events); start += chunkSize {
		end := min(start+chunkSize, len(events))

		var sb strings.Builder
		sb.WriteString("INSERT INTO {{_realtimeEvents}} ([[boot]], [[seq]], [[collection_id]], [[record_id]], [[action]]) VALUES ")

		params := dbx.Params{"boot": cs.boot()}
		for i, e := range events[start:end] {
			if i > 0 {
				sb.WriteString(", ")
			}
			n := strconv.Itoa(i)
			sb.WriteString("({:boot}, {:s" + n + "}, {:c" + n + "}, {:r" + n + "}, {:a" + n + "})")
			params["s"+n] = cs.realtimeSeq.Add(1)
			params["c"+n] = e.collectionId
			params["r"+n] = e.recordId
			params["a"+n] = e.action
		}

		if _, err := tx.NewQuery(sb.String()).Bind(params).Execute(); err != nil {
			return err
		}
	}

	// note: no NOTIFY. It would wake the other instances sooner, but a NOTIFY in a
	// transaction makes postgres serialize the commits of all such transactions on a
	// global lock (measured: it cut the commit throughput of four writing
	// instances by more than half). The consumers poll instead.
	return nil
}

// signalRealtimeGap tells the other instances that events may have been lost.
// A failure is remembered and retried by the consumer loop.
func (app *BaseApp) signalRealtimeGap() {
	cs := app.cacheSync
	if cs == nil {
		return
	}

	if err := app.writeCacheVersions(app.NonconcurrentDB(), []string{cacheVersionRealtimeGap}); err != nil {
		cs.gapPending.Store(true)
		app.Logger().Warn("Failed to signal a realtime gap", "error", err)
	}
}

// ---------------------------------------------------------------------

// registerRealtimeBoot lists this process in _realtimeBoots, so the other instances poll its events.
func (s *cacheSync) registerRealtimeBoot() error {
	now := "NOW()"
	if s.app.SQLDialect() == DialectMySQL {
		now = "CURRENT_TIMESTAMP(6)"
	}

	_, err := s.app.NonconcurrentDB().
		NewQuery("INSERT INTO {{_realtimeBoots}} ([[boot]], [[beat]]) VALUES ({:boot}, " + now + ")").
		Bind(dbx.Params{"boot": s.boot()}).
		Execute()

	return err
}

func (s *cacheSync) unregisterRealtimeBoot() {
	if !s.app.realtimeBusEnabled() || !s.app.IsBootstrapped() {
		return
	}

	_, _ = s.app.NonconcurrentDB().
		NewQuery("DELETE FROM {{_realtimeBoots}} WHERE [[boot]] = {:boot}").
		Bind(dbx.Params{"boot": s.boot()}).
		Execute()
}

func (s *cacheSync) heartbeatRealtimeBoot() {
	now := "NOW()"
	if s.app.SQLDialect() == DialectMySQL {
		now = "CURRENT_TIMESTAMP(6)"
	}

	if res, err := s.app.NonconcurrentDB().
		NewQuery("UPDATE {{_realtimeBoots}} SET [[beat]] = " + now + " WHERE [[boot]] = {:boot}").
		Bind(dbx.Params{"boot": s.boot()}).
		Execute(); err == nil {
		if n, _ := res.RowsAffected(); n == 0 {
			_ = s.registerRealtimeBoot() // pruned while this process was unreachable
		}
	}
}

// runRealtime consumes the events published by the other processes.
func (s *cacheSync) runRealtime(ctx context.Context) {
	// the events that exist now are history: only newer ones are dispatched
	cursors := map[string]int64{}
	s.discoverRealtimeBoots(cursors, true)

	gapSeen, _ := s.realtimeGapVersion()
	lastProgress := time.Now()
	var behindSince time.Time

	poll := time.NewTicker(RealtimeBusInterval)
	defer poll.Stop()

	maintenance := time.NewTicker(time.Minute)
	defer maintenance.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-maintenance.C:
			s.heartbeatRealtimeBoot()
			s.pruneRealtime()
			continue
		case <-poll.C:
		}

		if ctx.Err() != nil || !s.app.IsBootstrapped() {
			return
		}

		if s.gapPending.Load() {
			s.gapPending.Store(false)
			s.app.signalRealtimeGap()
		}

		// events may have been lost: skip to the head and re-validate the auth of the clients
		// (a failed read of the gap version changes nothing)
		gap, gapOk := s.realtimeGapVersion()
		if (gapOk && gap != gapSeen) || time.Since(lastProgress) > RealtimeBusRetention/2 {
			if gapOk {
				gapSeen = gap
			}
			s.discoverRealtimeBoots(cursors, true)
			lastProgress = time.Now()
			behindSince = time.Time{}
			s.callRealtimeHook(StoreKeyRealtimeResync)
			continue
		}

		// An event is dispatched with the rules of the cached collections, so it waits
		// (the cursors don't move, it is read again on the next poll) until the cache
		// has applied the collections version of the database. If that takes far
		// longer than a publication can be deferred, the events are dropped instead
		// of being delivered under stale rules, and the clients' auth is re-validated.
		if s.collectionsBehind() {
			if behindSince.IsZero() {
				behindSince = time.Now()
			}
			if time.Since(behindSince) > 2*CacheSyncMaxDeferral {
				s.discoverRealtimeBoots(cursors, true)
				behindSince = time.Time{}
				s.callRealtimeHook(StoreKeyRealtimeResync)
			}
			lastProgress = time.Now() // waiting is not lagging
			continue
		}
		behindSince = time.Time{}

		if err := s.consumeRealtime(cursors); err == nil {
			lastProgress = time.Now()
		} else {
			s.app.Logger().Debug("Failed to read the realtime events", "error", err)
		}
	}
}

// discoverRealtimeBoots adds the other live processes to the cursors and returns
// the live ones. With skipHistory the cursors of all of them move to their latest
// event (and those of processes that are gone are dropped); a process seen for the
// first time later starts from its first event.
func (s *cacheSync) discoverRealtimeBoots(cursors map[string]int64, skipHistory bool) map[string]struct{} {
	boots := []string{}
	err := s.app.ConcurrentDB().
		NewQuery("SELECT [[boot]] FROM {{_realtimeBoots}} WHERE [[boot]] != {:boot}").
		Bind(dbx.Params{"boot": s.boot()}).
		Column(&boots)
	if err != nil {
		return nil
	}

	live := make(map[string]struct{}, len(boots))
	for _, boot := range boots {
		live[boot] = struct{}{}

		if _, known := cursors[boot]; known && !skipHistory {
			continue
		}

		if !skipHistory {
			cursors[boot] = 0
			continue
		}

		var head sql.NullInt64
		err := s.app.ConcurrentDB().
			NewQuery("SELECT MAX([[seq]]) FROM {{_realtimeEvents}} WHERE [[boot]] = {:boot}").
			Bind(dbx.Params{"boot": boot}).
			Row(&head)
		if err != nil {
			continue
		}

		cursors[boot] = head.Int64
	}

	if skipHistory {
		for boot := range cursors {
			if _, ok := live[boot]; !ok {
				delete(cursors, boot)
			}
		}
	}

	return live
}

// realtimeGapVersion returns the shared gap version (0 when it was never bumped).
// ok is false when it could not be read.
func (s *cacheSync) realtimeGapVersion() (v int64, ok bool) {
	err := s.app.ConcurrentDB().NewQuery("SELECT [[version]] FROM {{_cacheVersions}} WHERE [[name]] = {:name}").
		Bind(dbx.Params{"name": cacheVersionRealtimeGap}).Row(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, true
		}
		return 0, false
	}
	return v, true
}

// consumeRealtime dispatches the events after the cursor of each other process.
//
// A process that left _realtimeBoots (a clean stop) still has its unread events
// dispatched before its cursor is dropped.
func (s *cacheSync) consumeRealtime(cursors map[string]int64) error {
	// processes that started since the last poll
	live := s.discoverRealtimeBoots(cursors, false)
	if live == nil {
		return errors.New("failed to list the realtime processes")
	}

	dispatcher, _ := s.app.Store().Get(StoreKeyRealtimeDispatcher).(RealtimeDispatcherFunc)
	if dispatcher == nil {
		return nil // the handlers are not registered yet (the router is bound later): nothing is consumed
	}

	var firstErr error

	for boot, cursor := range cursors {
		drained := true

		for {
			rows := []struct {
				Seq          int64  `db:"seq"`
				CollectionId string `db:"collection_id"`
				RecordId     string `db:"record_id"`
				Action       string `db:"action"`
			}{}

			err := s.app.ConcurrentDB().
				NewQuery("SELECT [[seq]], [[collection_id]], [[record_id]], [[action]] FROM {{_realtimeEvents}} WHERE [[boot]] = {:boot} AND [[seq]] > {:cursor} ORDER BY [[seq]] LIMIT 500").
				Bind(dbx.Params{"boot": boot, "cursor": cursor}).
				All(&rows)
			if err != nil {
				drained = false
				if firstErr == nil {
					firstErr = err
				}
				break
			}

			if len(rows) > 0 {
				if hook := realtimeBusAfterRead.Load(); hook != nil {
					(*hook)()
				}

				// a collections change may have committed while the events were read:
				// the page is dispatched only if the cache is still current (the
				// cursor stays where it is otherwise, and the page is read again)
				if s.collectionsBehind() {
					return firstErr
				}
			}

			for _, r := range rows {
				cursor = r.Seq
				if dispatcher != nil {
					s.dispatchRealtime(dispatcher, r.CollectionId, r.RecordId, r.Action)
				}
			}
			cursors[boot] = cursor

			if len(rows) < 500 {
				break
			}
		}

		if _, ok := live[boot]; !ok && drained {
			delete(cursors, boot)
		}
	}

	return firstErr
}

func (s *cacheSync) dispatchRealtime(dispatcher RealtimeDispatcherFunc, collectionId, recordId, action string) {
	defer func() {
		if r := recover(); r != nil {
			s.app.Logger().Error("Realtime dispatcher panic", "panic", r)
		}
	}()

	dispatcher(s.app, collectionId, recordId, action)
}

func (s *cacheSync) callRealtimeHook(storeKey string) {
	fn, ok := s.app.Store().Get(storeKey).(func(App))
	if !ok || fn == nil {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			s.app.Logger().Error("Realtime hook panic", "hook", storeKey, "panic", r)
		}
	}()

	fn(s.app)
}

// pruneRealtime deletes the events older than the retention (in small batches) and
// the processes that stopped beating.
func (s *cacheSync) pruneRealtime() {
	seconds := strconv.Itoa(int(RealtimeBusRetention.Seconds()))

	var eventsQuery, bootsQuery string
	if s.app.SQLDialect() == DialectMySQL {
		eventsQuery = "DELETE FROM {{_realtimeEvents}} WHERE [[created_at]] < (NOW(6) - INTERVAL " + seconds + " SECOND) LIMIT 1000"
		bootsQuery = "DELETE FROM {{_realtimeBoots}} WHERE [[beat]] < (NOW(6) - INTERVAL " + seconds + " SECOND)"
	} else {
		eventsQuery = "DELETE FROM {{_realtimeEvents}} WHERE ([[boot]], [[seq]]) IN (SELECT [[boot]], [[seq]] FROM {{_realtimeEvents}} WHERE [[created_at]] < (NOW() - INTERVAL '" + seconds + " seconds') LIMIT 1000)"
		bootsQuery = "DELETE FROM {{_realtimeBoots}} WHERE [[beat]] < (NOW() - INTERVAL '" + seconds + " seconds')"
	}

	for i := 0; i < 20; i++ {
		res, err := s.app.NonconcurrentDB().NewQuery(eventsQuery).Execute()
		if err != nil {
			s.app.Logger().Debug("Failed to prune the realtime events", "error", err)
			break
		}
		if n, _ := res.RowsAffected(); n < 1000 {
			break
		}
	}

	if _, err := s.app.NonconcurrentDB().NewQuery(bootsQuery).Execute(); err != nil {
		s.app.Logger().Debug("Failed to prune the realtime boots", "error", err)
	}
}

// runAuthSweep re-validates the authenticated realtime clients periodically,
// independently of the bus: it takes neither the events lock nor the write connection.
func (s *cacheSync) runAuthSweep(ctx context.Context) {
	interval := RealtimeAuthSweepInterval
	if v := os.Getenv("PB_REALTIME_AUTH_SWEEP"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			interval = time.Duration(secs) * time.Second
		}
	}
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.app.IsBootstrapped() {
				s.callRealtimeHook(StoreKeyRealtimeAuthSweep)
			}
		}
	}
}

func newRealtimeBoot() string {
	return security.RandomString(16)
}
