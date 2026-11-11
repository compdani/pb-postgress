package bucket

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/pocketbase/pocketbase/tools/fileschema"
	"log"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	tableHosts   = "_fileHosts"
	tableChanges = "_fileChanges"
)

// Host is one row in _fileHosts.
type Host struct {
	ID           string
	URL          string
	Mode         string
	Secret       string
	RegisteredAt time.Time
}

// DB is a Postgres or MySQL connection used for the file-host registry and
// the _fileChanges feed. It never opens SQLite.
type DB struct {
	driver       string
	dsn          string
	sql          *sql.DB
	hostsTable   string
	changesTable string
	hostsRaw     string
	changesRaw   string
}

// OpenDB connects with database/sql. driver is "postgres" or "mysql".
func OpenDB(ctx context.Context, driver, dsn string) (*DB, error) {
	norm, err := normalizeDriver(driver)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("database dsn is required")
	}
	openDSN := dsn
	sqlDriver := "pgx"
	if norm == "mysql" {
		openDSN = normalizeMySQLDSN(dsn)
		sqlDriver = "mysql"
	}
	sqlDB, err := sql.Open(sqlDriver, openDSN)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("database ping failed: %w", err)
	}
	return &DB{driver: norm, dsn: dsn, sql: sqlDB}, nil
}

func (d *DB) Close() error {
	if d == nil || d.sql == nil {
		return nil
	}
	return d.sql.Close()
}

func normalizeDriver(driver string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "postgres", "postgresql", "pgx":
		return "postgres", nil
	case "mysql":
		return "mysql", nil
	case "sqlite", "sqlite3":
		return "", errors.New("sqlite is not supported; use postgres or mysql")
	case "":
		return "", errors.New("database driver is required")
	default:
		return "", fmt.Errorf("unsupported database driver %q", driver)
	}
}

func normalizeMySQLDSN(dsn string) string {
	if strings.Contains(dsn, "parseTime=") {
		return dsn
	}
	if strings.Contains(dsn, "?") {
		return dsn + "&parseTime=true"
	}
	return dsn + "?parseTime=true"
}

// EnsureSchema creates _fileHosts and _fileChanges when they are missing and
// adds columns the bucket needs when an older table is already there.
func (d *DB) EnsureSchema(ctx context.Context) error {
	hosts, err := d.ensureTable(ctx, tableHosts, d.createHostsSQL, hostColumns())
	if err != nil {
		return err
	}
	changes, err := d.ensureTable(ctx, tableChanges, d.createChangesSQL, changeColumns())
	if err != nil {
		return err
	}
	d.hostsRaw, d.hostsTable = hosts, quoteIdent(d.driver, hosts)
	d.changesRaw, d.changesTable = changes, quoteIdent(d.driver, changes)
	return nil
}

// Register makes this bucket the registered file host. _fileHosts holds one
// row: earlier bucket rows (e.g. from a wiped bucket directory) are replaced,
// but a full PgBase host or S3 registration is never overwritten.
func (d *DB) Register(ctx context.Context, h Host) error {
	if d.hostsTable == "" {
		return errors.New("file host registry is not initialized")
	}
	if h.Mode == "" {
		h.Mode = "bucket"
	}
	if h.RegisteredAt.IsZero() {
		h.RegisteredAt = time.Now().UTC()
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Serialize with other registrations and with PgBase's SetFileHost,
	// which takes the same lock, so the check and the replace are atomic.
	release := func() {}
	if d.driver == "mysql" {
		if release, err = lockFileHostsMySQL(ctx, tx); err != nil {
			return err
		}
		defer release() // error paths; runs before the deferred Rollback
	} else if _, err := tx.ExecContext(ctx, fmt.Sprintf(`LOCK TABLE %s IN EXCLUSIVE MODE`, d.hostsTable)); err != nil {
		return err
	}

	var other string
	// A locking (current) read: on MySQL the named lock is released before
	// commit, so a plain REPEATABLE READ snapshot could miss a full/s3 row
	// whose writer has not committed yet. FOR UPDATE waits for that commit.
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT mode FROM %s WHERE mode <> %s LIMIT 1 FOR UPDATE`, d.hostsTable, d.ph(1)), h.Mode).Scan(&other)
	if err == nil {
		return fmt.Errorf("a %q file host is already registered; remove it from _fileHosts before starting a bucket", other)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s`, d.hostsTable)); err != nil {
		return err
	}
	q := fmt.Sprintf(`INSERT INTO %s (id, url, mode, secret, registered_at) VALUES (%s, %s, %s, %s, %s)`,
		d.hostsTable, d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5))
	if _, err := tx.ExecContext(ctx, q, h.ID, h.URL, h.Mode, h.Secret, h.RegisteredAt); err != nil {
		return err
	}
	// Release on the still-open transaction; the next writer then blocks on
	// the inserted row until this commit.
	release()
	return tx.Commit()
}

