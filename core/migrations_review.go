package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/pocketbase/dbx"
)

const paramsKeyMigrationGroupPrefix = "migrationGroup:"

// MigrationGroup returns the migration group of this instance. Instances in
// the same group share the applied state of their app migrations; "" is the
// shared group every instance uses unless it was set up as a new instance.
func (app *BaseApp) MigrationGroup() string {
	if app.DB() == nil || !app.HasTable(paramsTable) {
		return ""
	}
	id, err := app.InstanceID()
	if err != nil {
		return ""
	}
	return app.migrationGroupOf(id)
}

func (app *BaseApp) migrationGroupOf(instanceID string) string {
	var group string
	err := app.DB().Select("value").From(paramsTable).
		Where(dbx.HashExp{"id": paramsKeyMigrationGroupPrefix + instanceID}).Row(&group)
	if err != nil {
		return ""
	}
	return group
}

// SetMigrationGroup sets the migration group of this instance ("" for the
// shared one).
func (app *BaseApp) SetMigrationGroup(group string) error {
	id, err := app.InstanceID()
	if err != nil {
		return err
	}
	key := paramsKeyMigrationGroupPrefix + id
	if _, err := app.DB().Delete(paramsTable, dbx.HashExp{"id": key}).Execute(); err != nil {
		return err
	}
	if group == "" {
		return nil
	}
	_, err = app.DB().Insert(paramsTable, dbx.Params{"id": key, "value": group}).Execute()
	return err
}

// UseOwnMigrationGroup gives this instance its own migration history.
func (app *BaseApp) UseOwnMigrationGroup() error {
	id, err := app.InstanceID()
	if err != nil {
		return err
	}
	return app.SetMigrationGroup(id)
}

// -------------------------------------------------------------------

// ignoresCollectionChange reports whether a collection write must be skipped
// because the superuser chose to ignore that collection while applying the
// reviewed migrations (see [BaseApp.ApplyPendingMigrations]).
func (app *BaseApp) ignoresCollectionChange(model Model) bool {
	if len(app.ignoredCollections) == 0 {
		return false
	}
	c, ok := model.(*Collection)
	if !ok {
		return false
	}
	return app.ignoredCollections[c.Id] || app.ignoredCollections[c.Name]
}

// MigrationCollectionChange is one collection a migration creates, changes or deletes.
type MigrationCollectionChange struct {
	Id      string   `json:"id"`
	Name    string   `json:"name"`
	Action  string   `json:"action"` // created | updated | deleted
	Details []string `json:"details"`
}

// PendingMigration is an app migration that was not applied yet.
type PendingMigration struct {
	File    string                      `json:"file"`
	Changes []MigrationCollectionChange `json:"changes"`
	// Error is set when the migration failed in the preview.
	Error string `json:"error,omitempty"`
}

// MigrationsPreview is the result of [BaseApp.PreviewPendingMigrations].
type MigrationsPreview struct {
	Items []PendingMigration `json:"items"`
	// Preview is false when the migrations could not be dry-run (MySQL cannot
	// roll back schema changes), so Items only lists the files.
	Preview bool `json:"preview"`
}

var migrationsReviewMu sync.Mutex

// PendingAppMigrations returns the app migrations that were not applied yet.
func (app *BaseApp) PendingAppMigrations() ([]string, error) {
	return NewMigrationsRunner(app, AppMigrations).Pending()
}

// PreviewPendingMigrations runs the pending app migrations in a transaction
// that is rolled back, and reports the collections each of them changes.
// Record and raw SQL changes are not reported.
func (app *BaseApp) PreviewPendingMigrations() (*MigrationsPreview, error) {
	migrationsReviewMu.Lock()
	defer migrationsReviewMu.Unlock()

	runner := NewMigrationsRunner(app, AppMigrations)
	result := &MigrationsPreview{Items: []PendingMigration{}}

	if app.SQLDialect() != DialectPostgres {
		files, err := runner.Pending()
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			result.Items = append(result.Items, PendingMigration{File: f, Changes: []MigrationCollectionChange{}})
		}
		return result, nil
	}
	result.Preview = true

	failed := false
	_, err := runner.upWith(true, func(txApp App, m *Migration, run func(App) error) error {
		item := PendingMigration{File: m.File, Changes: []MigrationCollectionChange{}}
		if failed {
			// an earlier migration failed, so this one cannot be previewed
			result.Items = append(result.Items, item)
			return nil
		}
		before, err := collectionsSnapshot(txApp)
		if err != nil {
			return err
		}
		if err := run(txApp); err != nil {
			item.Error = err.Error()
			result.Items = append(result.Items, item)
			failed = true
			// the transaction is unusable after an error; stop here
			return errMigrationsDryRun
		}
		after, err := collectionsSnapshot(txApp)
		if err != nil {
			return err
		}
		item.Changes = diffCollections(before, after)
		result.Items = append(result.Items, item)
		return nil
	})
	// the cached collections must match the database again
	reloadErr := app.ReloadCachedCollections()
	if err != nil && !errors.Is(err, errMigrationsDryRun) {
		return nil, err
	}
	if failed {
		// list the migrations after the failed one
		files, err := runner.Pending()
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if !slices.ContainsFunc(result.Items, func(i PendingMigration) bool { return i.File == f }) {
				result.Items = append(result.Items, PendingMigration{File: f, Changes: []MigrationCollectionChange{}})
			}
		}
	}
	return result, reloadErr
}

