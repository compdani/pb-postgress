package apis

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/routine"
	"github.com/pocketbase/pocketbase/tools/search"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
)

// The realtime bus (see core/realtime_bus.go) hands the record events of the
// other instances to realtimeDispatchRemote, and calls realtimeSweepAuth
// periodically (and when events may have been lost) to re-validate the auth
// of the realtime clients of this instance.

const (
	// RealtimeClientUnverifiedKey marks a client whose auth could not be
	// re-validated by the last sweep: events are not delivered to it.
	realtimeClientUnverifiedKey = "authUnverified"

	// the number of consecutive failed sweeps after which the auth of a client is dropped
	realtimeSweepMaxFailures = 3

	realtimeClientSweepFailuresKey = "authSweepFailures"

	realtimeSweepChunkSize = 500
)

// RealtimeRemoteDeletesIgnoreEnrichHooks makes the realtime bus deliver the deletes
// made on other instances (id-only, see realtimeBroadcastRemoteDelete) even though
// OnRecordEnrich hooks are registered. By default such deletes are not delivered
// then, because the hooks may veto an event and can't run for a deleted record.
// Set it only if none of your hooks needs to veto deletes (e.g. they only add fields).
//
// It is a plain variable meant to be set once at startup, before the app serves: changing
// it while events are being dispatched is a data race.
var RealtimeRemoteDeletesIgnoreEnrichHooks bool

// realtimeAuthMu serializes the changes of the auth of the realtime clients
// that are decided from something read earlier (a database query, an event):
// see realtimeAuthSwap.
var realtimeAuthMu sync.Mutex

// realtimeAuthSwap runs fn only if the client still has the auth record that the
// decision was based on (a newer authentication, or a revocation that happened
// while the decision was being made, wins), atomically with respect to the other
// auth changes of the realtime clients.
func realtimeAuthSwap(client subscriptions.Client, expected *core.Record, fn func()) bool {
	realtimeAuthMu.Lock()
	defer realtimeAuthMu.Unlock()

	if current, _ := client.Get(RealtimeClientAuthKey).(*core.Record); current != expected {
		return false
	}

	fn()

	return true
}

// bindRealtimeBus registers the handlers of the realtime bus.
func bindRealtimeBus(app core.App) {
	app.Store().Set(core.StoreKeyRealtimeDispatcher, core.RealtimeDispatcherFunc(realtimeDispatchRemote))
	app.Store().Set(core.StoreKeyRealtimeResync, func(a core.App) { realtimeSweepAuth(a) })
	app.Store().Set(core.StoreKeyRealtimeAuthSweep, func(a core.App) { realtimeSweepAuth(a) })
}

// realtimeDispatchRemote applies a record event that another instance published.
func realtimeDispatchRemote(app core.App, collectionId, recordId, action string) {
	// the cache holds all collections: the auth handling below must not depend
	// on whether the collection is visible to this instance
	collection, err := app.FindCachedCollectionByNameOrId(collectionId)
	if err != nil {
		return
	}

	// auth records first: realtime clients may be authenticated with them, whether or not
	// anybody subscribes to their collection
	if collection.IsAuth() {
		realtimeApplyRemoteAuthEvent(app, collection, recordId, action)
	}

	// no database read unless a client can be interested
	if ba := core.AsBaseApp(app); ba != nil && !ba.CollectionVisible(collection) {
		return
	}
	if !realtimeHasSubscribers(app, collection) {
		return
	}

	// the record is gone: only the deletes that can be decided without it are delivered
	if action == "delete" {
		realtimeBroadcastRemoteDelete(app, collection, recordId)
		return
	}

	record, err := app.FindRecordById(collection, recordId)
	if err != nil {
		return // gone meanwhile (a later event covers it)
	}

	if err := realtimeBroadcastRecord(app, action, record, false); err != nil {
		app.Logger().Debug(
			"Failed to broadcast a remote record event",
			slog.String("id", recordId),
			slog.String("collectionName", collection.Name),
			slog.String("error", err.Error()),
		)
	}
}

