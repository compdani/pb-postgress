package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDatabaseConfigAndDialect(t *testing.T) {
	for _, driver := range []string{"sqlite", "pgx", "mysql", "mariadb"} {
		if (DatabaseConfig{Driver: driver, DSN: "dsn"}).Validate() == nil {
			t.Fatalf("accepted driver %s", driver)
		}
	}
	dir := t.TempDir()
	c := DatabaseConfig{Driver: "postgres", DSN: "secret", InstanceID: "instance"}
	if err := SaveDatabaseConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "db.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("bootstrap permissions: %v %v", info, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "db.json"))
	var got DatabaseConfig
	if json.Unmarshal(raw, &got) != nil || got != c {
		t.Fatalf("bootstrap config: %+v", got)
	}
	if DialectPostgres.Quote(`a"b`) != `"a""b"` {
		t.Fatal("identifier escaping")
	}
	if err := (DatabaseConfig{DSN: "x"}).Validate(); err != nil {
		t.Fatalf("expected an empty driver to mean postgres: %v", err)
	}
}
func TestFileChangeDiff(t *testing.T) {
	c := NewBaseCollection("files")
	c.Fields.Add(&FileField{Name: "one"}, &FileField{Name: "many", MaxSelect: 5})
	old := NewRecord(c)
	old.Id = "record"
	old.Set("one", "old.txt")
	old.Set("many", []string{"keep.txt", "remove.txt"})
	current := old.Fresh()
	current.Set("one", "new.txt")
	current.Set("many", []string{"keep.txt", "add.txt"})
	changes := fileChanges(old, current)
	if len(changes) != 4 {
		t.Fatalf("diff %+v", changes)
	}
	if len(fileChanges(old, old)) != 0 {
		t.Fatal("unchanged files emitted events")
	}
	if len(fileChanges(old, nil)) != 3 {
		t.Fatal("delete missing events")
	}
}
func TestInstanceScopeValidationVisibility(t *testing.T) {
	c := NewBaseCollection("hidden")
	if scopeVisible(InstanceScope{Mode: "deny", Collections: []string{"hidden"}}, c) {
		t.Fatal("deny ignored")
	}
	if scopeVisible(InstanceScope{Mode: "allow", Collections: []string{"other"}}, c) {
		t.Fatal("allow ignored")
	}
	c.System = true
	if !scopeVisible(InstanceScope{Mode: "deny", Collections: []string{"hidden"}}, c) {
		t.Fatal("system hidden")
	}
}
