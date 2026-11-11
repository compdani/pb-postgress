package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/hook"
)

const systemHookIdCacheSync = "__pbCacheSyncSystemHook__"

// StoreKeyAuthCollectionInvalidator can hold a
// [AuthCollectionInvalidatorFunc] that the cache sync calls when it notices
// that another instance changed the token secret of (or deleted) an auth
// collection, so the realtime clients authenticated with it can be unset.
const StoreKeyAuthCollectionInvalidator = "pbAuthCollectionInvalidator"

// AuthCollectionInvalidatorFunc is called with the id and the name
// (as known before the change) of an auth collection whose issued tokens
// are no longer valid.
type AuthCollectionInvalidatorFunc func(app App, collectionId, collectionName string)

var (
	// CacheSyncInterval is how often the shared cache versions are polled.
	CacheSyncInterval = 2 * time.Second

	// CacheSyncReconcileInterval is how often the caches are compared with the
	// database even when no version changed (repairs a lost version bump).
	CacheSyncReconcileInterval = 45 * time.Second

	// CacheSyncMaxDeferral is the longest the publication of a reloaded cache
	// is held back by local collection writes before it is forced.
	CacheSyncMaxDeferral = 30 * time.Second
)

// cacheSync keeps the cached collections and the settings of an instance in
// line with the changes that other instances committed to the shared database.
//
// The cache is published with a small protocol: the shared versions are read
// first, the data is loaded from the database and the result is only stored
// when no local collection write started or ran in the meantime
// (otherwise a stale load could revert the in-transaction cache of the local
// write). The "applied" version is the one read before the load.
type cacheSync struct {
	app *BaseApp

	// publish serializes only the check and the store of a loaded cache
	// (no database access is performed while it is held)
	publish sync.Mutex

	// local collection writes in flight and a counter that changes whenever
	// one of them ends
	inFlight   atomic.Int32
	generation atomic.Uint64

	// realtime bus (see realtime_bus.go)
	bootValue   atomic.Value // string: identifies this run in the events it publishes (new on every start)
	realtimeSeq atomic.Int64
	gapPending  atomic.Bool

	// serializes the settings reloads (see reloadSettings)
	settingsReload reentrantMutex

	// fingerprint of the stored settings rows the app settings were loaded from
	// (lets the sync skip reloading what a local reload already applied)
	settingsLoaded atomic.Value

	// content hash of the collections as of the last publication
	collectionsLoaded atomic.Value

	// the shared versions table exists (remembered)
	tableReady atomic.Bool

	// the collections version this instance has published (-1 until the first
	// publication)
	appliedCollections atomic.Int64

	// What the scope hides, by collection id, as of the published collections
	// (see BaseApp.visibleUnder). It is one immutable snapshot, replaced as a
	// whole: stateMu serializes the writers that merge into it.
	state   atomic.Pointer[scopeState]
	stateMu sync.Mutex

	// counts the installed snapshots: a snapshot computed from reads that began
	// at an older epoch may not relax what a newer one decided
	epoch atomic.Uint64

	versionRaces int // consecutive publications aborted by a concurrent change (sync goroutine)

	lifecycle sync.Mutex // serializes start and stop

	mu      sync.Mutex // protects run state below
	cancel  context.CancelFunc
	wake    chan struct{}
	done    chan struct{}
	pending map[string]bool // forced reloads (e.g. file notify, restore)
	wg      sync.WaitGroup  // the postgres listener

	// secrets of the auth collections as of the last applied reload, shared
	// with local collection writes (see noteLocalAuthCollection)
	baseMu       sync.Mutex
	authBaseline map[string]authBaselineEntry

	// the fields below are owned by the sync goroutine
	applied       map[string]int64
	deferredSince map[string]time.Time
	fingerprints  map[string]string
	retry         map[string]bool // retried on the next poll (never self-waking)
}

