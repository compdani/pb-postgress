package core

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/dbutils"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"
)

// ErrRestoreFiles is returned (wrapped) when the database part of a restore
// committed but copying the record files failed.
var ErrRestoreFiles = errors.New("copying the files failed")

// StoreKeyLastRestoreReport holds the [RestoreReport] of the last
// successful RestoreBackup call.
const StoreKeyLastRestoreReport = "@lastRestoreReport"

// RestoreOptions controls how a backup is restored.
type RestoreOptions struct {
	// DropCollections drops the non-system collections (and their records)
	// that are not in the backup. When false they are left untouched.
	DropCollections bool
	// ExcludeCollections lists collection names or ids of the backup that
	// are not imported (neither schema nor records). An existing collection
	// with the same id is left untouched, even when DropCollections is true.
	ExcludeCollections []string
	// Resolutions says what to do with backup collections that conflict
	// (same name or id) with a collection in this database, keyed by backup
	// collection id or name.
	Resolutions map[string]CollectionResolution
	// RequireResolution makes an unresolved conflict an error. Otherwise a
	// conflicting non-system collection is replaced (system ones are kept).
	RequireResolution bool
	// Settings: "" restores the backup settings into the current settings,
	// "none" keeps the current ones, "instance" first gives this instance its
	// own settings row and restores into it (S3 and backups stay shared).
	Settings string
	// ScopeToImported sets this instance's scope to the imported collections
	// (plus the ones skipped with Shared) before importing, so the instance
	// sees only those and never touches the other instances' collections.
	// It implies the behavior of DropCollections for collections this
	// instance can see.
	ScopeToImported bool
}

// Collection conflict resolution actions.
const (
	ResolutionSkip    = "skip"    // keep the local collection and its records, import nothing
	ResolutionReplace = "replace" // the backup collection and records replace the local ones
	ResolutionRename  = "rename"  // import the backup collection under NewName
)

// CollectionResolution resolves one conflicting collection.
type CollectionResolution struct {
	Action  string `json:"action"`
	NewName string `json:"newName,omitempty"`
	// Shared, with ResolutionSkip, keeps the local collection visible to this
	// instance when ScopeToImported is set (otherwise it is hidden).
	Shared bool `json:"shared,omitempty"`
}

// DefaultRestoreOptions returns the options used by RestoreBackup.
func DefaultRestoreOptions() RestoreOptions {
	return RestoreOptions{DropCollections: true}
}

// RestoreReport describes the views a PocketBase import changed or left out.
type RestoreReport struct {
	// TranslatedViews were rewritten from SQLite SQL and created.
	// Check that their results are what you expect.
	TranslatedViews []RestoreViewReport `json:"translatedViews"`
	// SkippedViews could not be created and were left out; recreate them
	// from OriginalQuery in the dashboard. Everything else was restored.
	SkippedViews []RestoreViewReport `json:"skippedViews"`
}

type RestoreViewReport struct {
	Name          string `json:"name"`
	OriginalQuery string `json:"originalQuery"`
	Query         string `json:"query,omitempty"`
	Error         string `json:"error,omitempty"`
}

// restoreView is a view collection imported one at a time.
type restoreView struct {
	data       map[string]any
	id, name   string
	original   string
	translated bool
}

// restorePlan is everything read and checked from a backup before the
// database is changed.
type restorePlan struct {
	rawCollections []map[string]any
	// viewFallback imports views separately (PocketBase imports): each
	// view's query is translated from SQLite, and a view the database
	// rejects is skipped instead of failing the restore.
	viewFallback bool
	views        []*restoreView
	collections  []*Collection // record collections (no views), source definitions
	byID         map[string]*Collection
	settings     *Settings // nil when the backup has no settings
	migrations   []backupMigration
	// srcMigrationGroup is the migration group of the instance that made the backup.
	srcMigrationGroup string
	excluded          map[string]bool // local ids of collections left untouched
	// origin maps an imported collection id to its definition in the backup
	// (the original id and name, used to read rows and files).
	origin map[string]*Collection
	// idMap maps backup collection ids to the id they get in this database.
	idMap map[string]string
	// dangling are skipped backup collections without a local counterpart.
	dangling map[string]string
	shared   []*Collection   // skipped local collections the instance keeps seeing
	scope    *InstanceScope  // set with ScopeToImported
	multi    bool            // other instances share this database
	names    map[string]bool // every collection name and id in the backup

	renamed [][2]string // old and new names of renamed collections
}

