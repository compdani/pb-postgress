package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/pocketbase/pocketbase/tools/inflector"
	"github.com/pocketbase/pocketbase/tools/security"
)

const StoreKeyActiveBackup = "@activeBackup"

// ErrBackupScoped is returned when a backup or restore is started from an
// instance that sees only part of the shared database.
var ErrBackupScoped = errors.New("backups need an instance with the \"all\" collection scope, because they cover the whole database")

// CreateBackup writes a portable backup of the whole database to the
// backups filesystem under name.
//
// The archive holds the collections, settings, applied migrations and every
// record (see [PortableBackupFormat]). Record files are included only for
// collections whose files are stored in this instance's pb_data/storage;
// S3 and remote file hosts are expected to keep their own copies.
//
// Backups are created on demand only. Scheduled protection belongs to the
// PostgreSQL/MySQL server (base backups, PITR, binlogs, managed snapshots).
func (app *BaseApp) CreateBackup(ctx context.Context, name string) error {
	release, err := app.acquireBackupOp(name)
	if err != nil {
		return err
	}
	defer release()

	if app.HasPendingRestoreFiles() {
		return errors.New("a previous restore did not finish copying its files: retry copying them (or discard them) before creating a backup")
	}

	event := new(BackupEvent)
	event.App = app
	event.Context = ctx
	event.Name = name

	return app.OnBackupCreate().Trigger(event, func(e *BackupEvent) error {
		if e.Name == "" {
			e.Name = generateBackupName(e.App, "pb_backup_")
		}

		ba := asBaseApp(e.App)
		if ba == nil {
			return errors.New("backups require a *BaseApp")
		}

		tempDir, err := ba.backupTempDir()
		if err != nil {
			return err
		}

		tempPath := filepath.Join(tempDir, "pb_backup_"+security.PseudorandomString(8))
		defer os.Remove(tempPath)

		if err := ba.writePortableBackup(e.Context, tempPath); err != nil {
			return err
		}

		fsys, err := e.App.NewBackupsFilesystem()
		if err != nil {
			return err
		}
		defer fsys.Close()

		fsys.SetContext(e.Context)

		file, err := filesystem.NewFileFromPath(tempPath)
		if err != nil {
			return err
		}
		file.OriginalName = e.Name
		file.Name = file.OriginalName

		return fsys.UploadFile(file, file.Name)
	})
}

var backupOpMu sync.Mutex

// acquireBackupOp marks a backup/restore operation as active atomically.
func (app *BaseApp) acquireBackupOp(name string) (release func(), err error) {
	backupOpMu.Lock()
	defer backupOpMu.Unlock()
	if app.Store().Has(StoreKeyActiveBackup) {
		return nil, errors.New("try again later - another backup/restore operation has already been started")
	}
	app.Store().Set(StoreKeyActiveBackup, name)
	return func() { app.Store().Remove(StoreKeyActiveBackup) }, nil
}

// RestoreBackup replaces the database content with the backup stored
// under name in the backups filesystem.
//
// Two archive types are accepted:
//   - a portable backup created by [BaseApp.CreateBackup] on PostgreSQL or MySQL;
//   - a standard PocketBase backup (a pb_data zip with data.db), from
//     PocketBase v0.23.0 through v0.41.x (see [PocketBaseImportVersions]).
//
// The archive is fully read and checked before anything is changed.
// Collections, records, settings and migrations are replaced first; the files
// are copied into the configured file storage after that committed. On
// PostgreSQL the database step is one transaction; on MySQL schema changes
// commit implicitly, so only the record data is atomic.
//
// The instance's file mode, file host, S3 storage and backups settings, and
// instance scopes are kept. Superusers and their sessions come from the
// backup. Other instances sharing the database must be restarted to reload
// their cached collections and settings.
func (app *BaseApp) RestoreBackup(ctx context.Context, name string) error {
	return app.RestoreBackupWithOptions(ctx, name, DefaultRestoreOptions())
}

// RestoreBackupWithOptions is the same as [BaseApp.RestoreBackup] but
// allows choosing whether collections missing from the backup are dropped.
func (app *BaseApp) RestoreBackupWithOptions(ctx context.Context, name string, opts RestoreOptions) error {
	release, err := app.acquireBackupOp(name)
	if err != nil {
		return err
	}
	defer release()

	if app.HasPendingRestoreFiles() {
		return errors.New("a previous restore did not finish copying its files: retry copying them (or discard them) before restoring again")
	}

	event := new(BackupEvent)
	event.App = app
	event.Context = ctx
	event.Name = name

	return app.OnBackupRestore().Trigger(event, func(e *BackupEvent) error {
		ba := asBaseApp(e.App)
		if ba == nil {
			return errors.New("backups require a *BaseApp")
		}

		archivePath, cleanup, err := ba.localBackupCopy(e.Context, e.Name)
		if err != nil {
			return err
		}
		defer cleanup()

		tempDir, err := ba.backupTempDir()
		if err != nil {
			return err
		}

		src, err := openBackupSource(archivePath, tempDir)
		if err != nil {
			return err
		}
		defer src.Close()

		return ba.restoreFromSource(e.Context, src, e.Name, opts)
	})
}

func (app *BaseApp) backupTempDir() (string, error) {
	dir := filepath.Join(app.DataDir(), LocalTempDirName)
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return "", fmt.Errorf("failed to create a temp dir: %w", err)
	}
	return dir, nil
}

// localBackupCopy returns a local path of the named backup, downloading
// it into the temp dir when backups are stored in S3.
func (app *BaseApp) localBackupCopy(ctx context.Context, name string) (string, func(), error) {
	noop := func() {}

	if name == "" || name != filepath.Base(name) {
		return "", noop, fmt.Errorf("invalid backup name %q", name)
	}

	fsys, err := app.NewBackupsFilesystem()
	if err != nil {
		return "", noop, err
	}
	defer fsys.Close()

	fsys.SetContext(ctx)

	if ok, _ := fsys.Exists(name); !ok {
		return "", noop, fmt.Errorf("missing or invalid backup file %q to restore", name)
	}

	if app.settings == nil || !app.settings.Backups.S3.Enabled {
		return filepath.Join(app.DataDir(), LocalBackupsDirName, name), noop, nil
	}

	tempDir, err := app.backupTempDir()
	if err != nil {
		return "", noop, err
	}

	br, err := fsys.GetReader(name)
	if err != nil {
		return "", noop, err
	}
	defer br.Close()

	tempZip, err := os.CreateTemp(tempDir, "pb_restore_zip")
	if err != nil {
		return "", noop, err
	}
	cleanup := func() { os.Remove(tempZip.Name()) }

	_, copyErr := io.Copy(tempZip, br)
	closeErr := tempZip.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		cleanup()
		return "", noop, err
	}

	return tempZip.Name(), cleanup, nil
}

func generateBackupName(app App, prefix string) string {
	appName := inflector.Snakecase(app.Settings().Meta.AppName)
	if len(appName) > 50 {
		appName = appName[:50]
	}

	return fmt.Sprintf(
		"%s%s_%s.zip",
		prefix,
		appName,
		time.Now().UTC().Format("20060102150405"),
	)
}