// ApplyPendingMigrations applies the pending app migrations. ignore lists,
// per migration file, the collections (id or name) whose create, update and
// delete are skipped while that migration runs. Everything is rolled back
// when a migration fails, for example because it depends on a skipped change.
func (app *BaseApp) ApplyPendingMigrations(ignore map[string][]string) ([]string, error) {
	migrationsReviewMu.Lock()
	defer migrationsReviewMu.Unlock()

	runner := NewMigrationsRunner(app, AppMigrations)
	applied, err := runner.upWith(false, func(txApp App, m *Migration, run func(App) error) error {
		names := ignore[m.File]
		ba := asBaseApp(txApp)
		if len(names) == 0 || ba == nil {
			return run(txApp)
		}
		clone := *ba
		clone.ignoredCollections = make(map[string]bool, len(names))
		for _, n := range names {
			clone.ignoredCollections[n] = true
		}
		if err := run(&clone); err != nil {
			return fmt.Errorf("%w (it may depend on a change you chose to ignore: %s)", err, strings.Join(names, ", "))
		}
		return nil
	})
	reloadErr := app.ReloadCachedCollections()
	if err != nil {
		return nil, err
	}
	return applied, reloadErr
}

// collectionsSnapshot returns every collection as generic JSON data by id.
func collectionsSnapshot(app App) (map[string]map[string]any, error) {
	collections := []*Collection{}
	if err := app.CollectionQuery().All(&collections); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	result := make(map[string]map[string]any, len(collections))
	for _, c := range collections {
		raw, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		data := map[string]any{}
		if err := json.Unmarshal(raw, &data); err != nil {
			return nil, err
		}
		result[c.Id] = data
	}
	return result, nil
}

func diffCollections(before, after map[string]map[string]any) []MigrationCollectionChange {
	changes := []MigrationCollectionChange{}
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }

	for id, a := range after {
		b, existed := before[id]
		if !existed {
			fields, _ := a["fields"].([]any)
			changes = append(changes, MigrationCollectionChange{
				Id: id, Name: str(a, "name"), Action: "created",
				Details: []string{fmt.Sprintf("%s collection with %d fields", str(a, "type"), len(fields))},
			})
			continue
		}
		if details := diffCollection(b, a); len(details) > 0 {
			changes = append(changes, MigrationCollectionChange{Id: id, Name: str(b, "name"), Action: "updated", Details: details})
		}
	}
	for id, b := range before {
		if _, ok := after[id]; !ok {
			changes = append(changes, MigrationCollectionChange{
				Id: id, Name: str(b, "name"), Action: "deleted",
				Details: []string{"the collection and all of its records are deleted"},
			})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Name < changes[j].Name })
	return changes
}

func diffCollection(b, a map[string]any) []string {
	details := []string{}
	if b["name"] != a["name"] {
		details = append(details, fmt.Sprintf("renamed to %v", a["name"]))
	}

	fieldsByID := func(m map[string]any) map[string]map[string]any {
		out := map[string]map[string]any{}
		list, _ := m["fields"].([]any)
		for _, f := range list {
			if field, ok := f.(map[string]any); ok {
				id, _ := field["id"].(string)
				out[id] = field
			}
		}
		return out
	}
	bf, af := fieldsByID(b), fieldsByID(a)
	var added, removed, changed []string
	for id, f := range af {
		old, ok := bf[id]
		switch {
		case !ok:
			added = append(added, fmt.Sprintf("%v (%v)", f["name"], f["type"]))
		case !reflect.DeepEqual(old, f):
			if old["name"] != f["name"] {
				changed = append(changed, fmt.Sprintf("%v → %v", old["name"], f["name"]))
			} else {
				changed = append(changed, fmt.Sprint(f["name"]))
			}
		}
	}
	for id, f := range bf {
		if _, ok := af[id]; !ok {
			removed = append(removed, fmt.Sprint(f["name"]))
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	if len(added) > 0 {
		details = append(details, "fields added: "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		details = append(details, "fields removed (their data is deleted): "+strings.Join(removed, ", "))
	}
	if len(changed) > 0 {
		details = append(details, "fields changed: "+strings.Join(changed, ", "))
	}

	rules := []string{}
	for _, k := range []string{"listRule", "viewRule", "createRule", "updateRule", "deleteRule"} {
		if !reflect.DeepEqual(b[k], a[k]) {
			rules = append(rules, k)
		}
	}
	if len(rules) > 0 {
		details = append(details, "rules changed: "+strings.Join(rules, ", "))
	}
	if !reflect.DeepEqual(b["indexes"], a["indexes"]) {
		details = append(details, "indexes changed")
	}
	if !reflect.DeepEqual(b["viewQuery"], a["viewQuery"]) {
		details = append(details, "view query changed")
	}

	other := []string{}
	skip := map[string]bool{"name": true, "fields": true, "indexes": true, "viewQuery": true, "created": true, "updated": true,
		"listRule": true, "viewRule": true, "createRule": true, "updateRule": true, "deleteRule": true}
	keys := map[string]bool{}
	for k := range b {
		keys[k] = true
	}
	for k := range a {
		keys[k] = true
	}
	for k := range keys {
		if !skip[k] && !reflect.DeepEqual(b[k], a[k]) {
			other = append(other, k)
		}
	}
	sort.Strings(other)
	if len(other) > 0 {
		details = append(details, "options changed: "+strings.Join(other, ", "))
	}
	return details
}