// realtimeBroadcastRemoteDelete delivers a record delete that another instance
// published, to the subscribers that are allowed to see it for sure.
//
// The record doesn't exist any more (and the rows it referenced may be gone too), so
// the API rules and the subscription filters can't be evaluated the way they are for
// a delete handled by this instance. Only what doesn't need the record is decided:
//
//   - the rule of the topic is empty (public) and the subscription has no filter, or
//   - the client is a superuser and the subscription has no filter.
//
// Everything else is not delivered (fail closed). The event carries only the ids of
// the record, never its data. Such a client learns about the delete when it
// refetches the record or refreshes its list (resubscribing doesn't replay events).
func realtimeBroadcastRemoteDelete(app core.App, collection *core.Collection, recordId string) {
	// An OnRecordEnrich hook can veto the delivery of an event to a client (the local
	// path honors it by skipping the client). The deleted record doesn't exist any more
	// to run the hooks with, so while any such hook is registered a remote delete is
	// not delivered, unless the application declared that its hooks don't need to
	// see deletes (RealtimeRemoteDeletesIgnoreEnrichHooks).
	if !RealtimeRemoteDeletesIgnoreEnrichHooks && app.OnRecordEnrich().Length() > 0 {
		return
	}

	rules := map[string]*string{
		(collection.Name + "/" + recordId + "?"): collection.ViewRule,
		(collection.Id + "/" + recordId + "?"):   collection.ViewRule,
		(collection.Name + "/*?"):                collection.ListRule,
		(collection.Id + "/*?"):                  collection.ListRule,

		// @deprecated: the same as the wildcard topic
		(collection.Name + "?"): collection.ListRule,
		(collection.Id + "?"):   collection.ListRule,
	}

	data, err := json.Marshal(&recordData{
		Action: "delete",
		Record: map[string]any{
			"id":             recordId,
			"collectionId":   collection.Id,
			"collectionName": collection.Name,
		},
	})
	if err != nil {
		return
	}

	for _, chunk := range app.SubscriptionsBroker().ChunkedClients(clientsChunkSize) {
		for _, client := range chunk {
			if unverified, _ := client.Get(realtimeClientUnverifiedKey).(bool); unverified {
				continue
			}

			auth, _ := client.Get(RealtimeClientAuthKey).(*core.Record)
			isSuperuser := (&core.RequestInfo{Auth: auth}).HasSuperuserAuth()

			for prefix, rule := range rules {
				for sub, options := range client.Subscriptions(prefix) {
					if options.Query[search.FilterQueryParam] != "" {
						continue // needs the record
					}

					if !isSuperuser && (rule == nil || *rule != "") {
						continue // an expression (or a locked rule) needs the record
					}

					msg := subscriptions.Message{Name: sub, Data: data}
					routine.FireAndForget(func() { client.Send(msg) })
				}
			}
		}
	}
}

func realtimeHasSubscribers(app core.App, collection *core.Collection) bool {
	for _, chunk := range app.SubscriptionsBroker().ChunkedClients(clientsChunkSize) {
		for _, client := range chunk {
			// delimited prefixes: "posts" must not match the subscriptions of "posts2"
			if len(client.Subscriptions(
				collection.Name+"/", collection.Name+"?",
				collection.Id+"/", collection.Id+"?",
			)) > 0 {
				return true
			}
		}
	}

	return false
}

// realtimeApplyRemoteAuthEvent updates or unsets the clients authenticated with a
// record that another instance changed or deleted (matched by collection id).
func realtimeApplyRemoteAuthEvent(app core.App, collection *core.Collection, recordId, action string) {
	// the clients and the auth they have are captured BEFORE the record is read: what is
	// read is only applied to a client that still has that very auth afterwards
	targets := realtimeAuthTargets(app, collection.Id, recordId)
	if len(targets) == 0 {
		return
	}

	if action == "delete" {
		realtimeApplyAuthTargets(targets, nil)
		return
	}

	record, err := app.FindRecordById(collection, recordId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			realtimeApplyAuthTargets(targets, nil) // deleted meanwhile
		}
		return
	}

	realtimeApplyAuthTargets(targets, record)
}

type realtimeAuthTarget struct {
	client subscriptions.Client
	auth   *core.Record
}