func (app *BaseApp) restoreFromSource(ctx context.Context, src backupSource, backupName string, opts RestoreOptions) error {
	if app.txInfo != nil {
		return errors.New("a backup cannot be restored inside a transaction")
	}
	if err := app.requireUnscopedForBackup(); err != nil {
		return err
	}

	if opts.DropCollections && !opts.ScopeToImported && app.HasTable("_instances") {
		instances, err := app.Instances()
		if err != nil {
			return err
		}
		if len(instances) > 1 {
			return errors.New("other instances share this database: restoring with collection dropping would delete their collections; disable dropping collections")
		}
	}

	plan, err := app.planRestore(ctx, src, opts)
	if err != nil {
		return fmt.Errorf("%s: %w", src.Kind(), err)
	}

	var previousScope *InstanceScope
	if plan.scope != nil {
		if prev, err := app.InstanceScope(); err == nil {
			previousScope = &prev
		}
		if err := app.writeInstanceScope(*plan.scope); err != nil {
			return fmt.Errorf("setting the instance scope: %w", err)
		}
	}

	if opts.Settings == "instance" && plan.settings != nil {
		if err := app.EnableInstanceSettings(""); err != nil {
			if previousScope != nil {
				_ = app.writeInstanceScope(*previousScope)
			}
			return fmt.Errorf("creating the instance settings: %w", err)
		}
	}

	report := &RestoreReport{TranslatedViews: []RestoreViewReport{}, SkippedViews: []RestoreViewReport{}}

	schema := func(txApp App) error {
		// nothing to import when every collection was skipped
		if len(plan.rawCollections) > 0 {
			if err := txApp.ImportCollections(plan.rawCollections, true); err != nil {
				return fmt.Errorf("importing collections: %w", err)
			}
		}
		if plan.viewFallback {
			*report = app.importViewsWithFallback(txApp, plan.views)
		}
		return nil
	}
	byOrigin := make(map[string]*Collection, len(plan.collections))
	state := restoreFilesState{Backup: backupName, Collections: map[string]string{}}
	for _, c := range plan.collections {
		byOrigin[plan.origin[c.Id].Id] = c
		state.Collections[plan.origin[c.Id].Id] = c.Id
	}
	data := func(txApp App) error {
		// saved with the data, so a committed restore always has its retry state
		if err := app.saveRestoreFilesState(txApp, state); err != nil {
			return fmt.Errorf("saving the restore state: %w", err)
		}
		return app.restoreData(ctx, txApp, src, plan)
	}

	if app.SQLDialect() == DialectPostgres {
		err = app.RunInTransaction(func(txApp App) error {
			if err := schema(txApp); err != nil {
				return err
			}
			return data(txApp)
		})
	} else {
		// MySQL commits DDL implicitly, so only the data step is atomic.
		err = app.RunInTransaction(schema)
		if err == nil {
			err = app.RunInTransaction(data)
		}
	}
	if err != nil {
		// keep a failed restore retryable
		if previousScope != nil {
			_ = app.writeInstanceScope(*previousScope)
		}
		return err
	}

	// Files are copied only after the database changes committed, so a failed
	// restore never overwrites files that are still in use.
	var filesErr error
	if err := app.restoreFiles(ctx, src, byOrigin); err != nil {
		filesErr = fmt.Errorf("%w (the database was restored; retry copying the files): %v", ErrRestoreFiles, err)
	} else if err := app.clearRestoreFilesState(); err != nil {
		filesErr = fmt.Errorf("%w (the files were copied but the restore state could not be cleared; discard it): %v", ErrRestoreFiles, err)
	}

	for _, v := range report.SkippedViews {
		app.Logger().Warn("Skipped a view while importing a PocketBase backup",
			"name", v.Name, "query", v.OriginalQuery, "error", v.Error)
	}
	app.Store().Set(StoreKeyLastRestoreReport, report)

	// the other instances reload their caches, and this one also re-applies
	// the auth token invalidations through the cache sync
	app.bumpCacheVersion(cacheVersionCollections)
	app.bumpCacheVersion(cacheVersionSettings)
	app.requestCacheReload(cacheVersionCollections)
	app.requestCacheReload(cacheVersionSettings)

	// the caches reload even when the files failed: the database did change
	// (held as a collection write so the cache sync never publishes in between)
	releaseWrite := app.beginCollectionWrite()
	defer releaseWrite()
	return errors.Join(app.ReloadCachedCollections(), app.ReloadSettings(), filesErr)
}

