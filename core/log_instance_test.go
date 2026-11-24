package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/logger"
	"github.com/pocketbase/pocketbase/tools/types"
)

func TestLogsInstanceIsolation(t *testing.T) {
	a, b := newTwoInstances(t)
	a.Settings().Logs.MaxDays = 7
	b.Settings().Logs.MaxDays = 7
	ctx := context.WithValue(context.Background(), logger.BlockKey, true)
	for _, app := range []*core.BaseApp{a, b} {
		app.Logger().Warn("instance log")
		if err := app.Logger().Handler().(*logger.BatchHandler).WriteAll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var ids []string
	for _, app := range []*core.BaseApp{a, b} {
		var logs []*core.Log
		if err := app.LogQuery().All(&logs); err != nil {
			t.Fatal(err)
		}
		if len(logs) != 1 {
			t.Fatalf("expected one local log, got %d", len(logs))
		}
		instanceID, _ := app.InstanceID()
		if logs[0].InstanceID != instanceID {
			t.Fatalf("wrong instance: %s", logs[0].InstanceID)
		}
		ids = append(ids, logs[0].Id)
		stats, err := app.LogsStats(nil)
		if err != nil || len(stats) != 1 || stats[0].Total != 1 {
			t.Fatalf("unexpected stats: %+v, %v", stats, err)
		}
	}
	if _, err := a.FindLogById(ids[1]); err == nil {
		t.Fatal("read another instance's log")
	}
	if err := a.DeleteOldLogs(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := b.CountLogs(); err != nil || n != 1 {
		t.Fatalf("retention deleted another instance's logs: %d, %v", n, err)
	}

	// Settings changes must also leave the other instance's logs alone.
	local := &core.Log{Message: "local", Created: types.NowDateTime()}
	local.Id = core.GenerateDefaultRandomId()
	if err := a.AuxSave(local); err != nil {
		t.Fatal(err)
	}
	a.Settings().Logs.MaxDays = 0
	if err := a.Save(a.Settings()); err != nil {
		t.Fatal(err)
	}
	if n, err := b.CountLogs(); err != nil || n != 1 {
		t.Fatalf("settings cleanup deleted another instance's logs: %d, %v", n, err)
	}
}
