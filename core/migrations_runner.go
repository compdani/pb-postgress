package core

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/osutils"
	"github.com/spf13/cast"
)

var AppMigrations MigrationsList
var SystemMigrations MigrationsList

const DefaultMigrationsTable = "_migrations"

// MigrationsRunner defines a simple struct for managing the execution of db migrations.
type MigrationsRunner struct {
	app            App
	tableName      string
	migrationsList MigrationsList
	inited         bool
	// group scopes the applied state of app migrations to the instances that
	// share it (see [BaseApp.MigrationGroup]); system migrations are global.
	group string
}

// NewMigrationsRunner creates and initializes a new db migrations MigrationsRunner instance.
func NewMigrationsRunner(app App, migrationsList MigrationsList) *MigrationsRunner {
	r := &MigrationsRunner{
		app:            app,
		migrationsList: migrationsList,
		tableName:      DefaultMigrationsTable,
	}
	if ba := asBaseApp(app); ba != nil {
		r.group = ba.MigrationGroup()
	}
	return r
}

// key returns the stored name of a migration file: app migrations of an
// instance with its own migration group are stored as "<group>/<file>".
func (r *MigrationsRunner) key(file string) string {
	return migrationKey(r.group, file)
}

func migrationKey(group, file string) string {
	if group == "" || isSystemMigration(file) {
		return file
	}
	return group + "/" + file
}

func isSystemMigration(file string) bool {
	for _, m := range SystemMigrations.Items() {
		if m.File == file {
			return true
		}
	}
	return false
}

// MigrationKey returns the name under which the migration file is stored in
// the migrations table for the instance of app.
func MigrationKey(app App, file string) string {
	if ba := asBaseApp(app); ba != nil {
		return migrationKey(ba.MigrationGroup(), file)
	}
	return file
}

// Run interactively executes the current runner with the provided args.
//
// The following commands are supported:
// - up           - applies all migrations
// - down [n]     - reverts the last n (default 1) applied migrations
// - history-sync - syncs the migrations table with the runner's migrations list
func (r *MigrationsRunner) Run(args ...string) error {
	if err := r.initMigrationsTable(); err != nil {
		return err
	}

	cmd := "up"
	if len(args) > 0 {
		cmd = args[0]
	}

	switch cmd {
	case "up":
		applied, err := r.Up()
		if err != nil {
			return err
		}

		if len(applied) == 0 {
			color.Green("No new migrations to apply.")
		} else {
			for _, file := range applied {
				color.Green("Applied %s", file)
			}
		}

		return nil
	case "down":
		toRevertCount := 1
		if len(args) > 1 {
			toRevertCount = cast.ToInt(args[1])
			if toRevertCount < 0 {
				// revert all applied migrations
				toRevertCount = len(r.migrationsList.Items())
			}
		}

		names, err := r.lastAppliedMigrations(toRevertCount)
		if err != nil {
			return err
		}

		confirm := osutils.YesNoPrompt(fmt.Sprintf(
			"\n%v\nDo you really want to revert the last %d applied migration(s)?",
			strings.Join(names, "\n"),
			toRevertCount,
		), false)
		if !confirm {
			fmt.Println("The command has been cancelled")
			return nil
		}

		reverted, err := r.Down(toRevertCount)
		if err != nil {
			return err
		}

		if len(reverted) == 0 {
			color.Green("No migrations to revert.")
		} else {
			for _, file := range reverted {
				color.Green("Reverted %s", file)
			}
		}

		return nil
	case "history-sync":
		if err := r.RemoveMissingAppliedMigrations(); err != nil {
			return err
		}

		color.Green("The %s table was synced with the available migrations.", r.tableName)
		return nil
	default:
		return fmt.Errorf("unsupported command: %q", cmd)
	}
}

// Up executes all unapplied migrations for the provided runner.
//
// On success returns list with the applied migrations file names.
func (r *MigrationsRunner) Up() ([]string, error) {
	return r.upWith(false, nil)
}

var errMigrationsDryRun = errors.New("migrations dry run")

// upWith is Up with two extras: dryRun rolls everything back at the end, and
// each (when set) wraps the execution of every migration (run applies it).
func (r *MigrationsRunner) upWith(dryRun bool, each func(txApp App, m *Migration, run func(App) error) error) ([]string, error) {
	if err := r.initMigrationsTable(); err != nil {
		return nil, err
	}

	applied := []string{}

	err := r.app.AuxRunInTransaction(func(txApp App) error {
		return txApp.RunInTransaction(func(txApp App) error {
			for _, m := range r.migrationsList.Items() {
				// applied migrations check
				if r.isMigrationApplied(txApp, m.File) {
					if m.ReapplyCondition == nil {
						continue // no need to reapply
					}

					shouldReapply, err := m.ReapplyCondition(txApp, r, m.File)
					if err != nil {
						return err
					}
					if !shouldReapply {
						continue
					}

					// clear previous history stored entry
					// (it will be recreated after successful execution)
					r.saveRevertedMigration(txApp, m.File)
				}

				run := func(a App) error {
					// ignore empty Up action
					if m.Up != nil {
						if err := m.Up(a); err != nil {
							return fmt.Errorf("failed to apply migration %s: %w", m.File, err)
						}
					}
					return nil
				}
				if each != nil {
					if err := each(txApp, m, run); err != nil {
						return err
					}
				} else if err := run(txApp); err != nil {
					return err
				}

				if err := r.saveAppliedMigration(txApp, m.File); err != nil {
					return fmt.Errorf("failed to save applied migration info for %s: %w", m.File, err)
				}

				applied = append(applied, m.File)
			}

			if dryRun {
				return errMigrationsDryRun
			}
			return nil
		})
	})

	if err != nil && !(dryRun && errors.Is(err, errMigrationsDryRun)) {
		return applied, err
	}
	return applied, nil
}