// scopeState is an immutable snapshot of the scope decisions for the
// non-system collections of the published collections: id -> hidden.
type scopeState struct {
	hidden map[string]bool

	// the scope the snapshot was resolved from
	scope InstanceScope

	// the snapshot was only tightened (not computed from a consistent pair of
	// collections and scope): the next consistent snapshot replaces it
	pending bool
}

// snapshotHidden resolves the scope against the collections.
func snapshotHidden(scope InstanceScope, collections []*Collection) map[string]bool {
	hidden := make(map[string]bool, len(collections))
	for _, c := range collections {
		if !c.System {
			hidden[c.Id] = !scopeVisible(scope, c)
		}
	}
	return hidden
}

// setState installs a snapshot. With union the ids that were hidden stay
// hidden (a tightening only: nothing turns visible until a consistent
// snapshot replaces it). hidden, scope and pending always change together.
func (s *cacheSync) setState(hidden map[string]bool, union bool, scope InstanceScope) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	s.install(hidden, union, scope)
}

// setStateSince installs a snapshot that was computed from reads that began
// when the epoch was since. If another snapshot was installed meanwhile (for
// instance the scope this instance just saved), this one cannot be trusted to
// relax anything and is merged as a tightening: the next consistent snapshot
// replaces it.
func (s *cacheSync) setStateSince(since uint64, hidden map[string]bool, union bool, scope InstanceScope) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	if s.epoch.Load() != since {
		union = true
		if cur := s.state.Load(); cur != nil {
			scope = cur.scope // the newer decision's scope: the next refresh compares with the database
		}
	}

	s.install(hidden, union, scope)
}

// install must be called with stateMu held.
func (s *cacheSync) install(hidden map[string]bool, union bool, scope InstanceScope) {
	if union {
		if old := s.state.Load(); old != nil {
			for id, h := range old.hidden {
				if h {
					hidden[id] = true
				}
			}
		}
	}

	s.epoch.Add(1)
	s.state.Store(&scopeState{hidden: hidden, scope: scope, pending: union})
}

// mergeHidden adds to dst the ids that the scope hides among collections.
func mergeHidden(dst map[string]bool, scope InstanceScope, collections []*Collection) {
	for id, h := range snapshotHidden(scope, collections) {
		if h {
			dst[id] = true
		}
	}
}

// localScopePlan is a scope this instance is about to save, resolved against
// the collections.
type localScopePlan struct {
	hidden map[string]bool
	behind bool
	scope  InstanceScope
}

// planLocal resolves a scope that this instance is about to write. It runs
// BEFORE the write and fails the write when the collections can't be read: a
// collection renamed elsewhere may not be published here yet, and a scope that
// already names the new name could then not be enforced on it. In that case
// the scope is also resolved against what the database has, and it may only
// tighten. It returns nil when the cache sync isn't running.
func (s *cacheSync) planLocal(scope InstanceScope) (*localScopePlan, error) {
	if !s.running() {
		return nil, nil
	}

	behind := s.collectionsBehind()

	hidden := snapshotHidden(scope, s.publishedCollections())
	if behind {
		collections, err := s.app.FindAllCollections()
		if err != nil {
			return nil, err
		}
		mergeHidden(hidden, scope, collections)
	}

	return &localScopePlan{hidden: hidden, behind: behind, scope: scope}, nil
}

// adoptLocal follows a scope written by this instance itself (after the write).
func (s *cacheSync) adoptLocal(plan *localScopePlan) {
	if plan != nil {
		s.setState(plan.hidden, plan.behind, plan.scope)
	}
}

// collectionsBehind reports whether the database collections version is ahead
// of what this instance published.
func (s *cacheSync) collectionsBehind() bool {
	var v int64
	err := s.app.ConcurrentDB().NewQuery("SELECT [[version]] FROM {{_cacheVersions}} WHERE [[name]] = {:name}").
		Bind(dbx.Params{"name": cacheVersionCollections}).Row(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return true
	}
	return v != s.appliedCollections.Load()
}

