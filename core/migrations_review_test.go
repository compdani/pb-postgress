package core_test

import (
	"context"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// useReviewMigrations replaces the global app migrations for a (non parallel) test.
func useReviewMigrations(t *testing.T) {
	old := core.AppMigrations
	t.Cleanup(func() { core.AppMigrations = old })

	list := core.MigrationsList{}
	list.Register(func(txApp core.App) error {
		return txApp.Save(core.NewBaseCollection("mig_a"))
	}, nil, "1_review_a.go")
	list.Register(func(txApp core.App) error {
		if err := txApp.Save(core.NewBaseCollection("mig_b")); err != nil {
			return err
		}
		a, err := txApp.FindCollectionByNameOrId("mig_a")
		if err != nil {
			return err
		}
		a.Fields.Add(&core.TextField{Name: "note"})
		return txApp.Save(a)
	}, nil, "2_review_b.go")
	core.AppMigrations = list
}

func TestMigrationsReviewPreviewAndApply(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()
	useReviewMigrations(t)

	pending, err := app.PendingAppMigrations()
	if err != nil || len(pending) != 2 {
		t.Fatalf("expected 2 pending migrations, got %v %v", pending, err)
	}

	preview, err := app.PreviewPendingMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if app.SQLDialect() != core.DialectPostgres {
		if preview.Preview || len(preview.Items) != 2 {
			t.Fatalf("unexpected preview %+v", preview)
		}
		return
	}
	if !preview.Preview || len(preview.Items) != 2 {
		t.Fatalf("unexpected preview %+v", preview)
	}
	if c := preview.Items[0].Changes; len(c) != 1 || c[0].Name != "mig_a" || c[0].Action != "created" {
		t.Fatalf("unexpected changes of the first migration: %+v", c)
	}
	second := preview.Items[1].Changes
	if len(second) != 2 || second[0].Name != "mig_a" || second[0].Action != "updated" || second[1].Name != "mig_b" || second[1].Action != "created" {
		t.Fatalf("unexpected changes of the second migration: %+v", second)
	}
	if !strings.Contains(strings.Join(second[0].Details, " "), "note") {
		t.Fatalf("expected the added field in the details, got %v", second[0].Details)
	}

	// the preview changed nothing
	if _, err := app.FindCollectionByNameOrId("mig_a"); err == nil {
		t.Fatal("expected the preview to be rolled back")
	}
	if pending, _ := app.PendingAppMigrations(); len(pending) != 2 {
		t.Fatalf("expected the migrations to stay pending, got %v", pending)
	}

	// apply, ignoring the new mig_b collection
	applied, err := app.ApplyPendingMigrations(map[string][]string{"2_review_b.go": {"mig_b"}})
	if err != nil || len(applied) != 2 {
		t.Fatalf("apply: %v %v", applied, err)
	}
	a, err := app.FindCollectionByNameOrId("mig_a")
	if err != nil || a.Fields.GetByName("note") == nil {
		t.Fatalf("expected mig_a with the note field: %v", err)
	}
	if _, err := app.FindCollectionByNameOrId("mig_b"); err == nil {
		t.Fatal("expected the ignored collection not to be created")
	}
	if pending, _ := app.PendingAppMigrations(); len(pending) != 0 {
		t.Fatalf("expected nothing pending, got %v", pending)
	}
}

func TestMigrationsReviewIgnoredDependencyRollsBack(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()
	useReviewMigrations(t)

	// the second migration needs mig_a
	_, err := app.ApplyPendingMigrations(map[string][]string{"1_review_a.go": {"mig_a"}})
	if err == nil {
		t.Fatal("expected the apply to fail")
	}
	if _, err := app.FindCollectionByNameOrId("mig_b"); err == nil {
		t.Fatal("expected everything to be rolled back")
	}
	if pending, _ := app.PendingAppMigrations(); len(pending) != 2 {
		t.Fatalf("expected the migrations to stay pending, got %v", pending)
	}
}

func TestMigrationGroupsKeepSeparateHistories(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()
	useReviewMigrations(t)

	if err := app.UseOwnMigrationGroup(); err != nil {
		t.Fatal(err)
	}
	group := app.MigrationGroup()
	if group == "" {
		t.Fatal("expected a migration group")
	}
	if _, err := app.ApplyPendingMigrations(nil); err != nil {
		t.Fatal(err)
	}

	files := []string{}
	if err := app.DB().Select("file").From(core.DefaultMigrationsTable).Column(&files); err != nil {
		t.Fatal(err)
	}
	var own, shared int
	for _, f := range files {
		if strings.HasPrefix(f, group+"/") {
			own++
		} else if strings.Contains(f, "review") {
			shared++
		}
	}
	if own != 2 || shared != 0 {
		t.Fatalf("expected 2 rows in the instance's group, got own=%d shared=%d (%v)", own, shared, files)
	}

	// the shared group has its own history
	if err := app.SetMigrationGroup(""); err != nil {
		t.Fatal(err)
	}
	if pending, _ := app.PendingAppMigrations(); len(pending) != 2 {
		t.Fatalf("expected the shared group to still have 2 pending, got %v", pending)
	}
}

func TestRestoreMapsSourceMigrationHistory(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()
	useReviewMigrations(t)

	// the source instance has its own migration history
	if err := app.UseOwnMigrationGroup(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyPendingMigrations(nil); err != nil {
		t.Fatal(err)
	}
	if err := app.CreateBackup(context.Background(), "history.zip"); err != nil {
		t.Fatal(err)
	}

	// restoring into the shared group brings that history with it
	if err := app.SetMigrationGroup(""); err != nil {
		t.Fatal(err)
	}
	if pending, _ := app.PendingAppMigrations(); len(pending) != 2 {
		t.Fatalf("expected 2 pending before the restore, got %v", pending)
	}
	if err := app.RestoreBackup(context.Background(), "history.zip"); err != nil {
		t.Fatal(err)
	}
	if pending, _ := app.PendingAppMigrations(); len(pending) != 0 {
		t.Fatalf("expected the restored history to cover the migrations, got %v", pending)
	}
}
