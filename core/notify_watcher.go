package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/fsnotify/fsnotify"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/routine"
	"github.com/pocketbase/pocketbase/tools/security"
)

const systemHookIdNotifyWatcher = "__pbNotifyWatcherSystemHook__"

func (app *BaseApp) registerNotifyWatcherHooks() {
	var notifyWatcher *fsnotify.Watcher
	var notifyDone <-chan struct{} // closed when the watcher's event goroutine returned

	// closeWatcher closes the watcher and waits for its event goroutine, so it can
	// no longer read the app state while a bootstrap clear is tearing it down
	closeWatcher := func() {
		if notifyWatcher != nil {
			_ = notifyWatcher.Close()
		}
		if notifyDone != nil {
			<-notifyDone
		}
		notifyWatcher, notifyDone = nil, nil
	}

	instanceId := "@" + security.PseudorandomString(10)

	localNotifyDirPath := filepath.Join(app.DataDir(), LocalNotifyDirName)
	settingsFile := filepath.Join(localNotifyDirPath, "settings"+instanceId)
	collectionsFile := filepath.Join(localNotifyDirPath, "collections"+instanceId)

	// init
	app.OnBootstrap().Bind(&hook.Handler[*BootstrapEvent]{
		Id: systemHookIdNotifyWatcher,
		Func: func(e *BootstrapEvent) error {
			err := e.Next()
			if err != nil {
				return err
			}

			closeWatcher()

			notifyWatcher, notifyDone, err = createNotifyDirWatcher(e.App, instanceId, localNotifyDirPath)
			if err != nil {
				e.App.Logger().Warn("Notify dir watcher failure.", "error", err)
			}

			return nil
		},
		Priority: -998,
	})

	// cleanup
	app.OnTerminate().Bind(&hook.Handler[*TerminateEvent]{
		Id: systemHookIdNotifyWatcher,
		Func: func(e *TerminateEvent) error {
			closeWatcher()

			_ = os.Remove(settingsFile)
			_ = os.Remove(collectionsFile)

			return e.Next()
		},
		Priority: -998,
	})

	app.OnBootstrapClear().Bind(&hook.Handler[*BootstrapEvent]{
		Id:       systemHookIdNotifyWatcher,
		Priority: -998,
		Func: func(e *BootstrapEvent) error {
			closeWatcher()
			_ = os.Remove(settingsFile)
			_ = os.Remove(collectionsFile)
			return e.Next()
		},
	})

	// ---------------------------------------------------------------

	settingsNotify := func(e *ModelEvent) error {
		err := e.Next()
		if err != nil || !isSettingsKey(e.Model.PK()) {
			return err
		}

		if notifyWatcher != nil {
			if err := os.WriteFile(settingsFile, nil, 0644); err != nil {
				e.App.Logger().Warn("Failed to write watcher file", "error", err, "file", settingsFile)
			}
		}

		return nil
	}
	app.OnModelAfterCreateSuccess(paramsTable).Bind(&hook.Handler[*ModelEvent]{
		Id:       systemHookIdNotifyWatcher,
		Func:     settingsNotify,
		Priority: 999,
	})
	app.OnModelAfterUpdateSuccess(paramsTable).Bind(&hook.Handler[*ModelEvent]{
		Id:       systemHookIdNotifyWatcher,
		Func:     settingsNotify,
		Priority: 999,
	})

	// ---------------------------------------------------------------

	collectionsNotify := func(e *CollectionEvent) error {
		if err := e.Next(); err != nil {
			return err
		}

		if notifyWatcher != nil {
			if err := os.WriteFile(collectionsFile, nil, 0644); err != nil {
				e.App.Logger().Warn("Failed to write watcher file", "error", err, "file", collectionsFile)
			}
		}

		return nil
	}
	app.OnCollectionAfterCreateSuccess().Bind(&hook.Handler[*CollectionEvent]{
		Id:       systemHookIdNotifyWatcher,
		Func:     collectionsNotify,
		Priority: 999,
	})
	app.OnCollectionAfterUpdateSuccess().Bind(&hook.Handler[*CollectionEvent]{
		Id:       systemHookIdNotifyWatcher,
		Func:     collectionsNotify,
		Priority: 999,
	})
	app.OnCollectionAfterDeleteSuccess().Bind(&hook.Handler[*CollectionEvent]{
		Id:       systemHookIdNotifyWatcher,
		Func:     collectionsNotify,
		Priority: 999,
	})
}

func createNotifyDirWatcher(app App, instanceId string, localNotifyDirPath string) (*fsnotify.Watcher, <-chan struct{}, error) {
	// create the notify dir (if not already)
	err := os.MkdirAll(localNotifyDirPath, os.ModePerm)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create a notify dir: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to init notify dir watcher: %w", err)
	}

	err = watcher.Add(localNotifyDirPath)
	if err != nil {
		_ = watcher.Close()
		return nil, nil, fmt.Errorf("unable to watch notify dir: %w", err)
	}

	var debounceTimer *time.Timer

	// tracks the scheduled debounce callbacks, so closing the watcher can wait
	// for one that already started instead of leaving it running during teardown
	var callbacks sync.WaitGroup

	stopDebounceTimer := func() {
		if debounceTimer != nil {
			if debounceTimer.Stop() {
				callbacks.Done() // it never ran
			}
			debounceTimer = nil
		}
	}

	// watch
	done := make(chan struct{})
	routine.FireAndForget(func() {
		defer close(done)
		defer callbacks.Wait() // after the timer was stopped (deferred below, runs first)
		defer stopDebounceTimer()

		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				// modified from within the current app instance or cleanup event
				if strings.HasSuffix(event.Name, instanceId) || !app.IsBootstrapped() {
					continue
				}

				stopDebounceTimer()

				callbacks.Add(1)
				debounceTimer = time.AfterFunc(50*time.Millisecond, func() {
					defer callbacks.Done()

					filename := filepath.Base(event.Name)

					// settings changed
					if strings.HasPrefix(filename, "settings@") {
						app.Logger().Debug("Reloading settings after notify event")

						// published through the cache sync (it never reverts a local collection write)
						if ba := asBaseApp(app); ba != nil && ba.cacheSync != nil && ba.cacheSync.running() {
							ba.requestCacheReload(cacheVersionSettings)
							return
						}

						err := app.ReloadSettings()
						if err != nil {
							app.Logger().Warn("Failed to reload app settings after notify", "error", err)
						}
						return
					}

					// collections changed
					if strings.HasPrefix(filename, "collections@") {
						app.Logger().Debug("Reloading cached collections after notify event")

						if ba := asBaseApp(app); ba != nil && ba.cacheSync != nil && ba.cacheSync.running() {
							ba.requestCacheReload(cacheVersionCollections)
							return
						}

						err := app.ReloadCachedCollections()
						if err != nil {
							app.Logger().Warn("Failed to reload cached collections after notify", "error", err)
						}
						return
					}
				})
			case err, ok := <-watcher.Errors:
				if app.IsDev() && err != nil {
					color.Red("Notify dir watch error:", err)
				}

				if !ok {
					return
				}
			}
		}
	})

	return watcher, done, err
}