func (s *cacheSync) publishedCollections() []*Collection {
	collections, _ := s.app.Store().Get(StoreKeyCachedCollections).([]*Collection)
	return collections
}

type authBaselineEntry struct {
	name   string
	secret string
}

func newCacheSync(app *BaseApp) *cacheSync {
	s := &cacheSync{app: app, pending: map[string]bool{}}
	s.bootValue.Store(newRealtimeBoot())
	return s
}

// beginCollectionWrite marks a local collection write and returns its release func.
//
// Inside a transaction the hold lasts until the outermost transaction ends.
func (app *BaseApp) beginCollectionWrite() func() {
	cs := app.cacheSync
	if cs == nil {
		return func() {}
	}

	// the counters change under the publish mutex, the same one the watcher
	// holds between checking them and storing a loaded cache, so a writer
	// can never start (or end) between that check and that store
	if info := app.txInfo; info != nil {
		info.mu.Lock()
		first := !info.collectionWriteHeld
		info.collectionWriteHeld = true
		info.mu.Unlock()

		if first {
			cs.enterWrite()
		}
		return func() {}
	}

	cs.enterWrite()
	return cs.leaveWrite
}

func (s *cacheSync) enterWrite() {
	s.publish.Lock()
	s.inFlight.Add(1)
	s.publish.Unlock()
}

func (s *cacheSync) leaveWrite() {
	s.publish.Lock()
	s.generation.Add(1)
	s.inFlight.Add(-1)
	s.publish.Unlock()
}

func (a *TxAppInfo) releaseCollectionWrite(app *BaseApp) {
	a.mu.Lock()
	held := a.collectionWriteHeld
	a.collectionWriteHeld = false
	a.mu.Unlock()

	if held && app.cacheSync != nil {
		app.cacheSync.leaveWrite()
	}
}

func (app *BaseApp) registerCacheSyncHooks() {
	app.OnBootstrap().Bind(&hook.Handler[*BootstrapEvent]{
		Id: systemHookIdCacheSync,
		Func: func(e *BootstrapEvent) error {
			if err := e.Next(); err != nil {
				return err
			}

			if ba := asBaseApp(e.App); ba != nil {
				ba.cacheSync.start()
			}

			return nil
		},
		Priority: -997,
	})

	app.OnTerminate().Bind(&hook.Handler[*TerminateEvent]{
		Id: systemHookIdCacheSync,
		Func: func(e *TerminateEvent) error {
			if ba := asBaseApp(e.App); ba != nil {
				ba.cacheSync.stop()
			}
			return e.Next()
		},
		Priority: -997,
	})

	app.OnBootstrapClear().Bind(&hook.Handler[*BootstrapEvent]{
		Id: systemHookIdCacheSync,
		Func: func(e *BootstrapEvent) error {
			if ba := asBaseApp(e.App); ba != nil {
				ba.cacheSync.stop()
			}
			return e.Next()
		},
		Priority: -997,
	})
}

// requestCacheReload asks the cache sync to reload the named cache from the
// database, whether or not its shared version changed.
func (app *BaseApp) requestCacheReload(name string) {
	if app.cacheSync != nil {
		app.cacheSync.request(name)
	}
}