func (app *BaseApp) planRestore(ctx context.Context, src backupSource, opts RestoreOptions) (*restorePlan, error) {
	plan := &restorePlan{byID: map[string]*Collection{}, excluded: map[string]bool{},
		origin: map[string]*Collection{}, idMap: map[string]string{}, dangling: map[string]string{}}
	excluded := make(map[string]bool, len(opts.ExcludeCollections))
	for _, n := range opts.ExcludeCollections {
		excluded[n] = true
	}
	plan.names = map[string]bool{}
	if app.HasTable("_instances") {
		instances, err := app.Instances()
		if err != nil {
			return nil, fmt.Errorf("listing the instances: %w", err)
		}
		plan.multi = len(instances) > 1
	}

	raw, err := src.Collections()
	if err != nil {
		return nil, err
	}
	for _, data := range raw {
		for _, k := range []string{"id", "name"} {
			if v, _ := data[k].(string); v != "" {
				plan.names[v] = true
			}
		}
	}
	if len(raw) == 0 {
		return nil, errors.New("the backup has no collections")
	}
	_, plan.viewFallback = src.(*pocketBaseSource)

	for _, data := range raw {
		encoded, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		c := &Collection{}
		if err := json.Unmarshal(encoded, c); err != nil {
			return nil, fmt.Errorf("collection %v: %w", data["name"], err)
		}
		if c.Id == "" || c.Name == "" {
			return nil, fmt.Errorf("collection %v has no id or name", data["name"])
		}
		orig := *c
		skip, err := app.resolveConflict(plan, opts, excluded, data, c)
		if err != nil {
			return nil, err
		}
		if skip {
			continue
		}
		plan.origin[c.Id] = &orig
		if err := app.checkRestorableIndexes(c); err != nil {
			return nil, err
		}
		if c.IsView() {
			if plan.viewFallback {
				view := &restoreView{data: data, id: c.Id, name: c.Name, original: c.ViewQuery}
				translated, changed, err := translateSQLiteView(c.ViewQuery, app.SQLDialect())
				if err == nil && changed {
					data["viewQuery"] = translated
					view.translated = true
				}
				plan.views = append(plan.views, view)
				continue
			}
			plan.rawCollections = append(plan.rawCollections, data)
			continue
		}
		plan.rawCollections = append(plan.rawCollections, data)
		plan.collections = append(plan.collections, c)
		plan.byID[c.Id] = c
	}

	if err := app.remapRelations(plan); err != nil {
		return nil, err
	}

	if opts.ScopeToImported {
		plan.scope, err = app.importedScope(plan)
		if err != nil {
			return nil, err
		}
	}

	if !opts.DropCollections && !opts.ScopeToImported || len(plan.excluded) > 0 {
		if err := app.keepMissingCollections(plan, !opts.DropCollections && !opts.ScopeToImported); err != nil {
			return nil, err
		}
	}

	params, err := src.Params()
	if err != nil {
		return nil, err
	}
	if opts.Settings != "none" {
		// a new instance restores the settings row of the instance that made
		// the backup when that instance had its own; otherwise the shared row
		wanted := paramsKeySettings
		if opts.Settings == "instance" {
			if is, ok := src.(interface{ SourceInstanceID() string }); ok && is.SourceInstanceID() != "" {
				own := paramsKeyInstanceSettingsPrefix + is.SourceInstanceID()
				for _, p := range params {
					if p.ID == own {
						wanted = own
					}
				}
			}
		}
		for _, p := range params {
			if p.ID != wanted {
				continue
			}
			restored := newDefaultSettings()
			if err := restored.loadParam(app, &Param{Value: types.JSONRaw(p.Value)}); err != nil {
				return nil, fmt.Errorf("reading the backup settings (encrypted backups need the same %s key): %w", app.EncryptionEnv(), err)
			}
			plan.settings = restored
		}
	}

	if plan.migrations, err = src.Migrations(); err != nil {
		return nil, err
	}
	if is, ok := src.(interface{ SourceInstanceID() string }); ok && is.SourceInstanceID() != "" {
		for _, p := range params {
			if p.ID == paramsKeyMigrationGroupPrefix+is.SourceInstanceID() {
				plan.srcMigrationGroup = p.Value
			}
		}
	}

	// Decode every record now so a bad value fails before anything changes.
	for _, c := range plan.collections {
		varchar := app.varcharColumns(c)
		err := src.Rows(plan.origin[c.Id], func(row dbx.NullStringMap) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			record, err := newRecordFromNullStringMap(c, row)
			if err != nil {
				return err
			}
			if record.Id == "" {
				return errors.New("a record has no id")
			}
			return checkVarcharValues(record, varchar)
		})
		if err != nil {
			return nil, fmt.Errorf("collection %q: %w", c.Name, err)
		}
	}

	return plan, nil
}

// AnalyzeBackup reads the named backup and reports its collections, which of
// them conflict with collections in this database, and what references them.
// It changes nothing.
func (app *BaseApp) AnalyzeBackup(ctx context.Context, name string) (*BackupAnalysis, error) {
	archivePath, cleanup, err := app.localBackupCopy(ctx, name)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	tempDir, err := app.backupTempDir()
	if err != nil {
		return nil, err
	}
	src, err := openBackupSource(archivePath, tempDir)
	if err != nil {
		return nil, err
	}
	defer src.Close()

	raw, err := src.Collections()
	if err != nil {
		return nil, err
	}

	local, err := app.FindAllCollections()
	if err != nil {
		return nil, err
	}
	localRefs := map[string][]BackupReference{} // local collection id -> referencing local collections
	for _, lc := range local {
		for _, f := range lc.Fields {
			if rel, ok := f.(*RelationField); ok {
				localRefs[rel.CollectionId] = append(localRefs[rel.CollectionId], BackupReference{Collection: lc.Name, Field: rel.Name})
			}
		}
	}

	backupRefs := map[string][]BackupReference{} // backup collection id -> referencing backup collections
	for _, data := range raw {
		cname, _ := data["name"].(string)
		fields, _ := data["fields"].([]any)
		for _, f := range fields {
			field, _ := f.(map[string]any)
			if field != nil && field["type"] == FieldTypeRelation {
				target, _ := field["collectionId"].(string)
				fname, _ := field["name"].(string)
				backupRefs[target] = append(backupRefs[target], BackupReference{Collection: cname, Field: fname})
			}
		}
	}

	result := &BackupAnalysis{Collections: make([]BackupCollectionInfo, 0, len(raw))}
	if app.HasTable("_instances") {
		if instances, err := app.Instances(); err == nil && len(instances) > 1 {
			result.OtherInstances = len(instances) - 1
		}
	}
	for _, data := range raw {
		info := BackupCollectionInfo{}
		info.Id, _ = data["id"].(string)
		info.Name, _ = data["name"].(string)
		info.Type, _ = data["type"].(string)
		info.System, _ = data["system"].(bool)
		info.ReferencedBy = backupRefs[info.Id]

		if lc := app.localCollectionFor(info.Id, info.Name); lc != nil {
			conflict := &BackupConflict{
				LocalId:           lc.Id,
				LocalName:         lc.Name,
				LocalSystem:       lc.System,
				LocalType:         lc.Type,
				SameId:            lc.Id == info.Id,
				SameName:          lc.Name == info.Name,
				LocalReferencedBy: localRefs[lc.Id],
			}
			if !lc.IsView() {
				if n, err := app.CountRecords(lc.Name); err == nil {
					conflict.LocalRecords = n
				}
			}
			info.Conflict = conflict
		}
		result.Collections = append(result.Collections, info)
	}

	params, err := src.Params()
	if err != nil {
		return nil, err
	}
	for _, p := range params {
		if p.ID == paramsKeySettings {
			result.HasSettings = true
		}
	}
	migrations, err := src.Migrations()
	if err != nil {
		return nil, err
	}
	result.Migrations = len(migrations)
	return result, nil
}

