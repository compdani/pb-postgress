package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	return err
}
func (app *BaseApp) Instances() ([]*Instance, error) {
	rows := []*Instance{}
	err := app.DB().Select("*").From("_instances").OrderBy("created_at").All(&rows)
	return rows, err
}
func (app *BaseApp) InstanceScope() (InstanceScope, error) {
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
	if c.System || c.IsAuth() {
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
	if c.System || c.IsAuth() {
		return true
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
	for _, name := range scope.Collections {
		c := byName[name]
		if c == nil {
			return fmt.Errorf("unknown collection %q", name)
		}
		if scope.Mode == "deny" && (c.System || c.IsAuth()) {
			return fmt.Errorf("system and auth collections cannot be hidden: %s", name)
		}
	}
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
	id, err := app.InstanceID()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(scope.Collections)
	if err != nil {
		return err
	}
	_, err = app.DB().Update("_instances", dbx.Params{"scopeMode": scope.Mode, "scopeCollections": string(raw)}, dbx.HashExp{"id": id}).Execute()
	return err
}