func (s *cacheSync) request(name string) {
	s.mu.Lock()
	s.pending[name] = true
	wake := s.wake
	s.mu.Unlock()

	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (s *cacheSync) start() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()

	s.stopLocked()

	if !s.app.IsBootstrapped() || !s.app.HasTable(cacheVersionsTable) {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	s.cancel = cancel
	s.wake = make(chan struct{}, 1)
	s.done = make(chan struct{})
	wake, done := s.wake, s.done
	s.mu.Unlock()

	// a new id for every start: the other instances may have dropped the cursor of
	// the previous one and would replay its retained events if it came back
	s.bootValue.Store(newRealtimeBoot())

	s.initState()

	// The collections were loaded during the bootstrap, before the versions
	// above were read: publish them again right now (the bootstrap is still in
	// progress, so this cannot interleave with a request), so a commit in
	// between is never missed and the scope snapshot exists before serving.
	s.retry[cacheVersionCollections] = true
	s.retry[cacheVersionSettings] = true // cheap: skipped when already loaded
	s.sync(false)
	for attempt := 0; attempt < 10 && s.state.Load() == nil; attempt++ {
		time.Sleep(50 * time.Millisecond)
		s.sync(false)
	}
	if s.state.Load() == nil {
		s.installBestEffortState()
	}

	go func() {
		defer close(done)
		s.run(ctx, wake)
	}()

	// the auth sweep always runs; the bus consumer only when the bus is enabled
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runAuthSweep(ctx)
	}()
	if s.app.realtimeBusEnabled() {
		if err := s.registerRealtimeBoot(); err != nil {
			s.app.Logger().Warn("Failed to register this process for the realtime bus", "error", err)
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.runRealtime(ctx)
		}()
	}
}

func (s *cacheSync) stop() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()

	s.stopLocked()
}

func (s *cacheSync) stopLocked() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done, s.wake = nil, nil, nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	s.wg.Wait()

	// the other processes stop polling this one's events
	if cancel != nil {
		s.unregisterRealtimeBoot()
	}
}

// initState captures what this instance currently has applied.
func (s *cacheSync) initState() {
	s.applied = map[string]int64{}
	s.deferredSince = map[string]time.Time{}
	s.fingerprints = map[string]string{}
	s.retry = map[string]bool{}

	if versions, err := s.readVersions(); err == nil {
		s.applied = versions
	}
	s.tableReady.Store(true)

	// The collections were loaded by the bootstrap, before this point, so what
	// is published is unknown until the first sync (run synchronously by start()):
	// nothing is recorded in the scope snapshot until then
	s.appliedCollections.Store(-1)
	s.stateMu.Lock()
	s.epoch.Add(1)
	s.state.Store(nil)
	s.stateMu.Unlock()
	s.collectionsLoaded.Store("") // nothing is published by this run yet
	s.versionRaces = 0

	s.resetBaseline(false)

	s.fingerprints[cacheVersionSettings], _ = s.settingsFingerprint()

	// note: settingsLoaded is deliberately not set here. It records what the
	// app settings were really loaded from (see reloadSettings), so a commit
	// made after the bootstrap loaded them is detected instead of assumed applied
}

// resetBaseline sets the auth baseline from the collections this instance has cached.
func (s *cacheSync) resetBaseline(recordHash bool) {
	collections, err := s.app.FindAllCollections()
	if err != nil {
		// keep the previous baseline: an empty one would leave the next swap
		// with nothing to invalidate against
		s.app.Logger().Warn("Failed to read the collections for the auth baseline", "error", err)
		return
	}

	// only for collections that were replaced out of band and are the published
	// ones by definition; never at startup, where nothing is published yet
	if recordHash {
		s.collectionsLoaded.Store(collectionsHash(collections))
	}

	s.baseMu.Lock()
	s.authBaseline = authBaselineOf(collections)
	s.baseMu.Unlock()
}

// RebaselineCacheSync tells the cache sync that the current collections are
// the ones this instance is in line with. Call it after replacing the
// collections out of band (e.g. test fixtures), when no auth token
// invalidation should follow.
func (app *BaseApp) RebaselineCacheSync() {
	if app.cacheSync != nil && app.cacheSync.running() {
		app.cacheSync.resetBaseline(true)
	}
}

type baselineChange struct {
	id, name, secret string
	deleted          bool
}