// Pending returns the migrations Up would run, without running them.
func (r *MigrationsRunner) Pending() ([]string, error) {
	if err := r.initMigrationsTable(); err != nil {
		return nil, err
	}
	pending := []string{}
	for _, m := range r.migrationsList.Items() {
		if r.isMigrationApplied(r.app, m.File) {
			if m.ReapplyCondition == nil {
				continue
			}
			again, err := m.ReapplyCondition(r.app, r, m.File)
			if err != nil {
				return nil, err
			}
			if !again {
				continue
			}
		}
		pending = append(pending, m.File)
	}
	return pending, nil
}

// Down reverts the last `toRevertCount` applied migrations
// (in the order they were applied).
//
// On success returns list with the reverted migrations file names.
func (r *MigrationsRunner) Down(toRevertCount int) ([]string, error) {
	if err := r.initMigrationsTable(); err != nil {
		return nil, err
	}

	reverted := make([]string, 0, toRevertCount)

	names, appliedErr := r.lastAppliedMigrations(toRevertCount)
	if appliedErr != nil {
		return nil, appliedErr
	}

	err := r.app.AuxRunInTransaction(func(txApp App) error {
		return txApp.RunInTransaction(func(txApp App) error {
			for _, name := range names {
				for _, m := range r.migrationsList.Items() {
					if m.File != name {
						continue
					}

					// revert limit reached
					if toRevertCount-len(reverted) <= 0 {
						return nil
					}

					// ignore empty Down action
					if m.Down != nil {
						if err := m.Down(txApp); err != nil {
							return fmt.Errorf("failed to revert migration %s: %w", m.File, err)
						}
					}

					if err := r.saveRevertedMigration(txApp, m.File); err != nil {
						return fmt.Errorf("failed to save reverted migration info for %s: %w", m.File, err)
					}

					reverted = append(reverted, m.File)
				}
			}
			return nil
		})
	})

	if err != nil {
		return nil, err
	}

	return reverted, nil
}

// RemoveMissingAppliedMigrations removes the db entries of all applied migrations
// that are not listed in the runner's migrations list.
func (r *MigrationsRunner) RemoveMissingAppliedMigrations() error {
	loadedMigrations := r.migrationsList.Items()

	names := make([]any, len(loadedMigrations))
	for i, migration := range loadedMigrations {
		names[i] = r.key(migration.File)
	}

	// only the entries of this instance's migration group (and the global
	// ones for the shared group); other groups belong to other instances
	var own dbx.Expression = dbx.Not(dbx.Like("file", "/"))
	if r.group != "" {
		own = dbx.NewExp("[[file]] LIKE {:prefix}", dbx.Params{"prefix": escapeLike(r.group) + "/%"})
	}

	_, err := r.app.DB().Delete(r.tableName, dbx.And(own, dbx.Not(dbx.HashExp{
		"file": names,
	}))).Execute()

	return err
}

func (r *MigrationsRunner) initMigrationsTable() error {
	if r.inited {
		return nil // already inited
	}

	rawQuery := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS {{%s}} (file VARCHAR(255) PRIMARY KEY NOT NULL, applied BIGINT NOT NULL)",
		r.tableName,
	)

	_, err := r.app.DB().NewQuery(rawQuery).Execute()

	if err == nil {
		r.inited = true
	}

	return err
}

func (r *MigrationsRunner) isMigrationApplied(txApp App, file string) bool {
	var exists int

	err := txApp.DB().Select("(1)").
		From(r.tableName).
		Where(dbx.HashExp{"file": r.key(file)}).
		Limit(1).
		Row(&exists)

	return err == nil && exists > 0
}

func (r *MigrationsRunner) saveAppliedMigration(txApp App, file string) error {
	_, err := txApp.DB().Insert(r.tableName, dbx.Params{
		"file":    r.key(file),
		"applied": time.Now().UnixMicro(),
	}).Execute()

	return err
}

func (r *MigrationsRunner) saveRevertedMigration(txApp App, file string) error {
	_, err := txApp.DB().Delete(r.tableName, dbx.HashExp{"file": r.key(file)}).Execute()

	return err
}

func (r *MigrationsRunner) lastAppliedMigrations(limit int) ([]string, error) {
	var files = make([]string, 0, limit)

	loadedMigrations := r.migrationsList.Items()

	names := make([]any, len(loadedMigrations))
	for i, migration := range loadedMigrations {
		names[i] = r.key(migration.File)
	}

	err := r.app.DB().Select("file").
		From(r.tableName).
		Where(dbx.Not(dbx.HashExp{"applied": nil})).
		AndWhere(dbx.HashExp{"file": names}).
		// unify microseconds and seconds applied time for backward compatibility
		OrderBy("substr(applied||'0000000000000000', 0, 17) DESC").
		AndOrderBy("file DESC").
		Limit(int64(limit)).
		Column(&files)

	if err != nil {
		return nil, err
	}

	// stored keys back to file names
	if r.group != "" {
		for i, f := range files {
			files[i] = strings.TrimPrefix(f, r.group+"/")
		}
	}

	return files, nil
}

func escapeLike(v string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(v)
}
