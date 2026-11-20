package core_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/security"
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

	if l := len(export["message"].(string)); l != 8000 {
		t.Fatalf("expected message truncated to 8000, got %d", l)
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

func TestLogDBExportSanitizesMessage(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	log := &core.Log{
		Message: "GET /php-cgi?\xadd+x=\x00y",
		Data:    types.JSONMap[any]{"a": "x\x00y", "b": `\u0000`, "c": "\\\x00"},
	}

	export, err := log.DBExport(app)
	if err != nil {
		t.Fatal(err)
	}

	expected := "GET /php-cgi?\uFFFDd+x=\uFFFDy"
	if msg := export["message"].(string); msg != expected {
		t.Fatalf("expected %q, got %q", expected, msg)
	}

	// a NUL becomes U+FFFD (also right after an escaped backslash),
	// a literal "\u0000" text is kept
	data := map[string]any{}
	if err := json.Unmarshal(export["data"].(types.JSONRaw), &data); err != nil {
		t.Fatal(err)
	}
	if data["a"] != "x\uFFFDy" || data["b"] != `\u0000` || data["c"] != "\\\uFFFD" {
		t.Fatalf("unexpected data %q", data)
	}
}

// The message column is TEXT without a btree index, so long messages are
// stored in full (up to the 8000 characters cap), and a NUL in the data
// doesn't fail the jsonb column.
func TestLogSaveLongMessage(t *testing.T) {
	app, _ := tests.NewTestApp(t)
	defer app.Cleanup()

	message := security.RandomString(7000) // random, so it doesn't compress
	log := &core.Log{Message: message, Data: types.JSONMap[any]{"a": "x\x00y"}}
	log.Id = core.GenerateDefaultRandomId()
	if err := app.AuxSave(log); err != nil {
		t.Fatal(err)
	}

	stored, err := app.FindLogById(log.Id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Message != message {
		t.Fatalf("expected the full message, got %d characters", len(stored.Message))
	}
}