// noteLocalAuthCollection records the token secret of an auth collection that this
// instance just saved (or deleted), so the sync doesn't report back a change
// the local hooks already handled (which would also unset the clients that
// authenticated again since).
//
// Inside a transaction the change is only applied once the outermost
// transaction committed (a rollback discards it).
func (app *BaseApp) noteLocalAuthCollection(c *Collection, deleted bool) {
	if app.cacheSync == nil || c == nil || !c.IsAuth() {
		return
	}

	change := baselineChange{id: c.Id, name: c.Name, secret: c.AuthToken.Secret, deleted: deleted}

	if info := app.txInfo; info != nil {
		info.mu.Lock()
		info.baselineChanges = append(info.baselineChanges, change)
		info.mu.Unlock()
		return
	}

	app.cacheSync.applyBaselineChanges([]baselineChange{change})
}

func (a *TxAppInfo) takeBaselineChanges() []baselineChange {
	a.mu.Lock()
	defer a.mu.Unlock()

	changes := a.baselineChanges
	a.baselineChanges = nil
	return changes
}

func (s *cacheSync) applyBaselineChanges(changes []baselineChange) {
	if len(changes) == 0 {
		return
	}

	s.baseMu.Lock()
	defer s.baseMu.Unlock()

	if s.authBaseline == nil {
		return
	}

	for _, c := range changes {
		if c.deleted {
			delete(s.authBaseline, c.id)
		} else {
			s.authBaseline[c.id] = authBaselineEntry{name: c.name, secret: c.secret}
		}
	}
}

// bestEffortHidden resolves the cached collections for the startup fallback.
//
// It fails closed: when the scope or the database collections could not be
// read, nothing can tell which cached collection a scope names under a
// different name (a rename published elsewhere), so every cached non-system
// collection counts as hidden until a reliable (collections, scope) pair
// replaces it. scope is nil when it could not be read, db is nil when the
// database collections could not be read.
func bestEffortHidden(scope *InstanceScope, published, db []*Collection) map[string]bool {
	hidden := make(map[string]bool, len(published))

	if scope == nil || db == nil {
		for _, c := range published {
			if !c.System {
				hidden[c.Id] = true
			}
		}
		return hidden
	}

	hidden = snapshotHidden(*scope, published)
	mergeHidden(hidden, *scope, db)

	return hidden
}

// installBestEffortState records the scope decisions of the collections this
// instance has cached when no consistent snapshot could be published at
// startup. It is keyed by id, only tightens, and is replaced by the first
// consistent snapshot (the sync loop keeps retrying). When the scope or the
// collections can't be read, it fails closed (see bestEffortHidden).
func (s *cacheSync) installBestEffortState() {
	var scope *InstanceScope
	if loaded, err := s.app.loadInstanceScope(); err == nil {
		scope = &loaded
	}

	var db []*Collection
	for attempt := 0; attempt < 3; attempt++ {
		if collections, err := s.app.FindAllCollections(); err == nil {
			db = collections
			if db == nil {
				db = []*Collection{}
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	hidden := bestEffortHidden(scope, s.publishedCollections(), db)

	installed := InstanceScope{}
	if scope != nil {
		installed = *scope
	}

	s.setState(hidden, true, installed)
	s.app.Logger().Warn("Could not publish a consistent collections snapshot at startup, using a conservative fallback",
		"scopeRead", scope != nil, "collectionsRead", db != nil)
}

func (s *cacheSync) running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancel != nil
}

func (s *cacheSync) run(ctx context.Context, wake chan struct{}) {
	poll := time.NewTicker(CacheSyncInterval)
	defer poll.Stop()

	reconcile := time.NewTicker(CacheSyncReconcileInterval)
	defer reconcile.Stop()

	s.listen(ctx, wake)

	for {
		forceCheck := false

		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		case <-wake:
		case <-reconcile.C:
			forceCheck = true
		}

		if ctx.Err() != nil {
			return
		}

		s.sync(forceCheck)
	}
}

func (s *cacheSync) readVersions() (map[string]int64, error) {
	rows := []struct {
		Name    string `db:"name"`
		Version int64  `db:"version"`
	}{}

	err := s.app.ConcurrentDB().
		NewQuery("SELECT [[name]], [[version]] FROM {{_cacheVersions}}").
		All(&rows)
	if err != nil {
		return nil, err
	}

	versions := make(map[string]int64, len(rows))
	for _, r := range rows {
		versions[r.Name] = r.Version
	}

	return versions, nil
}

func (s *cacheSync) takePending() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.pending
	s.pending = map[string]bool{}
	return p
}