// BackupAnalysis is the result of [BaseApp.AnalyzeBackup].
type BackupAnalysis struct {
	Collections []BackupCollectionInfo `json:"collections"`
	HasSettings bool                   `json:"hasSettings"`
	// OtherInstances is the number of other instances sharing this database.
	OtherInstances int `json:"otherInstances"`
	Migrations     int `json:"migrations"`
}

// BackupCollectionInfo describes a collection stored in a backup.
type BackupCollectionInfo struct {
	Id     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	System bool   `json:"system"`
	// Conflict is set when this database has a collection with the same id or name.
	Conflict *BackupConflict `json:"conflict,omitempty"`
	// ReferencedBy lists the relation fields of other backup collections pointing here.
	ReferencedBy []BackupReference `json:"referencedBy"`
}

// BackupConflict describes the collection of this database a backup collection conflicts with.
type BackupConflict struct {
	LocalId      string `json:"localId"`
	LocalName    string `json:"localName"`
	LocalType    string `json:"localType"`
	LocalSystem  bool   `json:"localSystem"`
	SameId       bool   `json:"sameId"`
	SameName     bool   `json:"sameName"`
	LocalRecords int64  `json:"localRecords"`
	// LocalReferencedBy lists the relation fields of local collections pointing at it.
	LocalReferencedBy []BackupReference `json:"localReferencedBy"`
}

// BackupReference is a relation field of a collection.
type BackupReference struct {
	Collection string `json:"collection"`
	Field      string `json:"field"`
}

// keepMissingCollections adds the current definition of every visible
// non-system collection that is not in the backup to the import, so the
// import does not drop it. Their records are not touched.
func (app *BaseApp) keepMissingCollections(plan *restorePlan, all bool) error {
	inBackup := make(map[string]bool, len(plan.rawCollections)+len(plan.views))
	for _, data := range plan.rawCollections {
		id, _ := data["id"].(string)
		inBackup[id] = true
	}
	for _, v := range plan.views {
		inBackup[v.id] = true
	}

	existing, err := app.FindAllCollections()
	if err != nil {
		return err
	}
	for _, c := range existing {
		if c.System || inBackup[c.Id] || !app.CollectionVisible(c) {
			continue
		}
		if !all && !plan.excluded[c.Id] {
			continue
		}
		// collections the planned scope hides are protected from deletion
		// by the importer and cannot be imported
		if plan.scope != nil && !slices.Contains(plan.scope.Collections, c.Name) {
			continue
		}
		encoded, err := json.Marshal(c)
		if err != nil {
			return err
		}
		data := map[string]any{}
		if err := json.Unmarshal(encoded, &data); err != nil {
			return err
		}
		plan.rawCollections = append(plan.rawCollections, data)
	}
	return nil
}

// checkRestorableIndexes rejects indexes this database cannot create, so
// MySQL fails before its non-transactional schema changes start.
func (app *BaseApp) checkRestorableIndexes(c *Collection) error {
	if app.SQLDialect() != DialectMySQL {
		return nil
	}
	for _, raw := range c.Indexes {
		idx := dbutils.ParseIndex(raw)
		if !idx.IsValid() {
			continue // reported by the collection validator
		}
		if _, err := indexCreateSQL(app, idx); err != nil {
			return fmt.Errorf("collection %q index %q: %w", c.Name, idx.IndexName, err)
		}
	}
	return nil
}

// varcharColumns lists the fields stored as VARCHAR(255) on MySQL.
func (app *BaseApp) varcharColumns(c *Collection) []string {
	if app.SQLDialect() != DialectMySQL {
		return nil
	}
	names := []string{}
	for _, f := range c.Fields {
		if f.GetName() != FieldNameId && strings.Contains(f.ColumnType(app), "VARCHAR(255)") {
			names = append(names, f.GetName())
		}
	}
	return names
}

func checkVarcharValues(record *Record, fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	values, err := record.dbExport()
	if err != nil {
		return err
	}
	for _, name := range fields {
		s, err := driverString(values[name])
		if err != nil {
			return err
		}
		if n := len([]rune(s)); n > mysqlVarcharMax {
			return fmt.Errorf("record %q field %q has %d characters; MySQL stores this field as VARCHAR(%d) (set a larger max on the field before restoring)", record.Id, name, n, mysqlVarcharMax)
		}
	}
	return nil
}

func driverString(v any) (string, error) {
	if valuer, ok := v.(driver.Valuer); ok {
		dv, err := valuer.Value()
		if err != nil {
			return "", err
		}
		v = dv
	}
	switch s := v.(type) {
	case string:
		return s, nil
	case []byte:
		return string(s), nil
	default:
		return "", nil
	}
}

// restoreFiles copies the files of the collections in byOrigin (backup
// collection id -> destination collection) into the file storage.
func (app *BaseApp) restoreFiles(ctx context.Context, src backupSource, byOrigin map[string]*Collection) error {
	systems := map[string]*filesystem.System{}
	defer func() {
		for _, fsys := range systems {
			fsys.Close()
		}
	}()

	return src.Files(func(key string, r io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		collectionID, rest, _ := strings.Cut(key, "/")
		c := byOrigin[collectionID]
		if c == nil {
			return nil // files of a collection that is not imported
		}
		key = c.Id + "/" + rest
		fsys := systems[c.Id]
		if fsys == nil {
			var err error
			if fsys, err = app.NewFilesystemForCollection(c); err != nil {
				return err
			}
			fsys.SetContext(ctx)
			systems[c.Id] = fsys
		}
		w, err := fsys.NewWriter(key, nil)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, r); err != nil {
			w.Close()
			return fmt.Errorf("%s: %w", key, err)
		}
		return w.Close()
	})
}

