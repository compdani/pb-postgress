package apis

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
)

func newSweepTestRecord(id, tokenKey string) *core.Record {
	r := core.NewRecord(core.NewAuthCollection("sweep_unit_users"))
	r.Id = id
	r.Set("tokenKey", tokenKey)
	return r
}

func TestRealtimeAuthSwapOnlyAppliesToTheCapturedAuth(t *testing.T) {
	captured := newSweepTestRecord("u1", "k")
	client := subscriptions.NewDefaultClient()
	client.Set(RealtimeClientAuthKey, captured)

	// the auth changed (a newer authentication or a revocation) while the decision was made
	client.Unset(RealtimeClientAuthKey)

	ran := realtimeAuthSwap(client, captured, func() { client.Set(RealtimeClientAuthKey, captured) })
	if ran || client.Get(RealtimeClientAuthKey) != nil {
		t.Fatal("a decision based on an outdated auth resurrected a revoked client")
	}

	// still the same auth: applied
	client.Set(RealtimeClientAuthKey, captured)
	if !realtimeAuthSwap(client, captured, func() { client.Unset(RealtimeClientAuthKey) }) || client.Get(RealtimeClientAuthKey) != nil {
		t.Fatal("a decision based on the current auth was not applied")
	}
}

func TestRealtimeSweepResultNeverOverridesANewerAuth(t *testing.T) {
	captured := newSweepTestRecord("u1", "k")
	current := newSweepTestRecord("u1", "k")

	// unchanged record: the cached auth is replaced
	client := subscriptions.NewDefaultClient()
	client.Set(RealtimeClientAuthKey, captured)
	realtimeApplySweepResult(client, captured, current)
	if client.Get(RealtimeClientAuthKey) != current {
		t.Fatal("the cached record must be replaced when the token key is the same")
	}

	// the record is gone or its token key changed: unset
	for name, record := range map[string]*core.Record{"missing": nil, "rotated": newSweepTestRecord("u1", "other")} {
		client := subscriptions.NewDefaultClient()
		client.Set(RealtimeClientAuthKey, captured)
		realtimeApplySweepResult(client, captured, record)
		if client.Get(RealtimeClientAuthKey) != nil {
			t.Fatalf("%s: the client must be unset", name)
		}
	}

	// the client re-authenticated (or was revoked) while the sweep was reading:
	// the result of the sweep is for an auth it no longer has
	client = subscriptions.NewDefaultClient()
	newer := newSweepTestRecord("u1", "newer")
	client.Set(RealtimeClientAuthKey, captured)
	client.Set(RealtimeClientAuthKey, newer) // a new subscription
	realtimeApplySweepResult(client, captured, nil)
	if client.Get(RealtimeClientAuthKey) != newer {
		t.Fatal("a sweep result for the previous auth must not touch a newer authentication")
	}

	client = subscriptions.NewDefaultClient()
	client.Set(RealtimeClientAuthKey, captured)
	client.Unset(RealtimeClientAuthKey) // revoked meanwhile
	realtimeApplySweepResult(client, captured, current)
	if client.Get(RealtimeClientAuthKey) != nil {
		t.Fatal("a sweep result must not bring back a client that was revoked meanwhile")
	}
}

func TestRealtimeRemoteAuthUpdateIsAppliedToTheCapturedAuthOnly(t *testing.T) {
	captured := newSweepTestRecord("u1", "k")
	client := subscriptions.NewDefaultClient()
	client.Set(RealtimeClientAuthKey, captured)

	targets := []realtimeAuthTarget{{client, captured}}

	// the client authenticates again while the record is being read
	newer := newSweepTestRecord("u1", "newer")
	client.Set(RealtimeClientAuthKey, newer)

	// what was read is for the auth that was captured: it must not touch the newer one
	realtimeApplyAuthTargets(targets, nil)
	if client.Get(RealtimeClientAuthKey) != newer {
		t.Fatal("a remote auth update overwrote an authentication made while it was being read")
	}

	// the captured auth is still the client's: applied
	client.Set(RealtimeClientAuthKey, captured)
	realtimeApplyAuthTargets(targets, nil)
	if client.Get(RealtimeClientAuthKey) != nil {
		t.Fatal("a remote revocation was not applied to the captured auth")
	}
}