// sync applies the changes made by the other instances.
//
// With check it also compares the caches with the database even when
// the versions did not change (the reconcile that repairs a lost bump).
//
// Work that could not be completed (a local write in flight, a failed read)
// is retried on the next poll: it never wakes the loop itself, so it cannot spin.
func (s *cacheSync) sync(check bool) {
	if !s.app.IsBootstrapped() {
		return
	}

	pending := s.takePending()

	versions, err := s.readVersions()
	if err != nil {
		s.app.Logger().Debug("Failed to read the shared cache versions", "error", err)
		for name := range pending {
			s.retry[name] = true
		}
		return
	}

	for _, name := range []string{cacheVersionCollections, cacheVersionSettings} {
		forced := pending[name] || s.retry[name] || versions[name] != s.applied[name]

		if !forced && !check {
			continue
		}

		var done bool
		if name == cacheVersionCollections {
			done = s.syncCollections(versions[name], forced)
		} else {
			done = s.syncSettings(versions[name])
		}

		if done {
			delete(s.retry, name)
		} else {
			s.retry[name] = true
		}
	}

	s.refreshConsistentScope(versions[cacheVersionCollections])
}

// refreshConsistentScope follows direct edits of the scope (they don't bump
// the collections version), and replaces a state that was only tightened as
// soon as the collections are consistent again.
//
// A scope change is always applied as a tightening at least, whatever happens
// to the collections version meanwhile: only the exact replace (which can
// relax) needs a stable version and the published collections.
func (s *cacheSync) refreshConsistentScope(version int64) {
	since := s.epoch.Load() // before the scope is read

	scope, err := s.app.loadInstanceScope()
	if err != nil {
		return
	}

	cur := s.state.Load()
	if cur != nil && !cur.pending && reflect.DeepEqual(cur.scope, scope) {
		return
	}

	stable := true
	if after, err := s.readVersions(); err != nil || after[cacheVersionCollections] != version {
		stable = false
	}

	collections, err := s.app.FindAllCollections()
	if err != nil {
		return
	}

	published := s.publishedCollections()

	if last, _ := s.collectionsLoaded.Load().(string); stable && collectionsHash(collections) == last {
		// the database collections are the published ones: an exact pair
		s.setStateSince(since, snapshotHidden(scope, published), false, scope)
		return
	}

	// publication pending: resolved against both, tightening only
	hidden := snapshotHidden(scope, published)
	mergeHidden(hidden, scope, collections)
	s.setStateSince(since, hidden, true, scope)
	s.retry[cacheVersionCollections] = true
}

// collectionsHash returns a hash of the full content of the collections
// (rules, fields, auth token secrets, ...).
func collectionsHash(collections []*Collection) string {
	sorted := slices.Clone(collections)
	slices.SortFunc(sorted, func(a, b *Collection) int { return strings.Compare(a.Id, b.Id) })

	h := sha256.New()
	for _, c := range sorted {
		// with the secrets: a rotated token secret must change the hash
		raw, err := c.marshalJSONWithSecrets()
		if err != nil {
			raw = []byte(c.Id + c.Updated.String())
		}
		h.Write(raw)
		h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil))
}