func (app *BaseApp) restoreData(ctx context.Context, txApp App, src backupSource, plan *restorePlan) error {
	for _, source := range plan.collections {
		target, err := txApp.FindCollectionByNameOrId(source.Id)
		if err != nil {
			return fmt.Errorf("collection %q was not imported: %w", source.Name, err)
		}

		if _, err := txApp.DB().NewQuery("DELETE FROM {{" + target.Name + "}}").Execute(); err != nil {
			return fmt.Errorf("clearing %q: %w", target.Name, err)
		}

		ins := newRecordInserter(txApp, target)
		err = src.Rows(plan.origin[source.Id], func(row dbx.NullStringMap) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			record, err := newRecordFromNullStringMap(target, row)
			if err != nil {
				return err
			}
			return ins.add(record)
		})
		if err == nil {
			err = ins.flush()
		}
		if err != nil {
			return fmt.Errorf("restoring records of %q: %w", target.Name, err)
		}
	}

	if plan.settings != nil {
		current, err := app.Settings().Clone()
		if err != nil {
			return err
		}
		// Storage locations belong to this deployment, not to the backup.
		plan.settings.setKey(current.key) // an instance with its own settings row keeps using it
		plan.settings.loadedS3, plan.settings.loadedBackups, plan.settings.loadedInit = current.loadedS3, current.loadedBackups, current.loadedInit
		plan.settings.S3 = current.S3
		plan.settings.Backups = current.Backups
		plan.settings.MarkAsNotNew()
		if err := txApp.SaveNoValidate(plan.settings); err != nil {
			return fmt.Errorf("restoring settings: %w", err)
		}
	}

	applied := []string{}
	if err := txApp.DB().Select("file").From(DefaultMigrationsTable).Column(&applied); err != nil {
		return err
	}
	known := make(map[string]bool, len(applied))
	for _, f := range applied {
		known[f] = true
	}
	// The history of the instance that made the backup becomes the history
	// of this instance's migration group; other instances' rows are left out.
	group := app.MigrationGroup()
	for _, m := range plan.migrations {
		file := m.File
		if prefix, name, ok := strings.Cut(file, "/"); ok {
			if plan.srcMigrationGroup == "" || prefix != plan.srcMigrationGroup {
				continue // another instance's history
			}
			file = name
		} else if plan.srcMigrationGroup != "" && !isSystemMigration(file) {
			continue // the shared group's history, not the source instance's
		}
		key := migrationKey(group, file)
		if known[key] {
			continue
		}
		if _, err := txApp.DB().Insert(DefaultMigrationsTable, dbx.Params{"file": key, "applied": m.Applied}).Execute(); err != nil {
			return fmt.Errorf("restoring migration %q: %w", m.File, err)
		}
		known[key] = true
	}

	return nil
}

// recordInserter writes records with multi-row INSERTs, bypassing record
// hooks and file handling: restored rows are stored exactly as backed up.
type recordInserter struct {
	app     App
	table   string
	columns []string
	rows    [][]any
	perStmt int
}

func newRecordInserter(app App, c *Collection) *recordInserter {
	columns := make([]string, 0, len(c.Fields))
	for _, f := range c.Fields {
		columns = append(columns, f.GetName())
	}
	perStmt := 20000 / max(1, len(columns)) // well under the 65535 placeholder limit
	return &recordInserter{app: app, table: c.Name, columns: columns, perStmt: max(1, min(500, perStmt))}
}

func (ins *recordInserter) add(record *Record) error {
	values, err := record.dbExport()
	if err != nil {
		return err
	}
	row := make([]any, len(ins.columns))
	for i, col := range ins.columns {
		row[i] = values[col]
	}
	ins.rows = append(ins.rows, row)
	if len(ins.rows) >= ins.perStmt {
		return ins.flush()
	}
	return nil
}

func (ins *recordInserter) flush() error {
	if len(ins.rows) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("INSERT INTO {{")
	sb.WriteString(ins.table)
	sb.WriteString("}} (")
	for i, col := range ins.columns {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("[[" + col + "]]")
	}
	sb.WriteString(") VALUES ")

	params := make(dbx.Params, len(ins.rows)*len(ins.columns))
	for r, row := range ins.rows {
		if r > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("(")
		for c, v := range row {
			if c > 0 {
				sb.WriteString(", ")
			}
			name := "r" + strconv.Itoa(r) + "c" + strconv.Itoa(c)
			sb.WriteString("{:" + name + "}")
			params[name] = v
		}
		sb.WriteString(")")
	}

	ins.rows = ins.rows[:0]

	_, err := ins.app.DB().NewQuery(sb.String()).Bind(params).Execute()
	return err
}

// importViewsWithFallback creates the views one at a time, in passes so a
// view can follow the views it selects from. A view the database still
// rejects after every pass is skipped and reported.
func (app *BaseApp) importViewsWithFallback(txApp App, views []*restoreView) RestoreReport {
	report := RestoreReport{TranslatedViews: []RestoreViewReport{}, SkippedViews: []RestoreViewReport{}}
	lastErr := map[string]error{}

	pending := views
	for len(pending) > 0 {
		var next []*restoreView
		for _, v := range pending {
			if err := app.tryImportView(txApp, v); err != nil {
				lastErr[v.id] = err
				next = append(next, v)
				continue
			}
			if v.translated {
				report.TranslatedViews = append(report.TranslatedViews, RestoreViewReport{
					Name: v.name, OriginalQuery: v.original, Query: fmt.Sprint(v.data["viewQuery"]),
				})
			}
		}
		if len(next) == len(pending) {
			break // no progress
		}
		pending = next
	}

	for _, v := range pending {
		item := RestoreViewReport{Name: v.name, OriginalQuery: v.original, Error: lastErr[v.id].Error()}
		if v.translated {
			item.Query = fmt.Sprint(v.data["viewQuery"])
		}
		report.SkippedViews = append(report.SkippedViews, item)
	}

	return report
}

