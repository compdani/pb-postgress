package core_test

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// A rough measurement of what the global events lock costs: several instances
// create records concurrently on one database, with the bus off and on.
//
//	PB_TEST_BENCH_BUS=1 PB_TEST_DB_DSN=... go test ./core -run TestRealtimeBusCommitThroughput -v
func TestRealtimeBusCommitThroughput(t *testing.T) {
	if os.Getenv("PB_TEST_BENCH_BUS") != "1" {
		t.Skip("set PB_TEST_BENCH_BUS=1 to run the commit throughput measurement")
	}

	run := func(bus bool) float64 {
		cfg := tests.NewDatabaseConfig(t)
		cfg.RealtimeBus = bus

		var apps []*core.BaseApp
		for i := 0; i < 4; i++ {
			c := cfg
			if i > 0 {
				c.DataDir = t.TempDir()
			}
			app := core.NewBaseApp(c)
			if err := app.Bootstrap(); err != nil {
				t.Fatal(err)
			}
			defer app.ClearBootstrap()
			apps = append(apps, app)
		}

		collection := core.NewBaseCollection("bench_posts")
		collection.Fields.Add(&core.TextField{Name: "title"})
		if err := apps[0].Save(collection); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond) // let the others see it

		var total atomic.Int64
		stop := time.Now().Add(4 * time.Second)

		var wg sync.WaitGroup
		for _, app := range apps {
			c, err := app.FindCachedCollectionByNameOrId("bench_posts")
			if err != nil {
				t.Fatal(err)
			}
			wg.Add(1)
			go func(app *core.BaseApp) {
				defer wg.Done()
				for time.Now().Before(stop) {
					r := core.NewRecord(c)
					r.Set("title", "x")
					if err := app.Save(r); err != nil {
						t.Error(err)
						return
					}
					total.Add(1)
				}
			}(app)
		}
		wg.Wait()

		return float64(total.Load()) / 4.0
	}

	off := run(false)
	on := run(true)

	t.Logf("4 instances creating records: bus off %.0f/s, bus on %.0f/s (%.1f%%)", off, on, on/off*100)
}
