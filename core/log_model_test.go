package core_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/types"
)

func TestLogDBExportTruncatesData(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	app.Settings().Logs.MaxDataSize = 100

	log := &core.Log{
		Message: strings.Repeat("m", 9000),
		Data: types.JSONMap[any]{
			"a":   "short",
			"big": strings.Repeat("x", 500),
			"z":   1,
		},
	}

	export, err := log.DBExport(app)
	if err != nil {
		t.Fatal(err)
	}

	if l := len(export["message"].(string)); l != 255 {
		t.Fatalf("expected message truncated to 255, got %d", l)
	}

	raw := export["data"].(types.JSONRaw)
	if len(raw) > 100 {
		t.Fatalf("expected data <= 100 bytes, got %d", len(raw))
	}

	decoded := map[string]any{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("expected valid json: %v", err)
	}
	if decoded["__pb_truncated__"] != true || decoded["a"] != "short" || decoded["z"] != float64(1) {
		t.Fatalf("unexpected truncated data: %s", raw)
	}
	if _, ok := decoded["big"]; ok {
		t.Fatalf("expected oversized entry to be dropped: %s", raw)
	}

	// untouched when under the limit
	app.Settings().Logs.MaxDataSize = 0
	export, _ = log.DBExport(app)
	if strings.Contains(string(export["data"].(types.JSONRaw)), "__pb_truncated__") {
		t.Fatal("did not expect truncation under the default limit")
	}
}