// tryImportView imports one view and undoes a failed attempt: PostgreSQL
// rolls back to a savepoint; MySQL (implicit DDL commits) removes the view
// and its collection row if they were created.
func (app *BaseApp) tryImportView(txApp App, v *restoreView) (err error) {
	// ImportCollections may modify the map it is given.
	encoded, err := json.Marshal(v.data)
	if err != nil {
		return err
	}
	data := map[string]any{}
	if err := json.Unmarshal(encoded, &data); err != nil {
		return err
	}

	if app.SQLDialect() == DialectPostgres {
		savepoint := "pb_restore_view"
		if _, err := txApp.DB().NewQuery("SAVEPOINT " + savepoint).Execute(); err != nil {
			return err
		}
		defer func() {
			if err != nil {
				if _, rbErr := txApp.DB().NewQuery("ROLLBACK TO SAVEPOINT " + savepoint).Execute(); rbErr != nil {
					err = errors.Join(err, rbErr)
				}
			}
			if _, relErr := txApp.DB().NewQuery("RELEASE SAVEPOINT " + savepoint).Execute(); relErr != nil {
				err = errors.Join(err, relErr)
			}
		}()
		return txApp.ImportCollections([]map[string]any{data}, false)
	}

	if err = txApp.ImportCollections([]map[string]any{data}, false); err != nil {
		_, delErr := txApp.DB().Delete((&Collection{}).TableName(), dbx.HashExp{"id": v.id}).Execute()
		return errors.Join(err, delErr, txApp.DeleteView(v.name))
	}
	return nil
}

// localCollectionFor returns the collection of this database a backup
// collection conflicts with (same id, else same name), or nil.
func (app *BaseApp) localCollectionFor(id, name string) *Collection {
	if c, err := app.FindCollectionByNameOrId(id); err == nil {
		return c
	}
	if c, err := app.FindCollectionByNameOrId(name); err == nil {
		return c
	}
	return nil
}

// resolveConflict applies the resolution of a backup collection to the plan.
// It reports skip when the collection is not imported. The collection
// definition (c and data) is rewritten for "replace" (it adopts the local
// id) and "rename" (new name and id).
func (app *BaseApp) resolveConflict(plan *restorePlan, opts RestoreOptions, excluded map[string]bool, data map[string]any, c *Collection) (skip bool, err error) {
	local := app.localCollectionFor(c.Id, c.Name)

	res, hasRes := opts.Resolutions[c.Id]
	if !hasRes {
		res, hasRes = opts.Resolutions[c.Name]
	}
	if excluded[c.Id] || excluded[c.Name] {
		res, hasRes = CollectionResolution{Action: ResolutionSkip}, true
	}
	if hasRes && res.Action == "" {
		if opts.RequireResolution {
			return false, fmt.Errorf("collection %q needs an action: skip, replace or rename", c.Name)
		}
		res.Action = ResolutionReplace
	}
	if local != nil && !hasRes {
		switch {
		case c.System && plan.multi:
			// superusers and other system data are shared by every instance
			res = CollectionResolution{Action: ResolutionSkip}
		case plan.multi && !opts.RequireResolution:
			// never replace collections other instances may use unless asked to
			res = CollectionResolution{Action: ResolutionSkip}
		case opts.RequireResolution && !c.System:
			return false, fmt.Errorf("collection %q conflicts with the existing collection %q: choose to skip, replace or rename it", c.Name, local.Name)
		default:
			res = CollectionResolution{Action: ResolutionReplace}
		}
		hasRes = true
	}
	if !hasRes {
		return false, nil // no conflict
	}

	switch res.Action {
	case "", ResolutionReplace:
		if c.System && plan.multi && res.Action == ResolutionReplace {
			return false, fmt.Errorf("system collection %q cannot be replaced; skip it", c.Name)
		}
		if local != nil && local.Id != c.Id {
			plan.idMap[c.Id] = local.Id
			c.Id = local.Id
			data["id"] = local.Id
		}
	case ResolutionSkip:
		if local != nil {
			plan.idMap[c.Id] = local.Id
			plan.excluded[local.Id] = true
			if res.Shared && !local.System {
				plan.shared = append(plan.shared, local)
			}
		} else {
			plan.dangling[c.Id] = c.Name
		}
		return true, nil
	case ResolutionRename:
		if local == nil {
			// nothing to rename around, import as is
			return false, nil
		}
		if c.System {
			return false, fmt.Errorf("system collection %q cannot be renamed", c.Name)
		}
		newName := strings.TrimSpace(res.NewName)
		if newName == "" {
			return false, fmt.Errorf("collection %q needs a new name", c.Name)
		}
		if _, err := app.FindCollectionByNameOrId(newName); err == nil || plan.names[newName] {
			return false, fmt.Errorf("cannot rename %q to %q: a collection with that name exists", c.Name, newName)
		}
		for _, other := range plan.rawCollections {
			if other["name"] == newName {
				return false, fmt.Errorf("cannot rename %q to %q: another imported collection has that name", c.Name, newName)
			}
		}
		oldName, oldID := c.Name, c.Id
		c.Name = newName
		c.Id = c.idChecksum()
		if _, err := app.FindCollectionByNameOrId(c.Id); err == nil || plan.names[c.Id] {
			c.Id = "pbc_" + security.RandomStringWithAlphabet(10, "0123456789")
		}
		plan.names[newName], plan.names[c.Id] = true, true
		data["name"], data["id"] = c.Name, c.Id
		renameIndexes(c, data, newName)
		plan.idMap[oldID] = c.Id
		plan.excluded[local.Id] = true // the existing collection is kept
		plan.renamed = append(plan.renamed, [2]string{oldName, newName})
	default:
		return false, fmt.Errorf("collection %q: unknown resolution %q", c.Name, res.Action)
	}
	return false, nil
}

