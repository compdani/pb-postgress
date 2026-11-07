package core

import (
	"errors"
	"fmt"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/pocketbase/pocketbase/tools/security"
	"slices"
	"strings"
	"time"
)

// InitFileTables creates the shared host registry and durable, append-only file feed.
func InitFileTables(app App) error {
	id := "BIGSERIAL PRIMARY KEY"
	if asBaseApp(app).SQLDialect() == DialectMySQL {
		id = "BIGINT AUTO_INCREMENT PRIMARY KEY"
	}
	return ExecuteSchema(app, `CREATE TABLE IF NOT EXISTS {{_fileDeleteQueue}} ([[id]] VARCHAR(32) PRIMARY KEY, [[path]] TEXT NOT NULL);`+`CREATE TABLE IF NOT EXISTS {{_fileHosts}} ([[id]] VARCHAR(255) PRIMARY KEY, [[url]] VARCHAR(2048) NOT NULL, [[mode]] VARCHAR(16) NOT NULL CHECK ([[mode]] IN ('full','bucket','s3')), [[secret]] VARCHAR(255) NOT NULL, [[registered_at]] VARCHAR(64) NOT NULL);
 CREATE TABLE IF NOT EXISTS {{_fileChanges}} ([[id]] `+id+`, [[op]] VARCHAR(8) NOT NULL CHECK ([[op]] IN ('upsert','delete')), [[collection]] VARCHAR(255) NOT NULL, [[record_id]] VARCHAR(255) NOT NULL, [[filename]] VARCHAR(1024) NOT NULL, [[created_at]] VARCHAR(64) NOT NULL);`)
}

type FileChange struct {
	ID         int64  `db:"id" json:"id"`
	Op         string `db:"op" json:"op"`
	Collection string `db:"collection" json:"collection"`
	RecordID   string `db:"record_id" json:"record_id"`
	Filename   string `db:"filename" json:"filename"`
	CreatedAt  string `db:"created_at" json:"created_at"`
}

func recordFileNames(record *Record) []string {
	var names []string
	if record == nil {
		return names
	}
	for _, field := range record.Collection().Fields {
		if field.Type() == FieldTypeFile {
			for _, name := range record.GetStringSlice(field.GetName()) {
				if name != "" && !slices.Contains(names, name) {
					names = append(names, name)
				}
			}
		}
	}
	return names
}
func fileChanges(old, current *Record) []FileChange {
	record := current
	if record == nil {
		record = old
	}
	if record == nil {
		return nil
	}
	before, after := recordFileNames(old), recordFileNames(current)
	changes := []FileChange{}
	for _, name := range before {
		if !slices.Contains(after, name) {
			changes = append(changes, FileChange{Op: "delete", Collection: record.Collection().Id, RecordID: record.Id, Filename: name})
		}
	}
	for _, name := range after {
		if !slices.Contains(before, name) {
			changes = append(changes, FileChange{Op: "upsert", Collection: record.Collection().Id, RecordID: record.Id, Filename: name})
		}
	}
	return changes
}
func appendFileChanges(app App, changes []FileChange) error {
	for _, change := range changes {
		if change.Op == "delete" {
			_, err := app.DB().Insert("_fileDeleteQueue", dbx.Params{"id": security.RandomString(32), "path": change.Collection + "/" + change.RecordID + "/" + change.Filename}).Execute()
			if err != nil {
				return err
			}
		}

		_, err := app.DB().Insert("_fileChanges", dbx.Params{"op": change.Op, "collection": change.Collection, "record_id": change.RecordID, "filename": change.Filename, "created_at": time.Now().UTC().Format(time.RFC3339Nano)}).Execute()
		if err != nil {
			return err
		}
	}
	if len(changes) > 0 && asBaseApp(app).SQLDialect() == DialectPostgres {
		_, err := app.DB().NewQuery(`SELECT pg_notify('pb_file_changes', '')`).Execute()
		return err
	}
	return nil
}
func appendCollectionFileDeletes(app App, c *Collection) error {
	if c.IsView() {
		return nil
	}
	offset := int64(0)
	for {
		records := []*Record{}
		err := app.RecordQuery(c).OrderBy("id").Limit(500).Offset(offset).All(&records)
		if err != nil {
			return fmt.Errorf("list collection files: %w", err)
		}
		for _, r := range records {
			if err = appendFileChanges(app, fileChanges(r, nil)); err != nil {
				return err
			}
		}
		if len(records) < 500 {
			return nil
		}
		offset += 500
	}
}

// RetryFileDeletes processes durable deletion work after commit. Failures remain
// queued; the immutable file feed is retained for other consumers.
func (app *BaseApp) RetryFileDeletes() error {
	if app.IsTransactional() {
		return fmt.Errorf("file deletions must run after commit")
	}
	rows := []struct {
		ID   string
		Path string
	}{}
	if err := app.DB().Select("id", "path").From("_fileDeleteQueue").Limit(100).All(&rows); err != nil {
		return err
	}
	fs, err := app.NewFilesystem()
	if err != nil {
		return err
	}
	defer fs.Close()
	for _, row := range rows {
		parts := strings.Split(row.Path, "/")
		if len(parts) == 3 {
			if record, err := app.FindRecordById(parts[0], parts[1]); err == nil && slices.Contains(recordFileNames(record), parts[2]) {
				if _, err = app.DB().Delete("_fileDeleteQueue", dbx.HashExp{"id": row.ID}).Execute(); err != nil {
					return err
				}
				continue
			}
		}

		err := fs.Delete(row.Path)
		if err != nil && !errors.Is(err, filesystem.ErrNotFound) {
			continue
		}
		parts = strings.Split(row.Path, "/")
		if len(parts) >= 3 {
			prefix := strings.Join(parts[:len(parts)-1], "/") + "/thumbs_" + parts[len(parts)-1] + "/"
			if len(fs.DeletePrefix(prefix)) > 0 {
				continue
			}
		}
		if _, err = app.DB().Delete("_fileDeleteQueue", dbx.HashExp{"id": row.ID}).Execute(); err != nil {
			return err
		}
	}
	return nil
}
