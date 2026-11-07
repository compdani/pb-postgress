package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/pocketbase/pocketbase/tools/bucket"
)

func main() {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := bucket.Run(ctx, cfg); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseConfig(args []string) (bucket.Config, error) {
	fs := flag.NewFlagSet("bucket", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", env("PB_BUCKET_DIR", "pb_bucket"), "filesystem directory for object bytes and metadata")
	httpAddr := fs.String("http", env("PB_BUCKET_HTTP", "127.0.0.1:8091"), "TCP address for the HTTP server")
	apiKey := fs.String("apiKey", env("PB_BUCKET_API_KEY", ""), "bearer secret; generated and printed once when empty")
	publicURL := fs.String("publicURL", env("PB_BUCKET_PUBLIC_URL", ""), "URL registered in _fileHosts (default http://<listen>)")
	dbDriver := fs.String("dbDriver", env("PB_DB_DRIVER", ""), "database driver: postgres or mysql")
	dbDSN := fs.String("dbDSN", env("PB_DB_DSN", ""), "database DSN for the shared instance database")
	maxBytes := fs.Int64("maxBytes", envInt64("PB_BUCKET_MAX_BYTES", 100<<20), "maximum upload size in bytes")
	cors := fs.String("cors", env("PB_BUCKET_CORS", "*"), "comma-separated CORS origins, or *")
	if err := fs.Parse(args); err != nil {
		return bucket.Config{}, err
	}
	return bucket.Config{
		Dir:         *dir,
		HTTPAddr:    *httpAddr,
		APIKey:      *apiKey,
		PublicURL:   *publicURL,
		DBDriver:    *dbDriver,
		DBDSN:       *dbDSN,
		MaxBytes:    *maxBytes,
		CORSOrigins: splitCSV(*cors),
	}, nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
