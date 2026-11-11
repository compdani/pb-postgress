package apis_test

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
	"github.com/pocketbase/pocketbase/tools/types"
)

// two instances (own data dir, one shared database); the first publishes
// its record events to the second when bus is true
func newBusApps(t *testing.T, busA, busB bool) (*core.BaseApp, *core.BaseApp) {
	t.Helper()
	return newBusAppsEvery(t, busA, busB, 40*time.Millisecond)
}

// like newBusApps, with the interval at which the bus is polled
func newBusAppsEvery(t *testing.T, busA, busB bool, busInterval time.Duration) (*core.BaseApp, *core.BaseApp) {
	t.Helper()

	oldSync, oldBus := core.CacheSyncInterval, core.RealtimeBusInterval
	core.CacheSyncInterval, core.RealtimeBusInterval = 40*time.Millisecond, busInterval
	t.Cleanup(func() { core.CacheSyncInterval, core.RealtimeBusInterval = oldSync, oldBus })

	cfgA := tests.NewDatabaseConfig(t)
	cfgA.RealtimeBus = busA
	a := core.NewBaseApp(cfgA)
	if err := a.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.ClearBootstrap() })

	cfgB := cfgA
	cfgB.DataDir = t.TempDir()
	cfgB.RealtimeBus = busB
	b := core.NewBaseApp(cfgB)
	if err := b.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.ClearBootstrap() })

	for _, app := range []*core.BaseApp{a, b} {
		if _, err := apis.NewRouter(app); err != nil {
			t.Fatal(err)
		}
	}

	return a, b
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", what)
}

func receive(client subscriptions.Client, timeout time.Duration) (subscriptions.Message, bool) {
	select {
	case m := <-client.Channel():
		return m, true
	case <-time.After(timeout):
		return subscriptions.Message{}, false
	}
}

func TestRealtimeBusDeliversRemoteRecordEvents(t *testing.T) {
	a, b := newBusApps(t, true, true)

	posts := core.NewBaseCollection("bus_posts")
	posts.ListRule = types.Pointer("")
	posts.Fields.Add(&core.TextField{Name: "title"})
	if err := a.Save(posts); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B sees the collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("bus_posts")
		return err == nil
	})

	client := subscriptions.NewDefaultClient()
	client.Subscribe("bus_posts/*")
	b.SubscriptionsBroker().Register(client)

	record := core.NewRecord(posts)
	record.Set("title", "from A")
	if err := a.Save(record); err != nil {
		t.Fatal(err)
	}

	msg, ok := receive(client, 5*time.Second)
	if !ok {
		t.Fatal("B's client did not receive the event of A's write")
	}

	var data struct {
		Action string         `json:"action"`
		Record map[string]any `json:"record"`
	}
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Action != "create" || data.Record["title"] != "from A" {
		t.Fatalf("unexpected event: %s", msg.Data)
	}

	// an update arrives too
	record.Set("title", "updated on A")
	if err := a.Save(record); err != nil {
		t.Fatal(err)
	}
	msg, ok = receive(client, 5*time.Second)
	if !ok {
		t.Fatal("B's client did not receive the update")
	}
	if err := json.Unmarshal(msg.Data, &data); err != nil || data.Action != "update" || data.Record["title"] != "updated on A" {
		t.Fatalf("unexpected update event: %s (%v)", msg.Data, err)
	}
}

func TestRealtimeBusDoesNotDuplicateLocalEvents(t *testing.T) {
	a, _ := newBusApps(t, true, true)

	posts := core.NewBaseCollection("bus_local")
	posts.ListRule = types.Pointer("")
	if err := a.Save(posts); err != nil {
		t.Fatal(err)
	}

	client := subscriptions.NewDefaultClient()
	client.Subscribe("bus_local/*")
	a.SubscriptionsBroker().Register(client)

	if err := a.Save(core.NewRecord(posts)); err != nil {
		t.Fatal(err)
	}

	if _, ok := receive(client, 3*time.Second); !ok {
		t.Fatal("the writer's own client did not receive the local event")
	}
	if _, ok := receive(client, 700*time.Millisecond); ok {
		t.Fatal("the writer received its own event a second time through the bus")
	}
}

