package bucket

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the bucket process configuration.
type Config struct {
	Dir         string
	HTTPAddr    string
	APIKey      string
	PublicURL   string
	DBDriver    string
	DBDSN       string
	MaxBytes    int64
	CORSOrigins []string
	PollEvery   time.Duration
}

// Identity is the stable id and bearer secret registered in _fileHosts.
type Identity struct {
	ID        string
	Secret    string
	Generated bool
	URL       string
}

// Run registers the bucket, serves the HTTP API, and watches _fileChanges
// until ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return err
	}
	identity, err := LoadIdentity(cfg.Dir, cfg.APIKey, cfg.PublicURL, cfg.HTTPAddr)
	if err != nil {
		return err
	}
	printGeneratedSecret(os.Stdout, identity)

	store, err := OpenStore(cfg.Dir, cfg.MaxBytes)
	if err != nil {
		return err
	}
	db, err := OpenDB(ctx, cfg.DBDriver, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		return err
	}
	if err := db.Register(ctx, Host{
		ID:           identity.ID,
		URL:          identity.URL,
		Mode:         "bucket",
		Secret:       identity.Secret,
		RegisteredAt: time.Now().UTC(),
	}); err != nil {
		return err
	}

	srv := NewServer(store, Options{
		APIKey:      identity.Secret,
		MaxBytes:    cfg.MaxBytes,
		CORSOrigins: cfg.CORSOrigins,
	})
	watcher := &Watcher{Source: db, Store: store, PollEvery: cfg.PollEvery}
	go func() {
		if err := watcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("bucket watcher stopped: %v", err)
		}
	}()

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("bucket listening on %s (id %s, url %s)", cfg.HTTPAddr, identity.ID, identity.URL)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func printGeneratedSecret(w io.Writer, id Identity) {
	if id.Generated {
		fmt.Fprintf(w, "PB_BUCKET_API_KEY=%s\n", id.Secret)
	}
}

func (c Config) withDefaults() Config {
	if c.HTTPAddr == "" {
		c.HTTPAddr = "127.0.0.1:8091"
	}
	if c.Dir == "" {
		c.Dir = "pb_bucket"
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = 100 << 20
	}
	if c.PollEvery <= 0 {
		c.PollEvery = 2 * time.Second
	}
	return c
}

func (c Config) validate() error {
	if _, err := normalizeDriver(c.DBDriver); err != nil {
		return err
	}
	if strings.TrimSpace(c.DBDSN) == "" {
		return errors.New("database dsn is required")
	}
	if strings.TrimSpace(c.Dir) == "" {
		return errors.New("storage dir is required")
	}
	return nil
}

// LoadIdentity reads or creates the instance id and bearer secret.
// Generated is true only the first time a secret is created, which is when
// the process prints it.
func LoadIdentity(dir, apiKey, publicURL, httpAddr string) (Identity, error) {
	id, err := readOrCreate(filepath.Join(dir, ".instance-id"), func() (string, error) {
		return randomToken("bkt_", 16)
	})
	if err != nil {
		return Identity{}, err
	}
	var (
		secret    string
		generated bool
	)
	if strings.TrimSpace(apiKey) != "" {
		secret = strings.TrimSpace(apiKey)
		if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte(secret), 0o600); err != nil {
			return Identity{}, err
		}
	} else {
		secret, generated, err = readOrCreateFlag(filepath.Join(dir, ".secret"), func() (string, error) {
			return randomToken("", 32)
		})
		if err != nil {
			return Identity{}, err
		}
	}
	return Identity{
		ID:        strings.TrimSpace(id),
		Secret:    strings.TrimSpace(secret),
		Generated: generated,
		URL:       publicBaseURL(publicURL, httpAddr),
	}, nil
}

func publicBaseURL(explicit, addr string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimRight(strings.TrimSpace(explicit), "/")
	}
	host := strings.TrimSpace(addr)
	if host == "" {
		host = "127.0.0.1:8091"
	}
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return strings.TrimRight(host, "/")
}

func readOrCreate(path string, gen func() (string, error)) (string, error) {
	value, _, err := readOrCreateFlag(path, gen)
	return value, err
}

func readOrCreateFlag(path string, gen func() (string, error)) (string, bool, error) {
	b, err := os.ReadFile(path)
	if err == nil && strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b)), false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return "", false, err
	}
	value, err := gen()
	if err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		return "", false, err
	}
	return value, true, nil
}

func randomToken(prefix string, n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}
