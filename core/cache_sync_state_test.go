package core

import "testing"

func testCollection(id, name string, system bool) *Collection {
	c := &Collection{}
	c.Id = id
	c.Name = name
	c.System = system
	return c
}

func TestScopeStateUnionOnlyTightens(t *testing.T) {
	s := &cacheSync{}

	// a union without a previous state is just the given snapshot
	s.setState(map[string]bool{"a": true}, true, InstanceScope{Mode: "deny"})
	if st := s.state.Load(); !st.hidden["a"] || !st.pending {
		t.Fatalf("unexpected state: %+v", st)
	}

	// an exact replace may relax and clears the pending mark
	s.setState(map[string]bool{"a": false, "b": true}, false, InstanceScope{Mode: "deny"})
	if st := s.state.Load(); st.hidden["a"] || !st.hidden["b"] || st.pending {
		t.Fatalf("exact replace must relax: %+v", st)
	}

	// a union keeps what was hidden hidden, adds what is newly hidden, never relaxes
	s.setState(map[string]bool{"a": false, "b": false, "c": true}, true, InstanceScope{Mode: "deny"})
	st := s.state.Load()
	if st.hidden["a"] || !st.hidden["b"] || !st.hidden["c"] || !st.pending {
		t.Fatalf("union must only tighten: %+v", st)
	}
}

func TestVisibleUnderUsesTheSnapshotForKnownIds(t *testing.T) {
	cs := &cacheSync{}
	app := &BaseApp{cacheSync: cs}

	old := testCollection("id1", "old_name", false)
	renamedElsewhere := testCollection("id1", "new_name", false)
	unknown := testCollection("id2", "created_later", false)
	system := testCollection("id3", "_system", true)

	denyNew := InstanceScope{Mode: "deny", Collections: []string{"new_name", "created_later"}}

	// no snapshot yet: the live scope decides (by name)
	if !app.visibleUnder(denyNew, old) {
		t.Fatal("without a snapshot the name-based scope applies")
	}

	// the snapshot knows id1 as hidden (it was denied under its old name)
	cs.setState(map[string]bool{"id1": true}, false, InstanceScope{Mode: "deny", Collections: []string{"old_name"}})

	if app.visibleUnder(denyNew, old) || app.visibleUnder(denyNew, renamedElsewhere) {
		t.Fatal("a known hidden id must stay hidden whatever its cached name is")
	}
	if app.visibleUnder(denyNew, unknown) {
		t.Fatal("an unknown id falls back to the live scope, which hides it")
	}
	if !app.visibleUnder(denyNew, system) {
		t.Fatal("system collections are always visible")
	}

	// the snapshot knows id1 as visible: the live scope cannot hide it before the snapshot moves
	cs.setState(map[string]bool{"id1": false}, false, InstanceScope{Mode: "all"})
	if !app.visibleUnder(denyNew, renamedElsewhere) {
		t.Fatal("a known visible id follows the snapshot")
	}
}

func TestSnapshotHiddenSkipsSystemCollections(t *testing.T) {
	scope := InstanceScope{Mode: "allow", Collections: []string{"a"}}
	hidden := snapshotHidden(scope, []*Collection{
		testCollection("1", "a", false),
		testCollection("2", "b", false),
		testCollection("3", "_s", true),
	})
	if hidden["1"] || !hidden["2"] {
		t.Fatalf("unexpected: %v", hidden)
	}
	if _, ok := hidden["3"]; ok {
		t.Fatal("system collections are not part of the snapshot")
	}
}

func TestStaleSnapshotCannotRelaxANewerLocalDecision(t *testing.T) {
	s := &cacheSync{}
	deny := InstanceScope{Mode: "deny", Collections: []string{"x"}}
	all := InstanceScope{Mode: "all"}

	// a sync starts reading (its scope read sees "all"), ...
	since := s.epoch.Load()

	// ... then this instance saves a deny scope, which is installed right away
	s.setState(map[string]bool{"id": true}, false, deny)

	// the sync finishes with its stale pair and tries to relax the id
	s.setStateSince(since, map[string]bool{"id": false}, false, all)

	st := s.state.Load()
	if !st.hidden["id"] {
		t.Fatal("a stale snapshot undid a newer local deny")
	}
	if !st.pending {
		t.Fatal("a snapshot that lost the race must stay pending so the next consistent one replaces it")
	}
	if st.scope.Mode != "deny" {
		t.Fatalf("the newer decision's scope must be kept, got %+v", st.scope)
	}

	// a sync whose reads began after the last install may relax
	since = s.epoch.Load()
	s.setStateSince(since, map[string]bool{"id": false}, false, all)
	if st := s.state.Load(); st.hidden["id"] || st.pending {
		t.Fatalf("an up to date exact snapshot must be able to relax: %+v", st)
	}
}

func TestBestEffortHiddenFailsClosedWhenUnresolved(t *testing.T) {
	published := []*Collection{
		testCollection("1", "renamed_elsewhere_old", false),
		testCollection("2", "plain", false),
		testCollection("3", "_system", true),
	}
	scope := InstanceScope{Mode: "deny", Collections: []string{"renamed_elsewhere_new"}}
	db := []*Collection{
		testCollection("1", "renamed_elsewhere_new", false),
		testCollection("2", "plain", false),
	}

	// everything read: the scope is resolved against both views
	hidden := bestEffortHidden(&scope, published, db)
	if !hidden["1"] || hidden["2"] {
		t.Fatalf("unexpected resolution: %v", hidden)
	}

	// the database collections could not be read: nothing can tell what the new
	// name belongs to, so every cached non-system collection is hidden
	hidden = bestEffortHidden(&scope, published, nil)
	if !hidden["1"] || !hidden["2"] {
		t.Fatalf("an unresolved fallback must fail closed: %v", hidden)
	}
	if _, ok := hidden["3"]; ok {
		t.Fatal("system collections are not part of the snapshot")
	}

	// the scope could not be read: same
	hidden = bestEffortHidden(nil, published, db)
	if !hidden["1"] || !hidden["2"] {
		t.Fatalf("an unreadable scope must fail closed: %v", hidden)
	}
}