func TestRealtimeBusRevokesRemoteClientAuth(t *testing.T) {
	a, b := newBusApps(t, true, true)

	users := core.NewAuthCollection("bus_users")
	if err := a.Save(users); err != nil {
		t.Fatal(err)
	}
	user := core.NewRecord(users)
	user.SetEmail("bus@example.com")
	user.SetPassword("1234567890")
	if err := a.Save(user); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B sees the user", func() bool {
		c, err := b.FindCachedCollectionByNameOrId("bus_users")
		if err != nil {
			return false
		}
		_, err = b.FindRecordById(c, user.Id)
		return err == nil
	})

	// B's client is authenticated, and nobody subscribes to the users collection
	collectionOnB, _ := b.FindCachedCollectionByNameOrId("bus_users")
	authOnB, err := b.FindRecordById(collectionOnB, user.Id)
	if err != nil {
		t.Fatal(err)
	}
	client := subscriptions.NewDefaultClient()
	client.Set(apis.RealtimeClientAuthKey, authOnB)
	b.SubscriptionsBroker().Register(client)

	// the password change on A rotates the token key
	user.SetPassword("0987654321")
	if err := a.Save(user); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "B unsets the revoked client", func() bool { return client.Get(apis.RealtimeClientAuthKey) == nil })
}

func TestRealtimeAuthSweepWorksWithoutTheBus(t *testing.T) {
	a, b := newBusApps(t, false, false)

	users := core.NewAuthCollection("sweep_users")
	if err := a.Save(users); err != nil {
		t.Fatal(err)
	}
	user := core.NewRecord(users)
	user.SetEmail("sweep@example.com")
	user.SetPassword("1234567890")
	if err := a.Save(user); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B sees the collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("sweep_users")
		return err == nil
	})

	collectionOnB, _ := b.FindCachedCollectionByNameOrId("sweep_users")
	authOnB, err := b.FindRecordById(collectionOnB, user.Id)
	if err != nil {
		t.Fatal(err)
	}
	client := subscriptions.NewDefaultClient()
	client.Set(apis.RealtimeClientAuthKey, authOnB)
	b.SubscriptionsBroker().Register(client)

	user.SetPassword("0987654321")
	if err := a.Save(user); err != nil {
		t.Fatal(err)
	}

	// nothing was published, the sweep is what catches it
	sweep, ok := b.Store().Get(core.StoreKeyRealtimeAuthSweep).(func(core.App))
	if !ok {
		t.Fatal("the auth sweep is not registered")
	}
	sweep(b)

	if client.Get(apis.RealtimeClientAuthKey) != nil {
		t.Fatal("the sweep did not unset the client of a record with a changed token key")
	}
}

func TestRealtimeBusSignalsAGapWhenEventsAreDropped(t *testing.T) {
	oldMax := core.RealtimeBusMaxEventsPerTx
	core.RealtimeBusMaxEventsPerTx = 2
	t.Cleanup(func() { core.RealtimeBusMaxEventsPerTx = oldMax })

	a, _ := newBusApps(t, true, true)

	posts := core.NewBaseCollection("gap_posts")
	if err := a.Save(posts); err != nil {
		t.Fatal(err)
	}

	gap := func() int64 {
		var v int64
		_ = a.DB().NewQuery("SELECT [[version]] FROM {{_cacheVersions}} WHERE [[name]] = 'realtimeGap'").Row(&v)
		return v
	}
	before := gap()

	err := a.RunInTransaction(func(tx core.App) error {
		for i := 0; i < 5; i++ { // above the cap
			if err := tx.Save(core.NewRecord(posts)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if gap() <= before {
		t.Fatal("dropping events above the cap must signal a gap")
	}
}

func TestRealtimeBusNeverDeliversUnderStaleRules(t *testing.T) {
	a, b := newBusApps(t, true, true)

	posts := core.NewBaseCollection("stale_posts")
	posts.ListRule = types.Pointer("") // anyone may list
	if err := a.Save(posts); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B sees the collection", func() bool {
		c, err := b.FindCachedCollectionByNameOrId("stale_posts")
		return err == nil && c.ListRule != nil
	})

	anonymous := subscriptions.NewDefaultClient()
	anonymous.Subscribe("stale_posts/*")
	b.SubscriptionsBroker().Register(anonymous)

	// what A changes to the collections is not published on B for now
	release := b.HoldCollectionCachePublication()
	defer release()

	posts.ListRule = nil // revoked: only superusers
	if err := a.Save(posts); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(core.NewRecord(posts)); err != nil {
		t.Fatal(err)
	}

	// B still caches the old rule, but must not use it for an event
	if _, ok := receive(anonymous, 1500*time.Millisecond); ok {
		t.Fatal("an event was delivered with the rules B had cached before the revocation")
	}

	// once B has the new rule the held event is evaluated with it: still nothing
	release()
	waitFor(t, "B publishes the revocation", func() bool {
		c, err := b.FindCachedCollectionByNameOrId("stale_posts")
		return err == nil && c.ListRule == nil
	})
	if _, ok := receive(anonymous, 1500*time.Millisecond); ok {
		t.Fatal("an event was delivered to an anonymous client after the list rule was revoked")
	}
}

func TestRealtimeBusDeliversTheLastEventsOfAProcessThatStopped(t *testing.T) {
	a, b := newBusAppsEvery(t, true, true, 600*time.Millisecond)

	posts := core.NewBaseCollection("stop_posts")
	posts.ListRule = types.Pointer("")
	if err := a.Save(posts); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B sees the collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("stop_posts")
		return err == nil
	})

	client := subscriptions.NewDefaultClient()
	client.Subscribe("stop_posts/*")
	b.SubscriptionsBroker().Register(client)

	if err := a.Save(core.NewRecord(posts)); err != nil {
		t.Fatal(err)
	}
	// A stops cleanly before B polled again: it leaves the list of live processes
	if err := a.ClearBootstrap(); err != nil {
		t.Fatal(err)
	}

	if _, ok := receive(client, 4*time.Second); !ok {
		t.Fatal("the event written just before the process stopped was lost")
	}
}

