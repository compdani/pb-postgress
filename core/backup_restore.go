package core

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/dbutils"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/pocketbase/pocketbase/tools/types"
)

// StoreKeyLastRestoreReport holds the [RestoreReport] of the last
// successful RestoreBackup call.
const StoreKeyLastRestoreReport = "@lastRestoreReport"

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
}

func (app *BaseApp) restoreFromSource(ctx context.Context, src backupSource) error {
	if app.txInfo != nil {
		return errors.New("a backup cannot be restored inside a transaction")
	}
	if err := app.requireUnscopedForBackup(); err != nil {
		return err
	}

	plan, err := app.planRestore(ctx, src)
	if err != nil {
		return fmt.Errorf("%s: %w", src.Kind(), err)
	}

	// Files first: an interrupted restore then leaves extra files rather
	// than records without their files.
	if err := app.restoreFiles(ctx, src, plan); err != nil {
		return fmt.Errorf("restoring files: %w", err)
	}

	report := &RestoreReport{TranslatedViews: []RestoreViewReport{}, SkippedViews: []RestoreViewReport{}}

	schema := func(txApp App) error {
		if err := txApp.ImportCollections(plan.rawCollections, true); err != nil {
			return fmt.Errorf("importing collections: %w", err)
		}
		if plan.viewFallback {
			*report = app.importViewsWithFallback(txApp, plan.views)
		}
		return nil
	}
	data := func(txApp App) error {
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
		return err
	}

	for _, v := range report.SkippedViews {
		app.Logger().Warn("Skipped a view while importing a PocketBase backup",
			"name", v.Name, "query", v.OriginalQuery, "error", v.Error)
	}
	app.Store().Set(StoreKeyLastRestoreReport, report)

	return errors.Join(app.ReloadCachedCollections(), app.ReloadSettings())
}

func (app *BaseApp) planRestore(ctx context.Context, src backupSource) (*restorePlan, error) {
	plan := &restorePlan{byID: map[string]*Collection{}}

	raw, err := src.Collections()
	if err != nil {
		return nil, err
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

	params, err := src.Params()
	if err != nil {
		return nil, err
	}
	for _, p := range params {
		if p.ID != paramsKeySettings {
			continue
		}
		restored := newDefaultSettings()
		if err := restored.loadParam(app, &Param{Value: types.JSONRaw(p.Value)}); err != nil {
			return nil, fmt.Errorf("reading the backup settings (encrypted backups need the same %s key): %w", app.EncryptionEnv(), err)
		}
		plan.settings = restored
	}

	if plan.migrations, err = src.Migrations(); err != nil {
		return nil, err
	}

	// Decode every record now so a bad value fails before anything changes.
	for _, c := range plan.collections {
		varchar := app.varcharColumns(c)
		err := src.Rows(c, func(row dbx.NullStringMap) error {
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

func (app *BaseApp) restoreFiles(ctx context.Context, src backupSource, plan *restorePlan) error {
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
		collectionID, _, _ := strings.Cut(key, "/")
		c := plan.byID[collectionID]
		if c == nil {
			return nil // files of a collection that is not in the backup
		}
		fsys := systems[collectionID]
		if fsys == nil {
			var err error
			if fsys, err = app.NewFilesystemForCollection(c); err != nil {
				return err
			}
			fsys.SetContext(ctx)
			systems[collectionID] = fsys
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
		err = src.Rows(source, func(row dbx.NullStringMap) error {
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
	for _, m := range plan.migrations {
		if known[m.File] {
			continue
		}
		if _, err := txApp.DB().Insert(DefaultMigrationsTable, dbx.Params{"file": m.File, "applied": m.Applied}).Execute(); err != nil {
			return fmt.Errorf("restoring migration %q: %w", m.File, err)
		}
		known[m.File] = true
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
