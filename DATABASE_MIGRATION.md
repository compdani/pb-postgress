# Server database transition

Configure one PostgreSQL or MySQL database with `--dbDriver` and `--dbDSN`,
`PB_DB_DRIVER` and `PB_DB_DSN`, or `pocketbase.Config.DBDriver` / `DBDSN`.
Main records, metadata, migrations, logs, and auxiliary operations use the same
configured database. SQLite is no longer opened and modernc dependencies are
removed. Existing SQLite datasets require an external migration/export process.
Database archives are unsupported: use `pg_dump`/`pg_restore` or
`mysqldump`/`mysql`, and back up uploaded files separately.

Without database configuration, `serve` exposes an installer API:

* `GET /api/installer/status`: dbConnected, installed, fileServer, fileMode.
* `POST /api/installer/test-db`: `{driver, dsn}`.
* `POST /api/installer/connect`: tests, persists owner-only `pb_data/db.json`,
  then bootstraps the database. The bootstrap also stores this instance's id.
* `POST /api/installer/test-s3`: `{endpoint,bucket,region,accessKey,secret,forcePathStyle}`;
  uploads, reads, verifies, and deletes a unique probe object.
* `POST /api/installer/complete`: `{email,password,fileMode,isFileServer,fileServerUrl,s3,scope}`.
  fileMode is local, fileServer, or s3. Modes cannot be changed without an
  explicit file migration. Remote mode requires a previously registered host.

`_fileHosts(id,url,mode,secret,registered_at)` contains the registered instance
id, HTTP URL, full/bucket/s3 mode, and shared secret. Secret is excluded from
API status serialization. Remote object storage uses authenticated HTTP
PUT/GET/HEAD/DELETE at `/files/{collectionId}/{recordId}/{filename}`; GET
`/files/?prefix=...` returns a JSON array of blob list objects. Full instances
also implement this authenticated API. Thumbnails and copy use the same blob
backend. Uploads finish before record commit. File deletions are retried from
`_fileDeleteQueue`; `_fileChanges` is retained as an immutable reconciliation
feed. PostgreSQL emits transactional `pb_file_changes` notifications. MySQL
consumers poll `_fileChanges.id`. S3 uses the existing backend globally.

`_instances` stores instance identity and all/allow/deny collection scope.
Superusers can GET/PUT `/api/instance/scope` with `{mode,collections:[]}` and
GET `/api/instances`. System and auth collections always remain visible;
unknown collections and required visible-to-hidden relations are rejected.
The implementation filters common API collection lookups, collection lists,
realtime publication/subscription, filters, and relation expansion. Raw SQL is
disabled for scoped instances. Hidden collection imports are rejected and
schema synchronization/deletion leaves hidden collections untouched.

## Validation and remaining work

The PostgreSQL integration test requires an **empty disposable database**:

```
PB_TEST_DB_DRIVER=postgres PB_TEST_DB_DSN='postgres://localhost/pb_test?sslmode=disable' \
  go test ./core -run '^TestServerDatabaseFileChanges$' -count=1
```

The same test accepts mysql and a MySQL DSN. It writes schema and data; never
point it at production. It tests bootstrap, rollback of file events, replace,
delete, host registration, immutable file mode, and two instances sharing a
DB with different scopes. Remote HTTP and S3 probes have httptest coverage.

This change is incomplete and should not be treated as production ready:

* The fixture harness runs against disposable server databases. The full
  PostgreSQL suite and the no-DSN suite pass; see DATABASE_TEST_RESULTS.md for
  exact counts and the remaining opt-in integration skips.
* MySQL 8 has been verified against a live server in the expanded subset.
  Arbitrary partial indexes are rejected; the built-in optional email unique
  index is translated to a NULLIF functional index (MySQL 8 required).
  MySQL DDL implicitly commits and cannot provide PostgreSQL's transactional
  schema migration guarantees. Indexing unrestricted TEXT fields needs review.
* Collection hooks and collection-tagged cron jobs enforce instance scope.
  Untagged custom jobs remain global. Cross-scope cascades use application-level integrity
  implementation; database foreign keys/triggers have not been implemented.
  Scope is therefore not yet a complete isolation/security boundary.
* Legacy sidecar PostgreSQL introspection/import APIs remain in source, with
  sidecar initialization disabled. Full retirement/adaptation is outstanding.
