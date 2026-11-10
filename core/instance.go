package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
)

type InstanceScope struct {
	Mode        string   `json:"mode"`
	Collections []string `json:"collections"`
}
type Instance struct {
	ID               string `db:"id" json:"id"`
	Name             string `db:"name" json:"name"`
	URL              string `db:"url" json:"url"`
	ScopeMode        string `db:"scopeMode" json:"scopeMode"`
	ScopeCollections string `db:"scopeCollections" json:"scopeCollections"`
	CreatedAt        string `db:"created_at" json:"created_at"`
}

func (app *BaseApp) initInstance() error {
	app.resetScopeCache()
	id, err := app.InstanceID()
	if err != nil {
		return err
	}
	var count int
	if err = app.DB().Select("count(*)").From("_instances").Where(dbx.HashExp{"id": id}).Row(&count); err != nil {
		return err
	}
	if count == 0 {
		_, err = app.DB().Insert("_instances", dbx.Params{"id": id, "name": id, "url": "", "scopeMode": "all", "scopeCollections": "[]", "created_at": time.Now().UTC().Format(time.RFC3339)}).Execute()
	}
	if err == nil {
		_, _ = app.InstanceScope() // warm the cache
	}
	return err
}
func (app *BaseApp) Instances() ([]*Instance, error) {
	rows := []*Instance{}
	err := app.DB().Select("*").From("_instances").OrderBy("created_at").All(&rows)
	return rows, err
}
func (app *BaseApp) InstanceScope() (InstanceScope, error) {
	if scope, ok := app.cachedScope(); ok {
		return scope, nil
	}
	gen := app.scopeGeneration()
	scope, err := app.loadInstanceScope()
	if err == nil {
		app.setCachedScope(scope, gen)
	}
	return scope, err
}

func (app *BaseApp) loadInstanceScope() (InstanceScope, error) {
	id, err := app.InstanceID()
	if err != nil {
		return InstanceScope{}, err
	}
	row := Instance{}
	if err = app.DB().Select("scopeMode", "scopeCollections").From("_instances").Where(dbx.HashExp{"id": id}).One(&row); err != nil {
		return InstanceScope{}, err
	}
	scope := InstanceScope{Mode: row.ScopeMode}
	err = json.Unmarshal([]byte(row.ScopeCollections), &scope.Collections)
	return scope, err
}
func scopeVisible(scope InstanceScope, c *Collection) bool {
	if c.System {
		return true
	}
	switch scope.Mode {
	case "allow":
		return slices.Contains(scope.Collections, c.Name)
	case "deny":
		return !slices.Contains(scope.Collections, c.Name)
	default:
		return true
	}
}
func (app *BaseApp) CollectionVisible(c *Collection) bool {
	if c == nil {
		return false
	}
	if c.System {
		return true
	}
	if scope, ok := app.cachedScope(); ok {
		return scopeVisible(scope, c)
	}
	if !app.IsBootstrapped() || !app.HasTable("_instances") {
		return true
	}
	scope, err := app.InstanceScope()
	return err == nil && scopeVisible(scope, c)
}
func (app *BaseApp) SetInstanceScope(scope InstanceScope) error {
	if scope.Mode != "all" && scope.Mode != "allow" && scope.Mode != "deny" {
		return errors.New("scope mode must be all, allow or deny")
	}
	collections, err := app.FindAllCollections()
	if err != nil {
		return err
	}
	byName := map[string]*Collection{}
	byID := map[string]*Collection{}
	for _, c := range collections {
		byName[c.Name] = c
		byID[c.Id] = c
	}
	// names of collections that no longer exist (deleted or renamed from
	// another instance) are dropped instead of rejecting the whole scope
	known := make([]string, 0, len(scope.Collections))
	for _, name := range scope.Collections {
		c := byName[name]
		if c == nil || slices.Contains(known, name) {
			continue
		}
		if scope.Mode == "deny" && c.System {
			return fmt.Errorf("system collections cannot be hidden: %s", name)
		}
		known = append(known, name)
	}
	scope.Collections = known
	for _, c := range collections {
		if !scopeVisible(scope, c) {
			continue
		}
		for _, f := range c.Fields {
			if rel, ok := f.(*RelationField); ok && rel.Required {
				target := byID[rel.CollectionId]
				if target != nil && !scopeVisible(scope, target) {
					return fmt.Errorf("visible collection %s has required relation to hidden collection %s", c.Name, target.Name)
				}
			}
		}
	}
	return app.writeInstanceScope(scope)
}

