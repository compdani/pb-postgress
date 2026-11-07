package core_test

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This exercises the actual cmd/bucket process and both table creation orders.
func TestBucketProcessFileLifecycle(t *testing.T) {
	for _, first := range []string{"core", "bucket"} {
		t.Run(first+"_creates_tables", func(t *testing.T) {
			cfg := tests.NewDatabaseConfig(t)
			binary := filepath.Join(t.TempDir(), "bucket")
			build := exec.Command("go", "build", "-o", binary, "../cmd/bucket")
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build bucket: %v\n%s", err, output)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().String()
			listener.Close()
			dir := t.TempDir()
			app := core.NewBaseApp(cfg)
			bootstrap := func() {
				if err := app.Bootstrap(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { app.ClearBootstrap() })
			}
			if first == "core" {
				bootstrap()
			}
			cmd := exec.Command(binary, "--dbDriver", cfg.DBDriver, "--dbDSN", cfg.DBDSN, "--http", addr, "--publicURL", "http://"+addr, "--dir", dir, "--apiKey", "integration-test-key")
			logs, err := os.Create(filepath.Join(t.TempDir(), "bucket.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer logs.Close()
			cmd.Stdout = logs
			cmd.Stderr = logs
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cmd.Process.Signal(os.Interrupt); cmd.Wait() })
			wait := func(check func() bool) {
				t.Helper()
				deadline := time.Now().Add(15 * time.Second)
				for time.Now().Before(deadline) {
					if check() {
						return
					}
					time.Sleep(50 * time.Millisecond)
				}
				t.Fatal("bucket lifecycle timed out")
			}
			wait(func() bool {
				c, e := net.DialTimeout("tcp", addr, 100*time.Millisecond)
				if e != nil {
					return false
				}
				c.Close()
				return true
			})
			if first == "bucket" {
				bootstrap()
			}
			host, err := app.FileHost()
			if err != nil || host == nil || host.RegisteredAt.IsZero() {
				t.Fatalf("registry: %+v %v", host, err)
			}
			if err := app.SetFileMode("fileServer"); err != nil {
				t.Fatal(err)
			} // remote host is mode=bucket
			c := core.NewBaseCollection("bucket_lifecycle")
			c.Fields.Add(&core.FileField{Name: "file", MaxSelect: 1})
			if err := app.Save(c); err != nil {
				t.Fatal(err)
			}
			r := core.NewRecord(c)
			f, err := filesystem.NewFileFromBytes([]byte("bucket test bytes"), "hello.txt")
			if err != nil {
				t.Fatal(err)
			}
			r.Set("file", f)
			if err := app.Save(r); err != nil {
				t.Fatal(err)
			}
			key := filepath.Join(dir, "objects", c.Id, r.Id, r.GetString("file"))
			content, err := os.ReadFile(key)
			if err != nil || string(content) != "bucket test bytes" {
				t.Fatalf("uploaded bytes: %s %v", content, err)
			}
			if err := app.Delete(r); err != nil {
				t.Fatal(err)
			}
			// Reintroduce the bytes after synchronous deletion so only the feed watcher
			// can remove them.
			// A second event also verifies that the bucket scans the timestamp column.
			if err := os.MkdirAll(filepath.Dir(key), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(key, content, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := app.DB().Insert("_fileChanges", map[string]any{"op": "delete", "collection": c.Id, "record_id": r.Id, "filename": r.GetString("file"), "created_at": time.Now().UTC()}).Execute(); err != nil {
				t.Fatal(err)
			}
			wait(func() bool { _, e := os.Stat(key); return os.IsNotExist(e) })
		})
	}
}