// FetchAfter implements ChangeSource.
func (d *DB) FetchAfter(ctx context.Context, afterID int64, limit int) ([]Change, error) {
	if d.changesTable == "" {
		return nil, errors.New("file change log is not initialized")
	}
	if limit <= 0 {
		limit = 100
	}
	q := fmt.Sprintf(`SELECT id, op, collection, record_id, COALESCE(filename, ''), created_at
		FROM %s WHERE id > %s ORDER BY id ASC LIMIT %s`, d.changesTable, d.ph(1), d.ph(2))
	rows, err := d.sql.QueryContext(ctx, q, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Change
	for rows.Next() {
		var (
			c       Change
			created sql.NullTime
		)
		if err := rows.Scan(&c.ID, &c.Op, &c.Collection, &c.RecordID, &c.Filename, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = created.Time
		out = append(out, c)
	}
	if out == nil {
		out = []Change{}
	}
	return out, rows.Err()
}

// Notify implements ChangeSource. Postgres LISTENs on pb_file_changes and
// reconnects after errors. MySQL returns nil so the watcher polls.
func (d *DB) Notify(ctx context.Context) <-chan struct{} {
	if d.driver != "postgres" {
		return nil
	}
	ch := make(chan struct{}, 1)
	go func() {
		defer close(ch)
		backoff := time.Second
		for {
			if ctx.Err() != nil {
				return
			}
			err := d.listenOnce(ctx, ch)
			if ctx.Err() != nil {
				return
			}
			log.Printf("bucket: postgres listen ended: %v", err)
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
		}
	}()
	return ch
}

func (d *DB) listenOnce(ctx context.Context, ch chan<- struct{}) error {
	conn, err := pgx.Connect(ctx, d.dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "LISTEN pb_file_changes"); err != nil {
		return err
	}
	for {
		waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := conn.WaitForNotification(waitCtx)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			return err
		}
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

type columnDef struct {
	name string
	pg   string
	my   string
}

func hostColumns() []columnDef {
	return []columnDef{
		{name: "url", pg: "TEXT NOT NULL DEFAULT ''", my: "TEXT NULL"},
		{name: "mode", pg: "VARCHAR(32) NOT NULL DEFAULT ''", my: "VARCHAR(32) NOT NULL DEFAULT ''"},
		{name: "secret", pg: "VARCHAR(512) NOT NULL DEFAULT ''", my: "VARCHAR(512) NOT NULL DEFAULT ''"},
		{name: "registered_at", pg: "TIMESTAMPTZ NOT NULL DEFAULT NOW()", my: "DATETIME(6) NULL"},
	}
}

func changeColumns() []columnDef {
	return []columnDef{
		{name: "op", pg: "VARCHAR(16) NOT NULL DEFAULT ''", my: "VARCHAR(16) NOT NULL DEFAULT ''"},
		{name: "collection", pg: "TEXT NOT NULL DEFAULT ''", my: "VARCHAR(255) NOT NULL DEFAULT ''"},
		{name: "record_id", pg: "TEXT NOT NULL DEFAULT ''", my: "VARCHAR(255) NOT NULL DEFAULT ''"},
		{name: "filename", pg: "TEXT NOT NULL DEFAULT ''", my: "VARCHAR(1024) NOT NULL DEFAULT ''"},
		{name: "created_at", pg: "TIMESTAMPTZ NOT NULL DEFAULT NOW()", my: "DATETIME(6) NULL"},
	}
}

func (d *DB) createHostsSQL(quoted string) string   { return fileschema.Hosts(d.driver, quoted) }
func (d *DB) createChangesSQL(quoted string) string { return fileschema.Changes(d.driver, quoted) }

func (d *DB) ensureTable(ctx context.Context, canonical string, create func(string) string, cols []columnDef) (string, error) {
	name, err := d.findTable(ctx, canonical)
	if err != nil {
		return "", err
	}
	if name == "" {
		quoted := quoteIdent(d.driver, canonical)
		if _, err := d.sql.ExecContext(ctx, create(quoted)); err != nil {
			return "", err
		}
		name = canonical
	}
	if err := d.ensureColumns(ctx, name, cols); err != nil {
		return "", err
	}
	return name, nil
}

func (d *DB) ensureColumns(ctx context.Context, raw string, cols []columnDef) error {
	quoted := quoteIdent(d.driver, raw)
	for _, col := range cols {
		ok, err := d.columnExists(ctx, raw, col.name)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		ddl := col.pg
		if d.driver == "mysql" {
			ddl = col.my
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", quoted, quoteIdent(d.driver, col.name), ddl)
		if _, err := d.sql.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("add column %s.%s: %w", raw, col.name, err)
		}
	}
	return nil
}

func (d *DB) findTable(ctx context.Context, want string) (string, error) {
	var (
		q    string
		args []any
	)
	if d.driver == "mysql" {
		q = `SELECT table_name FROM information_schema.tables
			WHERE table_schema = DATABASE() AND lower(table_name) = lower(?)
			ORDER BY CASE WHEN table_name = ? THEN 0 ELSE 1 END
			LIMIT 1`
		args = []any{want, want}
	} else {
		q = `SELECT table_name FROM information_schema.tables
			WHERE table_schema = current_schema() AND lower(table_name) = lower($1)
			ORDER BY CASE WHEN table_name = $1 THEN 0 ELSE 1 END
			LIMIT 1`
		args = []any{want}
	}
	var name string
	err := d.sql.QueryRowContext(ctx, q, args...).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

func (d *DB) columnExists(ctx context.Context, table, column string) (bool, error) {
	var q string
	var args []any
	if d.driver == "mysql" {
		q = `SELECT 1 FROM information_schema.columns
			WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ? LIMIT 1`
		args = []any{table, column}
	} else {
		q = `SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2 LIMIT 1`
		args = []any{table, column}
	}
	var one int
	err := d.sql.QueryRowContext(ctx, q, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (d *DB) ph(n int) string {
	if d.driver == "mysql" {
		return "?"
	}
	return fmt.Sprintf("$%d", n)
}

func quoteIdent(driver, name string) string {
	if driver == "mysql" {
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// FileHostsLockName prefixes the MySQL named lock that serializes writers of
// _fileHosts (the bucket's Register and PgBase's SetFileHost). Named locks
// are server-wide, so the current database name is appended; both sides build
// the name the same way in SQL.
const FileHostsLockName = "pb_file_hosts:"

// lockFileHostsMySQL takes the named lock on the transaction's connection.
// A row lock cannot do this: on an empty table MySQL's gap locks are shared,
// so two first registrations deadlock instead of waiting. The lock belongs to
// the connection, so release must run while the transaction is still open
// (before Commit or Rollback), or the pooled connection keeps it.
func lockFileHostsMySQL(ctx context.Context, tx *sql.Tx) (func(), error) {
	var got sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT GET_LOCK(CONCAT(?, DATABASE()), 30)", FileHostsLockName).Scan(&got); err != nil {
		return nil, err
	}
	if !got.Valid || got.Int64 != 1 {
		return nil, errors.New("timed out waiting for the _fileHosts lock")
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_, _ = tx.ExecContext(context.Background(), "SELECT RELEASE_LOCK(CONCAT(?, DATABASE()))", FileHostsLockName)
		})
	}, nil
}
