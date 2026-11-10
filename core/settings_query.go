package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pocketbase/dbx"
	"os"
	"reflect"

	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"
)

type Param struct {
	BaseModel

	Created types.DateTime `db:"created" json:"created"`
	Updated types.DateTime `db:"updated" json:"updated"`
	Value   types.JSONRaw  `db:"value" json:"value"`
}

func (m *Param) TableName() string {
	return paramsTable
}

// ReloadSettings initializes and reloads the stored application settings.
//
// If no settings were stored it will persist the current app ones.
func (app *BaseApp) ReloadSettings() error {
	param := &Param{}
	err := app.ModelQuery(param).Model(paramsKeySettings, param)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	// no settings were previously stored -> save
	// (ReloadSettings() will be invoked again by a system hook after successful save)
	if param.Id == "" {
		// force insert in case the param entry was deleted manually after application start
		app.Settings().MarkAsNew()
		return app.Save(app.Settings())
	}

	// an instance with its own settings row uses it instead of the shared one
	// (S3 and backups settings always come from the shared row)
	own := &Param{}
	if id, err := app.InstanceID(); err == nil {
		err := app.ModelQuery(own).Model(paramsKeyInstanceSettingsPrefix+id, own)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}

	event := new(SettingsReloadEvent)
	event.App = app

	return app.OnSettingsReload().Trigger(event, func(e *SettingsReloadEvent) error {
		s := e.App.Settings()
		if own.Id == "" {
			s.setKey("")
			if err := s.loadParam(e.App, param); err != nil {
				return err
			}
			s.mu.Lock()
			s.loadedS3, s.loadedBackups, s.loadedInit = s.S3, s.Backups, true
			s.mu.Unlock()
			return nil
		}
		if err := s.loadParam(e.App, own); err != nil {
			return err
		}
		s.setKey(own.Id)
		shared := newDefaultSettings()
		if err := shared.loadParam(e.App, param); err != nil {
			return err
		}
		s.mu.Lock()
		s.S3, s.Backups = shared.S3, shared.Backups
		s.loadedS3, s.loadedBackups, s.loadedInit = shared.S3, shared.Backups, true
		s.mu.Unlock()
		return nil
	})
}

func (s *Settings) setKey(key string) {
	s.mu.Lock()
	s.key = key
	s.mu.Unlock()
}

// syncSharedSettings writes the S3 and backups settings of an instance's
// own settings row to the shared row.
func syncSharedSettings(app App, model Model) error {
	s, ok := model.(*Settings)
	if !ok || s.paramKey() == paramsKeySettings {
		return nil
	}
	param := &Param{}
	if err := app.ModelQuery(param).Model(paramsKeySettings, param); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	shared := newDefaultSettings()
	if err := shared.loadParam(app, param); err != nil {
		return err
	}
	s.mu.RLock()
	init := s.loadedInit
	// without a snapshot nothing is known to have changed
	s3Changed := init && !reflect.DeepEqual(s.S3, s.loadedS3)
	backupsChanged := init && !reflect.DeepEqual(s.Backups, s.loadedBackups)
	if s3Changed {
		shared.S3 = s.S3
	}
	if backupsChanged {
		shared.Backups = s.Backups
	}
	s.mu.RUnlock()
	if !s3Changed && !backupsChanged {
		return nil // this instance did not change the shared values
	}
	shared.MarkAsNotNew()
	data, err := shared.DBExport(app)
	if err != nil {
		return err
	}
	_, err = app.DB().Update(paramsTable, dbx.Params{"value": data["value"], "updated": data["updated"]}, dbx.HashExp{"id": paramsKeySettings}).Execute()
	return err
}

// EnableInstanceSettings gives the current instance its own settings row,
// copied from the instance fromInstanceID (or the current settings when
// empty). S3 and backups settings stay shared.
func (app *BaseApp) EnableInstanceSettings(fromInstanceID string) error {
	id, err := app.InstanceID()
	if err != nil {
		return err
	}
	key := paramsKeyInstanceSettingsPrefix + id

	var exists int
	if err := app.DB().Select("count(*)").From(paramsTable).Where(dbx.HashExp{"id": key}).Row(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}

	clone, err := app.Settings().Clone()
	if err != nil {
		return err
	}
	if fromInstanceID != "" && fromInstanceID != id {
		src := &Param{}
		if err := app.ModelQuery(src).Model(paramsKeyInstanceSettingsPrefix+fromInstanceID, src); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			// the source uses the shared settings
			if err := app.ModelQuery(src).Model(paramsKeySettings, src); err != nil {
				return err
			}
		}
		if clone, err = newSettingsFromParam(app, src); err != nil {
			return err
		}
		// S3 and backups settings are authoritative in the shared row
		if err := overlaySharedSettings(app, clone); err != nil {
			return err
		}
	}
	clone.setKey(key)
	clone.MarkAsNew()
	return app.SaveNoValidate(clone)
}

// DisableInstanceSettings removes the current instance's own settings row so
// it uses the shared settings again.
func (app *BaseApp) DisableInstanceSettings() error {
	id, err := app.InstanceID()
	if err != nil {
		return err
	}
	if _, err := app.DB().Delete(paramsTable, dbx.HashExp{"id": paramsKeyInstanceSettingsPrefix + id}).Execute(); err != nil {
		return err
	}
	return app.ReloadSettings()
}

func newSettingsFromParam(app App, p *Param) (*Settings, error) {
	s := newDefaultSettings()
	if err := s.loadParam(app, p); err != nil {
		return nil, err
	}
	return s, nil
}

// loadParam loads the settings from the stored param into the app ones.
//
// @todo note that the encryption may get removed in the future since it doesn't
// really accomplish much and it might be better to find a way to encrypt the backups
// or implement support for resolving env variables.
func (s *Settings) loadParam(app App, param *Param) error {
	// try first without decryption
	s.mu.Lock()
	plainDecodeErr := json.Unmarshal(param.Value, s)
	s.mu.Unlock()

	// failed, try to decrypt
	if plainDecodeErr != nil {
		encryptionKey := os.Getenv(app.EncryptionEnv())

		// load without decryption has failed and there is no encryption key to use for decrypt
		if encryptionKey == "" {
			return fmt.Errorf("invalid settings db data or missing encryption key %q", app.EncryptionEnv())
		}

		// decrypt
		decrypted, decryptErr := security.Decrypt(string(param.Value), encryptionKey)
		if decryptErr != nil {
			return decryptErr
		}

		// decode again
		s.mu.Lock()
		decryptedDecodeErr := json.Unmarshal(decrypted, s)
		s.mu.Unlock()
		if decryptedDecodeErr != nil {
			return decryptedDecodeErr
		}
	}

	return s.PostScan()
}

// overlaySharedSettings replaces the S3 and backups settings of s with the
// ones stored in the shared settings row.
func overlaySharedSettings(app App, s *Settings) error {
	param := &Param{}
	if err := app.ModelQuery(param).Model(paramsKeySettings, param); err != nil {
		return err
	}
	shared, err := newSettingsFromParam(app, param)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.S3, s.Backups = shared.S3, shared.Backups
	s.loadedS3, s.loadedBackups, s.loadedInit = shared.S3, shared.Backups, true
	s.mu.Unlock()
	return nil
}
