package core_test

import (
	"errors"
	"github.com/pocketbase/pocketbase/core"
	"testing"

	"github.com/pocketbase/pocketbase/tests"
)

func TestInstanceSettings(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	shared := app.Settings().Meta.AppName

	if err := app.EnableInstanceSettings(""); err != nil {
		t.Fatal(err)
	}
	app.Settings().Meta.AppName = "event"
	app.Settings().S3.Bucket = "shared-bucket"
	if err := app.Save(app.Settings()); err != nil {
		t.Fatal(err)
	}
	if err := app.ReloadSettings(); err != nil {
		t.Fatal(err)
	}
	if app.Settings().Meta.AppName != "event" {
		t.Fatalf("expected the instance settings, got %q", app.Settings().Meta.AppName)
	}
	if app.Settings().S3.Bucket != "shared-bucket" {
		t.Fatal("expected S3 to be saved to the shared row")
	}

	if err := app.DisableInstanceSettings(); err != nil {
		t.Fatal(err)
	}
	if got := app.Settings().Meta.AppName; got != shared {
		t.Fatalf("expected the shared app name %q, got %q", shared, got)
	}
	if app.Settings().S3.Bucket != "shared-bucket" {
		t.Fatal("expected the shared S3 bucket to persist")
	}
}

func TestInstanceScopeCanHideAuthCollections(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.SetInstanceScope(core.InstanceScope{Mode: "deny", Collections: []string{"users"}}); err != nil {
		t.Fatalf("expected hiding an auth collection to be allowed: %v", err)
	}
	if app.CollectionVisible(users) {
		t.Fatal("expected users to be hidden")
	}
	su, _ := app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	if !app.CollectionVisible(su) {
		t.Fatal("system collections must stay visible")
	}
	if err := app.SetInstanceScope(core.InstanceScope{Mode: "deny", Collections: []string{core.CollectionNameSuperusers}}); err == nil {
		t.Fatal("expected hiding a system collection to fail")
	}
}

func TestInstanceScopeCacheIgnoresRolledBackTransactions(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	if !app.CollectionVisible(users) { // warms the cache with scope "all"
		t.Fatal("expected users to be visible")
	}

	rollback := errors.New("rollback")
	err = app.RunInTransaction(func(tx core.App) error {
		txBase := core.AsBaseApp(tx)
		if err := txBase.SetInstanceScope(core.InstanceScope{Mode: "deny", Collections: []string{"users"}}); err != nil {
			return err
		}
		if s, _ := txBase.InstanceScope(); s.Mode != "deny" {
			t.Fatalf("expected the transaction to see its own scope, got %+v", s)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("unexpected error %v", err)
	}
	if s, _ := app.InstanceScope(); s.Mode != "all" {
		t.Fatalf("expected the rolled back scope to be forgotten, got %+v", s)
	}
	if !app.CollectionVisible(users) {
		t.Fatal("expected users to stay visible after the rollback")
	}

	// a committed change is seen right away
	if err := app.SetInstanceScope(core.InstanceScope{Mode: "deny", Collections: []string{"users"}}); err != nil {
		t.Fatal(err)
	}
	if app.CollectionVisible(users) {
		t.Fatal("expected users to be hidden after the scope change")
	}
}
