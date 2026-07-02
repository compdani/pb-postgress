package core_test

import (
	"errors"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestRunSatelliteCascadePassthrough(t *testing.T) {
	testApp, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer testApp.Cleanup()

	called := false
	err = core.RunSatelliteCascade(testApp, func(txApp core.App) error {
		called = true
		if txApp != testApp {
			t.Fatalf("expected passthrough app, got %T", txApp)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected cascade fn to be called")
	}
}

func TestRunSatelliteCascadePostgres(t *testing.T) {
	testApp, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer testApp.Cleanup()

	if !testApp.HasPostgres() {
		t.Skip("postgres is not configured")
	}

	rolledBack := false
	err = testApp.RunSatelliteCascade(func(txApp core.App) error {
		rolledBack = true
		return errors.New("rollback")
	})
	if err == nil {
		t.Fatal("expected transaction error")
	}
	if !rolledBack {
		t.Fatal("expected cascade fn to run inside postgres transaction")
	}
}