// remapRelations points the relation fields of the imported collections to
// the ids the collections get in this database, and rejects relations to a
// skipped collection that does not exist here. Views that use the old name
// of a renamed collection are rejected too.
func (app *BaseApp) remapRelations(plan *restorePlan) error {
	all := append([]map[string]any{}, plan.rawCollections...)
	for _, v := range plan.views {
		all = append(all, v.data)
	}
	for _, data := range all {
		name, _ := data["name"].(string)
		fields, _ := data["fields"].([]any)
		for _, f := range fields {
			field, _ := f.(map[string]any)
			if field == nil || field["type"] != FieldTypeRelation {
				continue
			}
			target, _ := field["collectionId"].(string)
			if mapped, ok := plan.idMap[target]; ok {
				field["collectionId"] = mapped
			} else if dangling, ok := plan.dangling[target]; ok {
				return fmt.Errorf("collection %q field %q references %q, which is not imported and does not exist here: import it, or skip %q as well", name, field["name"], dangling, name)
			}
		}
		if q, _ := data["viewQuery"].(string); q != "" {
			if err := checkViewRenames(name, q, plan.renamed); err != nil {
				return err
			}
		}
	}
	for _, v := range plan.views {
		if err := checkViewRenames(v.name, v.original, plan.renamed); err != nil {
			return err
		}
	}
	return nil
}

func checkViewRenames(view, query string, renamed [][2]string) error {
	for _, r := range renamed {
		if viewMentions(query, r[0]) {
			return fmt.Errorf("view %q uses the collection %q, which is renamed to %q: skip the view or choose another resolution", view, r[0], r[1])
		}
	}
	return nil
}

// importedScope builds the allow scope of an instance that only sees the
// collections of the plan, and checks that no visible collection has a
// required relation to a collection the instance cannot see.
func (app *BaseApp) importedScope(plan *restorePlan) (*InstanceScope, error) {
	scope := &InstanceScope{Mode: "allow", Collections: []string{}}
	visible := map[string]bool{}
	all := append([]map[string]any{}, plan.rawCollections...)
	for _, v := range plan.views {
		all = append(all, v.data)
	}
	for _, data := range all {
		if sys, _ := data["system"].(bool); sys {
			continue
		}
		name, _ := data["name"].(string)
		id, _ := data["id"].(string)
		scope.Collections = append(scope.Collections, name)
		visible[id] = true
	}
	for _, c := range plan.shared {
		scope.Collections = append(scope.Collections, c.Name)
		visible[c.Id] = true
	}

	locals, err := app.FindAllCollections()
	if err != nil {
		return nil, err
	}
	type viewQuery struct{ name, query string }
	queries := []viewQuery{}
	for _, data := range all {
		name, _ := data["name"].(string)
		q, _ := data["viewQuery"].(string)
		queries = append(queries, viewQuery{name, q})
	}
	for _, sc := range plan.shared {
		if sc.IsView() {
			queries = append(queries, viewQuery{sc.Name, sc.ViewQuery})
		}
	}
	for _, vq := range queries {
		q := vq.query
		if q == "" {
			continue
		}
		for _, lc := range locals {
			if lc.System || slices.Contains(scope.Collections, lc.Name) {
				continue
			}
			if viewMentions(q, lc.Name) {
				return nil, fmt.Errorf("view %q reads the collection %q, which this instance would not see: import or share %q, or skip the view", vq.name, lc.Name, lc.Name)
			}
		}
	}

	for _, data := range plan.rawCollections {
		name, _ := data["name"].(string)
		fields, _ := data["fields"].([]any)
		for _, f := range fields {
			field, _ := f.(map[string]any)
			if field == nil || field["type"] != FieldTypeRelation {
				continue
			}
			if required, _ := field["required"].(bool); !required {
				continue
			}
			target, _ := field["collectionId"].(string)
			if visible[target] {
				continue
			}
			if lc, err := app.FindCollectionByNameOrId(target); err == nil && lc.System {
				continue
			}
			isSystem := false
			for _, other := range plan.rawCollections {
				if other["id"] == target {
					isSystem, _ = other["system"].(bool)
				}
			}
			if !isSystem {
				return nil, fmt.Errorf("collection %q field %q has a required relation to a collection this instance would not see: import it or mark it shared", name, field["name"])
			}
		}
	}
	return scope, nil
}

// renameIndexes points the indexes of a renamed collection at its new table
// and gives them new names (index names are unique per schema).
func renameIndexes(c *Collection, data map[string]any, newName string) {
	suffix := "_" + crc32Checksum(newName)
	rebuilt := make([]string, 0, len(c.Indexes))
	for _, raw := range c.Indexes {
		idx := dbutils.ParseIndex(raw)
		if !idx.IsValid() {
			rebuilt = append(rebuilt, raw)
			continue
		}
		idx.TableName = newName
		idx.IndexName += suffix
		rebuilt = append(rebuilt, idx.Build())
	}
	c.Indexes = rebuilt
	data["indexes"] = rebuilt
}