// syncCollections reloads the collections and publishes them.
//
// Without force (the periodic reconcile) the loaded collections are only
// published when their content differs from the last published ones.
func (s *cacheSync) syncCollections(version int64, force bool) bool {
	generation := s.generation.Load()
	since := s.epoch.Load() // before anything it installs is read

	collections, err := s.app.FindAllCollections()
	if err != nil {
		s.app.Logger().Warn("Failed to reload the cached collections", "error", err)
		return false
	}

	// derived from the loaded rows themselves, so it always describes what is published
	hash := collectionsHash(collections)

	if last, _ := s.collectionsLoaded.Load().(string); !force && hash == last {
		s.appliedCollections.Store(version)
		return true
	}

	// The scope that goes with these collections. Renames and deletes rewrite
	// the scopes in the same transaction that bumps the version, so when the
	// version is the same before and after both loads they describe one state.
	scope, err := s.app.loadInstanceScope()
	if err != nil {
		// without the scope that belongs to these collections nothing is
		// published: the guard would lose what it still has to hide
		s.app.Logger().Warn("Failed to load the instance scope for the cached collections", "error", err)
		return false
	}
	raced := false
	var racedCollections []*Collection
	if after, err := s.readVersions(); err != nil || after[cacheVersionCollections] != version {
		// changed meanwhile: retried on the next poll, but under a constant
		// stream of changes it is eventually published anyway, as a tightening
		// only (the next consistent snapshot replaces it)
		s.versionRaces++
		if s.versionRaces < 5 {
			return false
		}
		raced = true

		// the pair may straddle a change: resolve the scope against what the
		// database has now as well, so neither pairing can leave an id visible
		var ferr error
		if racedCollections, ferr = s.app.FindAllCollections(); ferr != nil {
			return false
		}
	}
	s.versionRaces = 0

	s.publish.Lock()
	busy := s.inFlight.Load() > 0 || s.generation.Load() != generation
	if busy {
		since, ok := s.deferredSince[cacheVersionCollections]
		if !ok {
			since = time.Now()
			s.deferredSince[cacheVersionCollections] = since
		}
		// a leaked hold must not defer the publication forever
		if time.Since(since) < CacheSyncMaxDeferral {
			s.publish.Unlock()
			return false
		}
	}
	delete(s.deferredSince, cacheVersionCollections)

	// the scope is read from the database (already committed together with
	// the collections), so it is reset on both sides of the swap
	// Order matters for readers that don't take the mutex: the collections
	// first, then the scope snapshot that matches them (a raced pair only
	// tightens it), then "applied", and only then the scope cache is dropped.
	s.app.resetScopeCache()
	s.app.Store().Set(StoreKeyCachedCollections, collections)
	hidden := snapshotHidden(scope, collections)
	if raced {
		mergeHidden(hidden, scope, racedCollections)
	}
	s.setStateSince(since, hidden, raced, scope)
	s.appliedCollections.Store(version)
	s.app.resetScopeCache()

	// the baseline moves together with the published collections, so a local
	// commit can never be compared against an older snapshot than the one it replaced
	previous, next := s.swapBaseline(collections)
	s.publish.Unlock()

	s.collectionsLoaded.Store(hash)
	s.applied[cacheVersionCollections] = version

	s.invalidateAuthCollections(previous, next)

	return true
}

func (s *cacheSync) syncSettings(version int64) bool {
	// taken before loading, so a concurrent change is never marked as loaded
	fingerprint, err := s.settingsFingerprint()

	// already loaded by a reload of this instance (e.g. it saved them itself,
	// or the bootstrap loaded exactly these rows)
	if loaded, _ := s.settingsLoaded.Load().(string); err == nil && loaded == fingerprint {
		s.applied[cacheVersionSettings] = version
		s.fingerprints[cacheVersionSettings] = fingerprint
		return true
	}

	if err := s.app.reloadSettings(false, true); err != nil {
		s.app.Logger().Warn("Failed to reload the settings", "error", err)
		return false
	}

	s.applied[cacheVersionSettings] = version
	s.fingerprints[cacheVersionSettings] = fingerprint

	return true
}

func authBaselineOf(collections []*Collection) map[string]authBaselineEntry {
	m := map[string]authBaselineEntry{}
	for _, c := range collections {
		if c.IsAuth() {
			m[c.Id] = authBaselineEntry{name: c.Name, secret: c.AuthToken.Secret}
		}
	}
	return m
}

