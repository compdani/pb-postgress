package core

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/pocketbase/pocketbase/tools/security"
	"io"
)

func (app *BaseApp) FileMode() (string, error) {
	var mode string
	err := app.DB().Select("mode").From("_fileConfig").Where(dbx.HashExp{"id": "current"}).Row(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return mode, err
}
func (app *BaseApp) SetFileMode(mode string) error {
	if mode != "local" && mode != "fileServer" && mode != "s3" {
		return fmt.Errorf("fileMode must be local, fileServer or s3")
	}
	old, err := app.FileMode()
	if err != nil {
		return err
	}
	if old != "" {
		if old != mode {
			return fmt.Errorf("file mode is already %s; switching modes requires a file migration", old)
		}
		return nil
	}
	_, err = app.DB().Insert("_fileConfig", dbx.Params{"id": "current", "mode": mode}).Execute()
	return err
}
func TestS3Connection(c S3Config) error {
	fs, err := filesystem.NewS3(c.Bucket, c.Region, c.Endpoint, c.AccessKey, c.Secret, c.ForcePathStyle)
	if err != nil {
		return err
	}
	defer fs.Close()
	key := ".pb-probe/" + security.RandomString(32)
	content := []byte(security.RandomString(32))
	if err = fs.Upload(content, key); err != nil {
		return errors.New("S3 probe upload failed")
	}
	defer fs.Delete(key)
	r, err := fs.GetReader(key)
	if err != nil {
		return errors.New("S3 probe read failed")
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(content, got) {
		return errors.New("S3 probe content mismatch")
	}
	if err = fs.Delete(key); err != nil {
		return errors.New("S3 probe delete failed")
	}
	return nil
}
