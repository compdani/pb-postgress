package apis

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/spf13/cast"
)

// bindBackupApi registers the file api endpoints and the corresponding handlers.
func bindBackupApi(app core.App, rg *router.RouterGroup[*core.RequestEvent]) {
	sub := rg.Group("/backups")
	sub.GET("", backupsList).Bind(RequireSuperuserAuth())
	sub.POST("", backupCreate).Bind(RequireSuperuserAuth())
	sub.POST("/upload", backupUpload).Bind(BodyLimit(0), RequireSuperuserAuth())
	sub.GET("/{key}", backupDownload) // relies on superuser file token
	sub.DELETE("/{key}", backupDelete).Bind(RequireSuperuserAuth())
	sub.GET("/{key}/collections", backupCollections).Bind(RequireSuperuserAuth())
	sub.POST("/restore-files", backupRestoreFiles).Bind(RequireSuperuserAuth())
	sub.DELETE("/restore-files", backupDiscardRestoreFiles).Bind(RequireSuperuserAuth())
	sub.POST("/{key}/restore", backupRestore).Bind(RequireSuperuserAuth())
}

type backupFileInfo struct {
	Modified types.DateTime `json:"modified"`
	Key      string         `json:"key"`
	Size     int64          `json:"size"`
}

func backupsList(e *core.RequestEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fsys, err := e.App.NewBackupsFilesystem()
	if err != nil {
		return e.BadRequestError("Failed to load backups filesystem.", err)
	}
	defer fsys.Close()

	fsys.SetContext(ctx)

	backups, err := fsys.List("")
	if err != nil {
		return e.BadRequestError("Failed to retrieve backup items. Raw error: \n"+err.Error(), nil)
	}

	result := make([]backupFileInfo, len(backups))

	for i, obj := range backups {
		modified, _ := types.ParseDateTime(obj.ModTime)

		result[i] = backupFileInfo{
			Key:      obj.Key,
			Size:     obj.Size,
			Modified: modified,
		}
	}

	return e.JSON(http.StatusOK, result)
}

func backupDownload(e *core.RequestEvent) error {
	fileToken := e.Request.URL.Query().Get("token")

	authRecord, err := e.App.FindAuthRecordByToken(fileToken, core.TokenTypeFile)
	if err != nil || !authRecord.IsSuperuser() {
		return e.ForbiddenError("Insufficient permissions to access the resource.", err)
	}

	allowedIPs := e.App.Settings().SuperuserIPs
	if len(allowedIPs) > 0 && !isIPInList(allowedIPs, e.RealIP()) {
		return e.ForbiddenError("Insufficient permissions to access the resource.", nil)
	}

	fsys, err := e.App.NewBackupsFilesystem()
	if err != nil {
		return e.InternalServerError("Failed to load backups filesystem.", err)
	}
	defer fsys.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	fsys.SetContext(ctx)

	key := e.Request.PathValue("key")

	return fsys.Serve(
		e.Response,
		e.Request,
		key,
		filepath.Base(key), // without the path prefix (if any)
	)
}

func backupDelete(e *core.RequestEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fsys, err := e.App.NewBackupsFilesystem()
	if err != nil {
		return e.InternalServerError("Failed to load backups filesystem.", err)
	}
	defer fsys.Close()

	fsys.SetContext(ctx)

	key := e.Request.PathValue("key")

	if key != "" && cast.ToString(e.App.Store().Get(core.StoreKeyActiveBackup)) == key {
		return e.BadRequestError("The backup is currently being used and cannot be deleted.", nil)
	}
	if ba := core.AsBaseApp(e.App); ba != nil && key != "" {
		pending, ok, err := ba.PendingRestoreFilesBackup()
		if err != nil {
			return e.InternalServerError("Could not check the pending restore state.", err)
		}
		if ok && pending == key {
			return e.BadRequestError("The backup still has files to copy after a restore. Copy or discard them first.", nil)
		}
	}

	if err := fsys.Delete(key); err != nil {
		return e.BadRequestError("Invalid or already deleted backup file. Raw error: \n"+err.Error(), nil)
	}

	return e.NoContent(http.StatusNoContent)
}

func backupRestore(e *core.RequestEvent) error {
	if e.App.Store().Has(core.StoreKeyActiveBackup) {
		return e.BadRequestError("Try again later - another backup/restore process has already been started.", nil)
	}

	key := e.Request.PathValue("key")

	existsCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fsys, err := e.App.NewBackupsFilesystem()
	if err != nil {
		return e.InternalServerError("Failed to load backups filesystem.", err)
	}
	defer fsys.Close()

	fsys.SetContext(existsCtx)

	if exists, err := fsys.Exists(key); !exists {
		return e.BadRequestError("Missing or invalid backup file.", err)
	}

	// The restore runs synchronously, so a failure is reported to the
	// caller. It is detached from the request context so a dropped
	// connection does not abort it halfway.
	ctx, cancelRestore := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancelRestore()

	opts := core.DefaultRestoreOptions()
	if v := e.Request.URL.Query().Get("dropCollections"); v != "" {
		drop, err := strconv.ParseBool(v)
		if err != nil {
			return e.BadRequestError("Invalid dropCollections value.", err)
		}
		opts.DropCollections = drop
	}

	opts.ExcludeCollections = e.Request.URL.Query()["excludeCollections"]

	// optional JSON body for the options that don't fit in a query string
	if e.Request.ContentLength != 0 {
		body := struct {
			DropCollections    *bool                                `json:"dropCollections"`
			ExcludeCollections []string                             `json:"excludeCollections"`
			Resolutions        map[string]core.CollectionResolution `json:"resolutions"`
			RequireResolution  bool                                 `json:"requireResolution"`
			Settings           string                               `json:"settings"`
		}{}
		if err := e.BindBody(&body); err != nil {
			return e.BadRequestError("Invalid restore options.", err)
		}
		if body.DropCollections != nil {
			opts.DropCollections = *body.DropCollections
		}
		opts.ExcludeCollections = append(opts.ExcludeCollections, body.ExcludeCollections...)
		opts.Resolutions = body.Resolutions
		opts.RequireResolution = body.RequireResolution
		opts.Settings = body.Settings
	}

	if err := e.App.RestoreBackupWithOptions(ctx, key, opts); err != nil {
		return e.BadRequestError("Failed to restore the backup: "+err.Error(), nil)
	}

	// Views a PocketBase import translated or skipped.
	report, _ := e.App.Store().Get(core.StoreKeyLastRestoreReport).(*core.RestoreReport)
	if report == nil {
		report = &core.RestoreReport{TranslatedViews: []core.RestoreViewReport{}, SkippedViews: []core.RestoreViewReport{}}
	}

	return e.JSON(http.StatusOK, report)
}

func backupCollections(e *core.RequestEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	list, err := e.App.AnalyzeBackup(ctx, e.Request.PathValue("key"))
	if err != nil {
		return e.BadRequestError("Failed to read the backup collections: "+err.Error(), nil)
	}

	return e.JSON(http.StatusOK, list)
}

// backupRestoreFiles retries copying the files of a restore whose database
// part committed.
func backupRestoreFiles(e *core.RequestEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	if err := e.App.RetryRestoreFiles(ctx); err != nil {
		return e.BadRequestError("Failed to copy the files: "+err.Error(), nil)
	}
	return e.NoContent(http.StatusNoContent)
}

func backupDiscardRestoreFiles(e *core.RequestEvent) error {
	if err := e.App.DiscardRestoreFiles(); err != nil {
		return e.BadRequestError("Failed to discard the pending files: "+err.Error(), nil)
	}
	return e.NoContent(http.StatusNoContent)
}