// swapBaseline replaces the auth baseline with the published collections
// and returns the previous one and the new one.
func (s *cacheSync) swapBaseline(collections []*Collection) (previous, next map[string]authBaselineEntry) {
	next = authBaselineOf(collections)

	s.baseMu.Lock()
	previous = s.authBaseline
	s.authBaseline = maps.Clone(next) // next is read after the swap, without the lock
	s.baseMu.Unlock()

	return previous, next
}

// invalidateAuthCollections compares the auth collections with the baseline
// of the last applied reload (not with the mutable app cache, which local
// reloads also change) and invalidates the ones whose tokens stopped being valid.
func (s *cacheSync) invalidateAuthCollections(previous, next map[string]authBaselineEntry) {
	var invalidate func(App, string, string)
	if fn, ok := s.app.Store().Get(StoreKeyAuthCollectionInvalidator).(AuthCollectionInvalidatorFunc); ok && fn != nil {
		invalidate = fn
	}

	for id, old := range previous {
		cur, exists := next[id]
		if exists && cur.secret == old.secret {
			continue
		}
		if invalidate != nil {
			invalidate(s.app, id, old.name)
		}
	}
}

// settingsFingerprint is a hash of the content of the stored settings rows
// this instance loads: the shared one and its own (if any).
func (s *cacheSync) settingsFingerprint() (string, error) {
	ownKey := ""
	if id, err := s.app.InstanceID(); err == nil {
		ownKey = paramsKeyInstanceSettingsPrefix + id
	}

	rows := []struct {
		Id    string `db:"id"`
		Value string `db:"value"`
	}{}
	err := s.app.ConcurrentDB().
		NewQuery("SELECT [[id]], [[value]] FROM {{_params}} WHERE [[id]] = {:shared} OR [[id]] = {:own} ORDER BY [[id]]").
		Bind(dbx.Params{"shared": paramsKeySettings, "own": ownKey}).
		All(&rows)
	if err != nil {
		return "", err
	}

	h := sha256.New()
	for _, r := range rows {
		h.Write([]byte(r.Id))
		h.Write([]byte{0})
		h.Write([]byte(r.Value))
		h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// listen wakes the sync loop on postgres NOTIFY. Polling alone is always
// enough (it is the only mechanism for MySQL and for poolers that cannot LISTEN).
func (s *cacheSync) listen(ctx context.Context, wake chan struct{}) {
	cfg := s.app.config
	if s.app.SQLDialect() != DialectPostgres || cfg.DBConnect != nil || cfg.DBDSN == "" {
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		backoff := time.Second
		for ctx.Err() == nil {
			err := s.listenOnce(ctx, cfg.DBDSN, wake)
			if ctx.Err() != nil {
				return
			}
			s.app.Logger().Debug("Cache sync postgres listen ended", "error", err)

			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
		}
	}()
}

func (s *cacheSync) listenOnce(ctx context.Context, dsn string, wake chan struct{}) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())

	if _, err := conn.Exec(ctx, "LISTEN "+cacheNotifyChannel); err != nil {
		return err
	}

	for {
		waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := conn.WaitForNotification(waitCtx)
		cancel()

		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			return err
		}

		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

// HoldCollectionCachePublication defers the publication of the collection
// changes that other instances committed, until the returned release func is
// called (the cache sync never publishes while a local collection write is in
// flight, and publishes at the latest after CacheSyncMaxDeferral). It is meant
// for tests and for callers that must apply several local changes against one
// stable collections cache.
func (app *BaseApp) HoldCollectionCachePublication() (release func()) {
	return app.beginCollectionWrite()
}

// boot returns the id of this run, used in the realtime events it publishes.
func (s *cacheSync) boot() string {
	id, _ := s.bootValue.Load().(string)
	return id
}