// renameCollectionInScopes replaces oldName with newName in the scope of every
// instance that lists it. An empty newName removes the entry.
func (app *BaseApp) renameCollectionInScopes(oldName, newName string) error {
	defer app.resetScopeCache()
	rows := []*Instance{}
	if err := app.DB().Select("id", "scopeCollections").From("_instances").All(&rows); err != nil {
		return err
	}
	for _, row := range rows {
		var names []string
		if err := json.Unmarshal([]byte(row.ScopeCollections), &names); err != nil {
			continue
		}
		if !slices.Contains(names, oldName) {
			continue
		}
		next := make([]string, 0, len(names))
		for _, n := range names {
			if n == oldName {
				n = newName
			}
			if n != "" && !slices.Contains(next, n) {
				next = append(next, n)
			}
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if _, err := app.DB().Update("_instances", dbx.Params{"scopeCollections": string(raw)}, dbx.HashExp{"id": row.ID}).Execute(); err != nil {
			return err
		}
	}
	return nil
}

func (app *BaseApp) writeInstanceScope(scope InstanceScope) error {
	// invalidate again after the write: a read that overlapped it must not
	// stay cached (a transaction app also invalidates the parent on completion)
	defer app.resetScopeCache()
	id, err := app.InstanceID()
	if err != nil {
		return err
	}
	if scope.Collections == nil {
		scope.Collections = []string{}
	}
	raw, err := json.Marshal(scope.Collections)
	if err != nil {
		return err
	}
	_, err = app.DB().Update("_instances", dbx.Params{"scopeMode": scope.Mode, "scopeCollections": string(raw)}, dbx.HashExp{"id": id}).Execute()
	return err
}

// SetInstanceInfo sets the display name and public URL of this instance.
func (app *BaseApp) SetInstanceInfo(name, url string) error {
	id, err := app.InstanceID()
	if err != nil {
		return err
	}
	params := dbx.Params{}
	if name != "" {
		params["name"] = name
	}
	if url != "" {
		params["url"] = url
	}
	if len(params) == 0 {
		return nil
	}
	_, err = app.DB().Update("_instances", params, dbx.HashExp{"id": id}).Execute()
	return err
}

// ConfigureReplicaInstance makes this instance a replica of the instance
// fromID: same collection scope, and its own copy of its settings (S3 and
// backups settings stay shared).
func (app *BaseApp) ConfigureReplicaInstance(fromID, name, url string) error {
	id, err := app.InstanceID()
	if err != nil {
		return err
	}
	if fromID == id {
		return errors.New("an instance cannot replicate itself")
	}
	source := Instance{}
	if err := app.DB().Select("*").From("_instances").Where(dbx.HashExp{"id": fromID}).One(&source); err != nil {
		return fmt.Errorf("unknown instance %q", fromID)
	}
	scope := InstanceScope{Mode: source.ScopeMode}
	if err := json.Unmarshal([]byte(source.ScopeCollections), &scope.Collections); err != nil {
		return err
	}
	if err := app.writeInstanceScope(scope); err != nil {
		return err
	}
	if err := app.EnableInstanceSettings(fromID); err != nil {
		return err
	}
	// a replica runs the same migrations, so it shares their history
	if err := app.SetMigrationGroup(app.migrationGroupOf(fromID)); err != nil {
		return err
	}
	if err := app.SetInstanceInfo(name, url); err != nil {
		return err
	}
	return app.setInstanceAppURL(url)
}

// ConfigureCleanInstance makes this instance a new one that sees no
// collections except the system ones, with its own settings.
func (app *BaseApp) ConfigureCleanInstance(name, url string) error {
	if err := app.writeInstanceScope(InstanceScope{Mode: "allow"}); err != nil {
		return err
	}
	if err := app.EnableInstanceSettings(""); err != nil {
		return err
	}
	// a new instance has its own migrations and their history
	if err := app.UseOwnMigrationGroup(); err != nil {
		return err
	}
	if err := app.SetInstanceInfo(name, url); err != nil {
		return err
	}
	return app.setInstanceAppURL(url)
}

// SetInstanceURLSetting sets the app URL of this instance's settings.
func (app *BaseApp) SetInstanceURLSetting(url string) error {
	return app.setInstanceAppURL(url)
}

func (app *BaseApp) setInstanceAppURL(url string) error {
	if url == "" {
		return nil
	}
	s, err := app.Settings().Clone()
	if err != nil {
		return err
	}
	s.Meta.AppURL = url
	return app.SaveNoValidate(s)
}

// The scope of this instance is read on every collection visibility check, so
// it is cached. Writers invalidate the cache after the write (and a
// generation counter keeps a read that overlapped the write from being cached
// afterwards). Transaction apps get their own cache and invalidate the
// parent's when the transaction ends, so a rolled back scope is never kept.
type scopeCache struct {
	mu       sync.RWMutex
	gen      uint64
	scope    *InstanceScope
	at       uint64 // generation the cached scope was loaded in
	loadedAt time.Time
}

// scopeCacheTTL bounds how long another process that changed this instance's
// scope (same instance id, separate cache) can go unnoticed.
const scopeCacheTTL = 5 * time.Second

func (app *BaseApp) cachedScope() (InstanceScope, bool) {
	c := app.scopeCache
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.scope == nil || c.at != c.gen || time.Since(c.loadedAt) > scopeCacheTTL {
		return InstanceScope{}, false
	}
	return *c.scope, true
}

func (c *scopeCache) generation() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.gen
}

func (app *BaseApp) scopeGeneration() uint64 {
	return app.scopeCache.generation()
}

// setCachedScope stores a scope that was loaded while the cache was in gen.
func (app *BaseApp) setCachedScope(scope InstanceScope, gen uint64) {
	c := app.scopeCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		return // a write happened meanwhile
	}
	c.scope, c.at, c.loadedAt = &scope, gen, time.Now()
}

func (app *BaseApp) resetScopeCache() {
	c := app.scopeCache
	c.mu.Lock()
	c.gen++
	c.scope = nil
	c.mu.Unlock()
}