// viewMentions reports whether a view query reads the collection name as a
// table: the tables found by the view parser (comma lists, schema-qualified
// names) plus any name right after FROM or JOIN (subqueries). A column or
// literal with the same name does not count.
func viewMentions(query, name string) bool {
	p := new(identifiersParser)
	if err := p.parse(query); err == nil {
		for _, t := range p.tables {
			ident := strings.Trim(t.original, "`\"[]")
			if i := strings.LastIndex(ident, "."); i >= 0 {
				ident = strings.Trim(ident[i+1:], "`\"[]")
			}
			if strings.EqualFold(ident, name) {
				return true
			}
		}
	}
	re, err := regexp.Compile("(?i)\\b(from|join)\\s+(?:[`\"\\[]?\\w+[`\"\\]]?\\.)?[`\"\\[]?" + regexp.QuoteMeta(name) + "[`\"\\]]?($|[^A-Za-z0-9_])")
	return err == nil && re.MatchString(query)
}

// restoreFilesState is what a files-only retry needs after the database part
// of a restore committed but copying the files failed.
type restoreFilesState struct {
	Backup string `json:"backup"`
	// Collections maps backup collection ids to their ids in this database.
	Collections map[string]string `json:"collections"`
}

func (app *BaseApp) restoreFilesKey() (string, error) {
	id, err := app.InstanceID()
	if err != nil {
		return "", err
	}
	return "restoreFiles:" + id, nil
}

func (app *BaseApp) saveRestoreFilesState(txApp App, state restoreFilesState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	key, err := app.restoreFilesKey()
	if err != nil {
		return err
	}
	now := types.NowDateTime().String()
	if _, err := txApp.DB().Delete(paramsTable, dbx.HashExp{"id": key}).Execute(); err != nil {
		return err
	}
	_, err = txApp.DB().Insert(paramsTable, dbx.Params{"id": key, "value": string(raw), "created": now, "updated": now}).Execute()
	return err
}

func (app *BaseApp) clearRestoreFilesState() error {
	key, err := app.restoreFilesKey()
	if err != nil {
		return err
	}
	_, err = app.DB().Delete(paramsTable, dbx.HashExp{"id": key}).Execute()
	return err
}

func (app *BaseApp) loadRestoreFilesState() (*restoreFilesState, error) {
	key, err := app.restoreFilesKey()
	if err != nil {
		return nil, err
	}
	var value string
	err = app.DB().Select("value").From(paramsTable).Where(dbx.HashExp{"id": key}).Row(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	state := &restoreFilesState{}
	if err := json.Unmarshal([]byte(value), state); err != nil {
		return nil, err
	}
	return state, nil
}

// HasPendingRestoreFiles reports whether a restore committed its database
// changes but did not finish copying the record files.
//
// A state that cannot be read counts as pending, so a restore is never
// allowed on top of an unknown state.
func (app *BaseApp) HasPendingRestoreFiles() bool {
	state, err := app.loadRestoreFilesState()
	return err != nil || state != nil
}

// PendingRestoreFilesBackup returns the backup whose files are still to be copied.
func (app *BaseApp) PendingRestoreFilesBackup() (string, bool, error) {
	state, err := app.loadRestoreFilesState()
	if err != nil || state == nil {
		return "", false, err
	}
	return state.Backup, true, nil
}

// CopyPendingRestoreFiles copies the files of a restore whose database part
// committed, using the saved collection mapping, and keeps the saved state.
// It never changes records.
func (app *BaseApp) CopyPendingRestoreFiles(ctx context.Context) error {
	return app.copyPendingRestoreFiles(ctx, false)
}

func (app *BaseApp) copyPendingRestoreFiles(ctx context.Context, clear bool) error {
	release, err := app.acquireBackupOp("restore-files")
	if err != nil {
		return err
	}
	defer release()

	state, err := app.loadRestoreFilesState()
	if err != nil {
		return err
	}
	if state == nil {
		return errors.New("there are no restored files to copy")
	}

	archivePath, cleanup, err := app.localBackupCopy(ctx, state.Backup)
	if err != nil {
		return err
	}
	defer cleanup()
	tempDir, err := app.backupTempDir()
	if err != nil {
		return err
	}
	src, err := openBackupSource(archivePath, tempDir)
	if err != nil {
		return err
	}
	defer src.Close()

	byOrigin := make(map[string]*Collection, len(state.Collections))
	for origin, dest := range state.Collections {
		c, err := app.FindCollectionByNameOrId(dest)
		if err != nil {
			return fmt.Errorf("collection %s no longer exists: %w", dest, err)
		}
		byOrigin[origin] = c
	}
	if err := app.restoreFiles(ctx, src, byOrigin); err != nil {
		return fmt.Errorf("%w: %v", ErrRestoreFiles, err)
	}
	if clear {
		// still holding the operation, so no restore can write a new state meanwhile
		return app.clearRestoreFilesState()
	}
	return nil
}

// RetryRestoreFiles copies the pending files and then forgets the saved state.
func (app *BaseApp) RetryRestoreFiles(ctx context.Context) error {
	return app.copyPendingRestoreFiles(ctx, true)
}

// DiscardRestoreFiles forgets a pending files-only retry.
func (app *BaseApp) DiscardRestoreFiles() error {
	release, err := app.acquireBackupOp("restore-files")
	if err != nil {
		return err
	}
	defer release()
	return app.clearRestoreFilesState()
}
