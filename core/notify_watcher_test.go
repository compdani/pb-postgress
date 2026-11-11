package core_test

import (
	"context"
	"database/sql"
	"github.com/pocketbase/pocketbase/tests"
	"os"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/store"
	"golang.org/x/sync/semaphore"
)

func TestNotifyWatcher_SettingsUpdate(t *testing.T) {
	t.Parallel()

	testEvents := store.New[core.App, int](nil)

	tmpDir, err := os.MkdirTemp("", "pb_notify_test*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := tests.NewDatabaseConfig(t)
	app1 := core.NewBaseApp(core.BaseAppConfig{
		DataDir: tmpDir, DBDriver: cfg.DBDriver, DBDSN: cfg.DBDSN,
	})
	if err := app1.Bootstrap(); err != nil {
		t.Fatal(err)
	}

	app2 := core.NewBaseApp(core.BaseAppConfig{
		DataDir: tmpDir, DBDriver: cfg.DBDriver, DBDSN: cfg.DBDSN,
	})
	defer app1.ClearBootstrap()
	defer app2.ClearBootstrap()
	if err := app2.Bootstrap(); err != nil {
		t.Fatal(err)
	}

	timeout := time.After(3 * time.Second)
	done := make(chan struct{})

	app1.OnSettingsReload().BindFunc(func(e *core.SettingsReloadEvent) error {
		testEvents.SetFunc(app1, func(old int) int {
			return old + 1
		})
		return e.Next()
	})

	app2.OnSettingsReload().BindFunc(func(e *core.SettingsReloadEvent) error {
		testEvents.SetFunc(app2, func(old int) int {
			defer func() {
				done <- struct{}{}
			}()

			return old + 1
		})
		return e.Next()
	})

	// updating app1 settings should trigger a reload in app2
	app1.Settings().SuperuserIPs = []string{"127.0.0.1"}
	if err := app1.Save(app1.Settings()); err != nil {
		t.Fatal(err)
	}

	// wait for the event
	select {
	case <-timeout:
		t.Fatal("app2 reload event timeout")
	case <-done:
		// ready
	}

	if app1Total := testEvents.Get(app1); app1Total != 1 {
		t.Fatalf("Expected 1 app1 event, got %d", app1Total)
	}

	if app2Total := testEvents.Get(app2); app2Total != 1 {
		t.Fatalf("Expected 1 app2 event, got %d", app2Total)
	}

	// the event is emitted before the reload finished loading: wait for the loaded
	// value, reading it under the settings lock
	var app2SuperuserIPs []string
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		clone, err := app2.Settings().Clone()
		if err != nil {
			t.Fatal(err)
		}
		if app2SuperuserIPs = clone.SuperuserIPs; len(app2SuperuserIPs) == 1 {
			break
		}
	}
	if len(app2SuperuserIPs) != 1 || app2SuperuserIPs[0] != "127.0.0.1" {
		t.Fatalf("Expected exactly 127.0.0.1 superuser IP in app2 settings event, got %v", app2SuperuserIPs)
	}
}