* User-authored SQL, view SQL, geo/date filter compatibility, large MySQL text
  fields, and existing-schema upgrades need broader integration coverage.
* The pre-database server uses a basic HTTP listener; full Serve lifecycle,
  TLS, cancellation, startup cron hooks, and setup across existing multi-instance
  deployments need further integration. Use a trusted local setup network.
* UI and standalone bucket implementation were intentionally not modified.

## Follow-up implementation (2026-11-07)

The harness now takes `testing.TB` (`tests.NewTestApp(t)`), skips database
callers when `PB_TEST_DB_DSN` is absent, allocates a uniquely named database
per app, and closes/drops it during idempotent `Cleanup`. The configured user
needs CREATE/DROP DATABASE permission. `PB_TEST_DB_DRIVER` defaults to postgres;
set it to mysql for a MySQL DSN. Connection options are preserved. Both main
and auxiliary state belong to that isolated database. Fixtures are seeded
from `tests/data/fixtures.json`; `tests/data/export_fixtures.py` documents the
one-time export of the historical data. Go tests do not open SQLite files.

`tools/fileschema` is now the canonical DDL for core and bucket. Both use real
TIMESTAMPTZ/DATETIME(6) columns and time.Time values, matching IDs, modes and
secret sizes, and no restrictive CHECKs. The merged bucket branch consumes
that same definition. `TestBucketProcessFileLifecycle` builds and runs the
actual cmd/bucket process and verifies both schema creation orders, core
uploads and deletes with a remote mode=bucket host, timestamp scanning, and
feed-driven deletion of bytes reintroduced after the synchronous deletion.
The core calls this remote file mode `fileServer`.

Hidden collection metadata writes are rejected before collection hooks,
including attempts to rename a hidden collection. Collection hook adapters
skip hidden collections. JS `cronAdd` accepts optional collection names after
the handler; these are checked on every execution so scope changes apply to
already registered jobs. Untagged custom cron jobs have no collection identity
and remain global. Application-level relation cascades deliberately query all
collections; tests verify deletion and unsetting references in hidden collections.
Direct SQL remains outside these application-level integrity guarantees.

Legacy sidecar transaction implementations have been replaced by compatibility
aliases to the instance transaction. Other sidecar metadata/introspection types
and exported APIs are temporarily retained for source compatibility with App,
JS bindings and the independently maintained collection import UI. Bootstrap
never initializes a sidecar connection; those legacy import/introspection paths
cannot provide a working split-database configuration. Removing these exported
APIs and their UI clients together remains follow-up work; retaining their
source must not be interpreted as supported sidecar operation.

MySQL live testing fixed TEXT defaults in bootstrap DDL, optional-index creation,
JSON-array uniqueness in the fixture, time scanning, and null-safe comparisons.
Encrypted settings are stored in a text column, since ciphertext is not JSON.

## Runtime compatibility follow-up (2026-11-07)

Generic log filters and request-array filters now use PostgreSQL/MySQL JSON
helpers. PostgreSQL relation and multi-match joins include explicit conditions;
numeric JSON comparisons and virtual create-rule rows use explicit SQL types.
Case-insensitive identity lookups use the same LOWER expression as their unique
index. Case-insensitive optional-email MySQL indexes and lookups use
NULLIF(LOWER(email), '').
New MySQL text columns use binary collation so case-sensitive unique indexes
retain their configured meaning. Existing-schema collation upgrades still need
an explicit migration.

PostgreSQL view replacement and record-table changes detach/recreate dependent
views transactionally. Incompatible replacements roll back. View introspection
uses information_schema, and nested view DDL failures use savepoints so callers
can discard an error without poisoning the parent transaction. Collection API
updates bind fresh metadata rather than mutating the shared collection cache.
View records remain read-only even when PostgreSQL considers a view updatable.

CreateBackup and RestoreBackup return ErrServerDatabaseBackupUnsupported for
both drivers. Authorized API requests report an explicit unsupported error;
restore no longer returns success before a background failure. Listing,
uploading, downloading and deleting archive files remain supported filesystem
operations. Native server dumps and uploaded-file backups are managed externally.

The SQL API executes multiple statements individually in a transaction and
returns the final statement's result. PostgreSQL DDL rollback is verified;
MySQL's implicit DDL commits remain an engine limitation. Notify files remain
present until cleanup so macOS cannot coalesce a create/delete pair away.