// realtimeAuthTargets returns the clients authenticated with the record, with their auth.
func realtimeAuthTargets(app core.App, collectionId, recordId string) []realtimeAuthTarget {
	var targets []realtimeAuthTarget

	for _, chunk := range app.SubscriptionsBroker().ChunkedClients(clientsChunkSize) {
		for _, client := range chunk {
			if auth, _ := client.Get(RealtimeClientAuthKey).(*core.Record); auth != nil &&
				auth.Id == recordId && auth.Collection().Id == collectionId {
				targets = append(targets, realtimeAuthTarget{client, auth})
			}
		}
	}

	return targets
}

// realtimeApplyAuthTargets replaces (record != nil, unless its token key changed) or
// unsets (record == nil) the auth of the captured clients, each only if it still has
// the auth it was captured with.
func realtimeApplyAuthTargets(targets []realtimeAuthTarget, record *core.Record) {
	for _, t := range targets {
		realtimeAuthSwap(t.client, t.auth, func() {
			if record == nil || t.auth.TokenKey() != record.TokenKey() {
				t.client.Unset(RealtimeClientAuthKey)
			} else {
				t.client.Set(RealtimeClientAuthKey, record)
			}
			t.client.Unset(realtimeClientUnverifiedKey)
			t.client.Unset(realtimeClientSweepFailuresKey)
		})
	}
}

// realtimeSweepAuth re-validates the auth of every authenticated realtime client
// of this instance against the database:
//
//   - the record is gone, its token key changed, or its (auth) collection is
//     not visible any more: the client is unset;
//   - otherwise the cached record is replaced;
//   - when the records can't be read the clients are marked unverified (no
//     events are delivered to them) and unset after realtimeSweepMaxFailures
//     failed sweeps in a row.
func realtimeSweepAuth(app core.App) {
	type target struct {
		client subscriptions.Client
		auth   *core.Record
	}

	byCollection := map[string][]target{}

	for _, chunk := range app.SubscriptionsBroker().ChunkedClients(clientsChunkSize) {
		for _, client := range chunk {
			if auth, _ := client.Get(RealtimeClientAuthKey).(*core.Record); auth != nil {
				id := auth.Collection().Id
				byCollection[id] = append(byCollection[id], target{client, auth})
			}
		}
	}

	for collectionId, targets := range byCollection {
		collection, err := app.FindCachedCollectionByNameOrId(collectionId)
		if err != nil || (core.AsBaseApp(app) != nil && !core.AsBaseApp(app).CollectionVisible(collection)) {
			for _, t := range targets {
				realtimeAuthSwap(t.client, t.auth, func() { t.client.Unset(RealtimeClientAuthKey) })
			}
			continue
		}

		for start := 0; start < len(targets); start += realtimeSweepChunkSize {
			end := min(start+realtimeSweepChunkSize, len(targets))
			chunk := targets[start:end]

			ids := make([]string, 0, len(chunk))
			for _, t := range chunk {
				ids = append(ids, t.auth.Id)
			}

			records, err := app.FindRecordsByIds(collection, ids)
			if err != nil {
				for _, t := range chunk {
					realtimeAuthSwap(t.client, t.auth, func() {
						failures, _ := t.client.Get(realtimeClientSweepFailuresKey).(int)
						failures++
						if failures >= realtimeSweepMaxFailures {
							t.client.Unset(RealtimeClientAuthKey)
							t.client.Unset(realtimeClientUnverifiedKey)
							t.client.Unset(realtimeClientSweepFailuresKey)
							return
						}
						t.client.Set(realtimeClientSweepFailuresKey, failures)
						t.client.Set(realtimeClientUnverifiedKey, true)
					})
				}
				continue
			}

			byId := make(map[string]*core.Record, len(records))
			for _, r := range records {
				byId[r.Id] = r
			}

			for _, t := range chunk {
				realtimeApplySweepResult(t.client, t.auth, byId[t.auth.Id])
			}
		}
	}
}

// realtimeApplySweepResult applies what the sweep read for a client: only if the
// client still has the auth record it was read for.
func realtimeApplySweepResult(client subscriptions.Client, captured, current *core.Record) {
	realtimeAuthSwap(client, captured, func() {
		switch {
		case current == nil, current.TokenKey() != captured.TokenKey():
			client.Unset(RealtimeClientAuthKey)
		default:
			client.Set(RealtimeClientAuthKey, current)
		}

		client.Unset(realtimeClientUnverifiedKey)
		client.Unset(realtimeClientSweepFailuresKey)
	})
}