func TestNotifyWatcher_CollectionsUpdate(t *testing.T) {
	t.Parallel()

	tmpDir, err := os.MkdirTemp("", "pb_notify_test*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := tests.NewDatabaseConfig(t)
	app1 := core.NewBaseApp(core.BaseAppConfig{
		DataDir: tmpDir, DBDriver: cfg.DBDriver, DBDSN: cfg.DBDSN,
	})
	if err := app1.Bootstrap(); err != nil {
		t.Fatal(err)
	}

	app2 := core.NewBaseApp(core.BaseAppConfig{
		DataDir: tmpDir, DBDriver: cfg.DBDriver, DBDSN: cfg.DBDSN,
	})
	defer app1.ClearBootstrap()
	defer app2.ClearBootstrap()

	// The query loggers are installed right after the bootstrap and before the
	// cache sync (which polls the same connections from its own goroutine)
	// starts, so setting them can't race with it.
	testQueries := store.New[string, []string](nil)
	app2.OnBootstrap().Bind(&hook.Handler[*core.BootstrapEvent]{
		Func: func(e *core.BootstrapEvent) error {
			if err := e.Next(); err != nil {
				return err
			}

			app2.ConcurrentDB().(*dbx.DB).QueryLogFunc = func(ctx context.Context, t time.Duration, sql string, rows *sql.Rows, err error) {
				testQueries.SetFunc("concurrent", func(old []string) []string {
					return append(old, sql)
				})
			}
			app2.ConcurrentDB().(*dbx.DB).ExecLogFunc = func(ctx context.Context, t time.Duration, sql string, result sql.Result, err error) {
				testQueries.SetFunc("concurrent", func(old []string) []string {
					return append(old, sql)
				})
			}
			app2.NonconcurrentDB().(*dbx.DB).QueryLogFunc = func(ctx context.Context, t time.Duration, sql string, rows *sql.Rows, err error) {
				testQueries.SetFunc("nonconcurrent", func(old []string) []string {
					return append(old, sql)
				})
			}
			app2.NonconcurrentDB().(*dbx.DB).ExecLogFunc = func(ctx context.Context, t time.Duration, sql string, result sql.Result, err error) {
				testQueries.SetFunc("nonconcurrent", func(old []string) []string {
					return append(old, sql)
				})
			}

			return nil
		},
		Priority: -996, // after the bootstrap, before the cache sync starts (-997 continues)
	})
	if err := app2.Bootstrap(); err != nil {
		t.Fatal(err)
	}

	// what the bootstrap itself ran is not part of the test
	testQueries.Remove("concurrent")
	testQueries.Remove("nonconcurrent")

	ctx, cancelCtx := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancelCtx()

	sem := semaphore.NewWeighted(1)
	sem.Acquire(ctx, 1)

	// currently there is no hook for the collections cache reload so we pool instead
	done := make(chan bool, 1)
	ticker := time.NewTicker(100 * time.Millisecond)
	go func() {
		for {
			select {
			case <-ticker.C:
				if countQuery(testQueries.Get("concurrent"), "SELECT {{_collections}}.* FROM \"_collections\" ORDER BY \"created\" ASC, \"id\" ASC") >= 1 {
					sem.Release(1)
					return
				}
			case <-done:
				return
			}
		}
	}()

	// create/update/delete app1 collections should trigger a reload in app2
	dummyCollection := core.NewBaseCollection("test")
	if err := app1.Save(dummyCollection); err != nil {
		t.Fatal(err)
	}
	dummyCollection.Fields.Add(&core.TextField{Name: "test"})
	if err := app1.Save(dummyCollection); err != nil {
		t.Fatal(err)
	}
	if err := app1.Delete(dummyCollection); err != nil {
		t.Fatal(err)
	}

	// block until released or timeouted
	sem.Acquire(ctx, 1)
	ticker.Stop()
	done <- true

	nonconcurrentQueries := testQueries.Get("nonconcurrent")
	concurrentQueries := testQueries.Get("concurrent")

	if len(nonconcurrentQueries) != 0 {
		t.Fatalf("Expected 0 concurrent queries, got %d (%v)", len(nonconcurrentQueries), nonconcurrentQueries)
	}

	// the cache sync also polls the shared versions, so only the reload query is checked
	expectedQuery := "SELECT {{_collections}}.* FROM \"_collections\" ORDER BY \"created\" ASC, \"id\" ASC"
	if countQuery(concurrentQueries, expectedQuery) < 1 {
		t.Fatalf("Expected at least one %q, got %v", expectedQuery, concurrentQueries)
	}
}

func countQuery(queries []string, query string) int {
	n := 0
	for _, q := range queries {
		if q == query {
			n++
		}
	}
	return n
}