func TestRealtimeBusDeliversEventsOfACollectionCreatedMeanwhile(t *testing.T) {
	a, b := newBusApps(t, true, true)

	// subscribed by name before the collection exists anywhere
	client := subscriptions.NewDefaultClient()
	client.Subscribe("late_posts/*")
	b.SubscriptionsBroker().Register(client)

	posts := core.NewBaseCollection("late_posts")
	posts.ListRule = types.Pointer("")
	if err := a.Save(posts); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(core.NewRecord(posts)); err != nil {
		t.Fatal(err)
	}

	if _, ok := receive(client, 5*time.Second); !ok {
		t.Fatal("the event of a record in a collection that was not cached yet was dropped")
	}
}

func TestRealtimeBusRolledBackAndNestedTransactions(t *testing.T) {
	a, b := newBusApps(t, true, true)

	posts := core.NewBaseCollection("tx_posts")
	posts.ListRule = types.Pointer("")
	if err := a.Save(posts); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B sees the collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("tx_posts")
		return err == nil
	})

	client := subscriptions.NewDefaultClient()
	client.Subscribe("tx_posts/*")
	b.SubscriptionsBroker().Register(client)

	// rolled back (also through a nested transaction): nothing is published
	rollback := errors.New("rollback")
	err := a.RunInTransaction(func(tx core.App) error {
		if err := tx.RunInTransaction(func(inner core.App) error {
			return inner.Save(core.NewRecord(posts))
		}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if _, ok := receive(client, 800*time.Millisecond); ok {
		t.Fatal("a rolled back transaction published an event")
	}

	// committed through nested transactions: one event for the record
	err = a.RunInTransaction(func(tx core.App) error {
		return tx.RunInTransaction(func(inner core.App) error {
			r := core.NewRecord(posts)
			if err := inner.Save(r); err != nil {
				return err
			}
			r.Set("id", r.Id)
			return inner.Save(r) // a second write of the same record in the same transaction
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := receive(client, 3*time.Second); !ok {
		t.Fatal("the committed nested transaction published nothing")
	}
}

func TestRealtimeAuthSweepSuspendsThenDropsClientsItCannotRead(t *testing.T) {
	a, _ := newBusApps(t, false, false)

	users := core.NewAuthCollection("sweepfail_users")
	if err := a.Save(users); err != nil {
		t.Fatal(err)
	}
	user := core.NewRecord(users)
	user.SetEmail("sweepfail@example.com")
	user.SetPassword("1234567890")
	if err := a.Save(user); err != nil {
		t.Fatal(err)
	}

	client := subscriptions.NewDefaultClient()
	client.Set(apis.RealtimeClientAuthKey, user)
	a.SubscriptionsBroker().Register(client)

	sweep := a.Store().Get(core.StoreKeyRealtimeAuthSweep).(func(core.App))

	// the records can't be read any more
	if _, err := a.DB().NewQuery(`DROP TABLE "sweepfail_users"`).Execute(); err != nil {
		t.Fatal(err)
	}

	sweep(a)
	if client.Get(apis.RealtimeClientAuthKey) == nil {
		t.Fatal("one failed sweep must not drop the auth")
	}
	if unverified, _ := client.Get("authUnverified").(bool); !unverified {
		t.Fatal("a client whose auth could not be verified must be marked unverified (no delivery)")
	}

	sweep(a)
	sweep(a)
	if client.Get(apis.RealtimeClientAuthKey) != nil {
		t.Fatal("the auth must be dropped after three failed sweeps")
	}
}

func TestRealtimeBusRechecksTheCollectionsBeforeDispatching(t *testing.T) {
	a, b := newBusApps(t, true, true)

	posts := core.NewBaseCollection("recheck_posts")
	posts.ListRule = types.Pointer("")
	if err := a.Save(posts); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B sees the collection", func() bool {
		c, err := b.FindCachedCollectionByNameOrId("recheck_posts")
		return err == nil && c.ListRule != nil
	})

	anonymous := subscriptions.NewDefaultClient()
	anonymous.Subscribe("recheck_posts/*")
	b.SubscriptionsBroker().Register(anonymous)

	// B's cache keeps the public rule (its publication is held)
	release := b.HoldCollectionCachePublication()
	defer release()

	// the rule is revoked on A while B is between reading an event and dispatching it
	var armed atomic.Bool
	armed.Store(true)
	core.SetRealtimeBusAfterReadHook(func() {
		if armed.CompareAndSwap(true, false) {
			// runs on B's consumer goroutine: its own copy of the collection
			revoked, err := a.FindCollectionByNameOrId("recheck_posts")
			if err != nil {
				t.Error(err)
				return
			}
			revoked.ListRule = nil
			if err := a.Save(revoked); err != nil {
				t.Error(err)
			}
		}
	})
	t.Cleanup(func() { core.SetRealtimeBusAfterReadHook(nil) })

	if err := a.Save(core.NewRecord(posts)); err != nil {
		t.Fatal(err)
	}

	if _, ok := receive(anonymous, 1500*time.Millisecond); ok {
		t.Fatal("an event was dispatched under a rule that was revoked while it was being read")
	}

	release()
	waitFor(t, "B publishes the revocation", func() bool {
		c, err := b.FindCachedCollectionByNameOrId("recheck_posts")
		return err == nil && c.ListRule == nil
	})
	if _, ok := receive(anonymous, 1500*time.Millisecond); ok {
		t.Fatal("the event was delivered to an anonymous client after the rule was revoked")
	}
}

func decodeDeleteEvent(t *testing.T, msg subscriptions.Message) (action string, record map[string]any) {
	t.Helper()
	var data struct {
		Action string         `json:"action"`
		Record map[string]any `json:"record"`
	}
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		t.Fatal(err)
	}
	return data.Action, data.Record
}

func TestRealtimeBusDeliversRemoteDeletesOnlyWhenDecidableWithoutTheRecord(t *testing.T) {
	a, b := newBusApps(t, true, true)

	public := core.NewBaseCollection("del_public")
	public.ListRule, public.ViewRule = types.Pointer(""), types.Pointer("")
	public.Fields.Add(&core.TextField{Name: "title"})

	guarded := core.NewBaseCollection("del_guarded")
	guarded.ListRule = types.Pointer("@request.auth.id != ''")
	guarded.ViewRule = guarded.ListRule
	guarded.Fields.Add(&core.TextField{Name: "title"})

	users := core.NewAuthCollection("del_users")
	for _, c := range []*core.Collection{public, guarded, users} {
		if err := a.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "B sees the collections", func() bool {
		for _, name := range []string{"del_public", "del_guarded", "del_users"} {
			if _, err := b.FindCachedCollectionByNameOrId(name); err != nil {
				return false
			}
		}
		return true
	})

	regular := core.NewRecord(users)
	regular.SetEmail("del@example.com")
	regular.SetPassword("1234567890")
	if err := a.Save(regular); err != nil {
		t.Fatal(err)
	}
	superuser, err := b.FindAuthRecordByEmail(core.CollectionNameSuperusers, "")
	if err != nil {
		superuser = core.NewRecord(must(b.FindCachedCollectionByNameOrId(core.CollectionNameSuperusers)))
		superuser.SetEmail("root@example.com")
		superuser.SetPassword("1234567890")
		if err := b.Save(superuser); err != nil {
			t.Fatal(err)
		}
	}

	register := func(auth *core.Record, subs ...string) subscriptions.Client {
		c := subscriptions.NewDefaultClient()
		if auth != nil {
			c.Set(apis.RealtimeClientAuthKey, auth)
		}
		c.Subscribe(subs...)
		b.SubscriptionsBroker().Register(c)
		return c
	}

	anonymousPublic := register(nil, "del_public/*")
	filteredPublic := register(nil, `del_public/*?options={"query":{"filter":"title='x'"}}`)
	userGuarded := register(regular, "del_guarded/*")
	superGuarded := register(superuser, "del_guarded/*")
	superFiltered := register(superuser, `del_guarded/*?options={"query":{"filter":"title='x'"}}`)

	pub := core.NewRecord(public)
	pub.Set("title", "secret title")
	grd := core.NewRecord(guarded)
	grd.Set("title", "guarded title")
	for _, r := range []*core.Record{pub, grd} {
		if err := a.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	// the create events first (not what is checked here)
	for name, c := range map[string]subscriptions.Client{"anonymous": anonymousPublic, "user": userGuarded, "superuser": superGuarded} {
		msg, ok := receive(c, 3*time.Second)
		if !ok {
			t.Fatalf("%s: the create event never arrived", name)
		}
		if action, _ := decodeDeleteEvent(t, msg); action != "create" {
			t.Fatalf("%s: expected a create event, got %s", name, msg.Data)
		}
	}
	// (the filtered subscriptions don't match the created records: nothing to wait for)

	if err := a.Delete(pub); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(grd); err != nil {
		t.Fatal(err)
	}

	// public rule, no filter: delivered, and with the ids only
	msg, ok := receive(anonymousPublic, 3*time.Second)
	if !ok {
		t.Fatal("the delete of a public record was not delivered")
	}
	action, record := decodeDeleteEvent(t, msg)
	if action != "delete" || record["id"] != pub.Id || record["collectionName"] != "del_public" {
		t.Fatalf("unexpected delete event: %s", msg.Data)
	}
	if _, leaked := record["title"]; leaked || len(record) != 3 {
		t.Fatalf("a remote delete must carry only the ids, got %s", msg.Data)
	}

	// superuser, no filter: delivered
	if msg, ok = receive(superGuarded, 3*time.Second); !ok {
		t.Fatal("the delete was not delivered to a superuser subscriber")
	} else if action, record = decodeDeleteEvent(t, msg); action != "delete" || record["id"] != grd.Id {
		t.Fatalf("unexpected delete event: %s", msg.Data)
	}

	// everything that needs the record is not delivered
	if _, ok := receive(userGuarded, 700*time.Millisecond); ok {
		t.Fatal("a delete was delivered under a rule expression that can't be evaluated without the record")
	}
	if _, ok := receive(filteredPublic, 300*time.Millisecond); ok {
		t.Fatal("a delete was delivered to a subscription with a filter")
	}
	if _, ok := receive(superFiltered, 300*time.Millisecond); ok {
		t.Fatal("a delete was delivered to a filtered superuser subscription")
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestRealtimeBusRemoteDeleteIsNotDeliveredWhileEnrichHooksCanVetoEvents(t *testing.T) {
	a, b := newBusApps(t, true, true)

	posts := core.NewBaseCollection("hook_posts")
	posts.ListRule, posts.ViewRule = types.Pointer(""), types.Pointer("")
	if err := a.Save(posts); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B sees the collection", func() bool {
		_, err := b.FindCachedCollectionByNameOrId("hook_posts")
		return err == nil
	})

	// a hook that vetoes the delivery of events (on both instances, as an application would)
	deny := func(e *core.RecordEnrichEvent) error { return errors.New("not for this client") }
	a.OnRecordEnrich("hook_posts").BindFunc(deny)
	b.OnRecordEnrich("hook_posts").BindFunc(deny)

	client := subscriptions.NewDefaultClient()
	client.Subscribe("hook_posts/*")
	b.SubscriptionsBroker().Register(client)

	record := core.NewRecord(posts)
	if err := a.Save(record); err != nil {
		t.Fatal(err)
	}
	if _, ok := receive(client, 800*time.Millisecond); ok {
		t.Fatal("precondition: the hook is supposed to veto the create event")
	}

	if err := a.Delete(record); err != nil {
		t.Fatal(err)
	}
	if _, ok := receive(client, 1*time.Second); ok {
		t.Fatal("a remote delete bypassed an OnRecordEnrich hook that vetoes events")
	}

	// the application declares that its hooks need not see deletes
	apis.RealtimeRemoteDeletesIgnoreEnrichHooks = true
	t.Cleanup(func() { apis.RealtimeRemoteDeletesIgnoreEnrichHooks = false })

	again := core.NewRecord(posts)
	if err := a.Save(again); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(again); err != nil {
		t.Fatal(err)
	}
	msg, ok := receive(client, 3*time.Second)
	if !ok {
		t.Fatal("the opt-in did not enable the delivery of remote deletes")
	}
	if action, record := decodeDeleteEvent(t, msg); action != "delete" || record["id"] != again.Id {
		t.Fatalf("unexpected event: %s", msg.Data)
	}
}

func TestRealtimeBusRemoteDeleteFollowsTheRuleOfTheTopic(t *testing.T) {
	a, b := newBusApps(t, true, true)

	// anyone may list, only authenticated users may view; a third collection is locked
	mixed := core.NewBaseCollection("mixed_rules")
	mixed.ListRule, mixed.ViewRule = types.Pointer(""), types.Pointer("@request.auth.id != ''")
	locked := core.NewBaseCollection("locked_rules") // nil rules: superusers only
	for _, c := range []*core.Collection{mixed, locked} {
		if err := a.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "B sees the collections", func() bool {
		_, e1 := b.FindCachedCollectionByNameOrId("mixed_rules")
		_, e2 := b.FindCachedCollectionByNameOrId("locked_rules")
		return e1 == nil && e2 == nil
	})

	rm := core.NewRecord(mixed)
	rl := core.NewRecord(locked)
	for _, r := range []*core.Record{rm, rl} {
		if err := a.Save(r); err != nil {
			t.Fatal(err)
		}
	}

	subscribe := func(subs ...string) subscriptions.Client {
		c := subscriptions.NewDefaultClient()
		c.Subscribe(subs...)
		b.SubscriptionsBroker().Register(c)
		return c
	}
	byListName := subscribe("mixed_rules/*")
	byListId := subscribe(mixed.Id + "/*")
	byRecordTopic := subscribe("mixed_rules/" + rm.Id)
	both := subscribe("mixed_rules/*", mixed.Id+"/*") // two subscriptions that both match
	onLocked := subscribe("locked_rules/*")

	// the creates (list topics only: the record topic is not matched by a create of a record the client cannot view)
	for _, c := range []subscriptions.Client{byListName, byListId, both} {
		msg, ok := receive(c, 3*time.Second)
		if !ok {
			t.Fatal("the create event never arrived")
		}
		if action, _ := decodeDeleteEvent(t, msg); action != "create" {
			t.Fatalf("expected a create event, got %s", msg.Data)
		}
	}
	// the second subscription of the same client matches the create too
	if msg, ok := receive(both, 3*time.Second); !ok {
		t.Fatal("the second create event of the client with two subscriptions never arrived")
	} else if action, _ := decodeDeleteEvent(t, msg); action != "create" {
		t.Fatalf("expected a create event, got %s", msg.Data)
	}
	receive(onLocked, 300*time.Millisecond) // a locked collection delivers nothing to this client

	if err := a.Delete(rm); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(rl); err != nil {
		t.Fatal(err)
	}

	// list topic is public: by name and by id
	for name, c := range map[string]subscriptions.Client{"list by name": byListName, "list by id": byListId} {
		if msg, ok := receive(c, 3*time.Second); !ok {
			t.Fatalf("%s: a public list topic must receive the delete", name)
		} else if action, record := decodeDeleteEvent(t, msg); action != "delete" || record["id"] != rm.Id {
			t.Fatalf("%s: unexpected event %s", name, msg.Data)
		}
	}

	// one message per matching subscription, as for a local delete
	for i := 0; i < 2; i++ {
		msg, ok := receive(both, 3*time.Second)
		if !ok {
			t.Fatalf("the client with two matching subscriptions got only %d of 2 messages", i)
		}
		if action, record := decodeDeleteEvent(t, msg); action != "delete" || record["id"] != rm.Id {
			t.Fatalf("unexpected event for the client with two subscriptions: %s", msg.Data)
		}
	}
	if _, ok := receive(both, 300*time.Millisecond); ok {
		t.Fatal("more messages than matching subscriptions")
	}

	// the record topic is guarded by the view rule (an expression): nothing
	if _, ok := receive(byRecordTopic, 700*time.Millisecond); ok {
		t.Fatal("the record topic must follow the view rule, not the list rule")
	}

	// locked (nil) rules: not for an anonymous client
	if _, ok := receive(onLocked, 700*time.Millisecond); ok {
		t.Fatal("a locked collection's delete was delivered to an anonymous client")
	}
}
