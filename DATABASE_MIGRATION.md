# Server database transition

Configure one PostgreSQL or MySQL database with `--dbDriver` and `--dbDSN`,
`PB_DB_DRIVER` and `PB_DB_DSN`, or `pocketbase.Config.DBDriver` / `DBDSN`.
Main records, metadata, migrations, logs, and auxiliary operations use the same
configured database. SQLite is no longer opened and modernc dependencies are
removed (the SQLite driver is used only to read `data.db` from imported
PocketBase backups). See "Backups" below for moving data in and out.

Without database configuration, `serve` exposes only the installer API and the
dashboard, and prints a setup URL (`/_/#/pbinstall/<token>`). When setup
completes, or `connect` reaches a database that is already set up, the
installer listener stops and the same process starts the full server
(`apis.Serve`, with cron and hooks). The token is a
random per-process secret. Every `POST` below requires it in the
`X-Installer-Token` header, and the routes return 403 once a superuser other
than the temporary installer account exists. The same token is printed when a
configured database has no superuser yet.

* `GET /api/installer/status`: dbConnected, installed. With the token, also
  fileServer and fileMode.
* `POST /api/installer/test-db`: `{driver, dsn}`.
* `POST /api/installer/connect`: tests, persists owner-only `pb_data/db.json`,
  then bootstraps the database. The bootstrap also stores this instance's id.
* `POST /api/installer/test-s3`: `{endpoint,bucket,region,accessKey,secret,forcePathStyle}`;
  uploads, reads, verifies, and deletes a unique probe object.
* `POST /api/installer/complete`: `{email,password,fileMode,isFileServer,fileServerUrl,s3,scope}`.
  fileMode is local, fileServer, or s3. Modes cannot be changed without an
  explicit file migration. Remote mode requires a previously registered host.

`_fileHosts(id,url,mode,secret,registered_at)` contains the registered instance
id, HTTP URL, full/bucket/s3 mode, and shared secret. It holds at most one row;
more than one is an error. A bucket replaces earlier bucket rows on startup but
refuses to start while a full or s3 host is registered. Both writers lock
the table around the check and replace (PostgreSQL `LOCK TABLE`; MySQL the named lock
`pb_file_hosts:<database>` via `GET_LOCK`). A bucket host is used
only in fileServer mode; a full host serves the other instances. Secret is
excluded from API status serialization. Remote object storage uses
authenticated HTTP PUT/GET/HEAD/DELETE at
`/files/{collectionId}/{recordId}/{filename...}` (thumbnails nest one level,
`thumbs_<file>/<size>_<file>`). GET `/files/?prefix=` lists objects: a bucket
returns `{items,truncated}` and pages with `&after=<last key>`; a full instance
returns a JSON array of blob list objects. The client accepts both. Thumbnails and copy use the same blob
backend. Uploads finish before record commit. File deletions are retried from
`_fileDeleteQueue`; `_fileChanges` is retained as an immutable reconciliation
feed. PostgreSQL emits transactional `pb_file_changes` notifications. MySQL
consumers poll `_fileChanges.id`. S3 uses the existing backend globally.

`_instances` stores instance identity and all/allow/deny collection scope.
Superusers can GET/PUT `/api/instance/scope` with `{mode,collections:[]}` and
GET `/api/instances`. System collections always remain visible (user-created
auth collections can be hidden, and then cannot authenticate on that instance);
unknown collections and required visible-to-hidden relations are rejected.
The implementation filters common API collection lookups, collection lists,
realtime publication/subscription, filters, and relation expansion. Raw SQL is
disabled for scoped instances. Hidden collection imports are rejected and
schema synchronization/deletion leaves hidden collections untouched.
Scope is not a security boundary: it is enforced by each instance when it reads
and delivers data, while the collection cache, the cache versions and (with the
realtime bus enabled) realtime events are shared by all instances. A collection rename or delete
rewrites the scopes of all instances in the same transaction.

### Shared cache versions

Instances sharing a database keep their cached collections (API rules, schema,
auth token secrets) and settings in line through `_cacheVersions`. A
transaction that changes collections or settings bumps the matching version
row once, as its last statements, so a rollback never bumps it (on MySQL DDL
commits implicitly, so a failed transaction may still bump). PostgreSQL also
sends a `pb_cache` NOTIFY; every instance polls the versions anyway
(`core.CacheSyncInterval`, 2s), which is the only mechanism on MySQL or behind
a pooler that cannot LISTEN. A slower reconcile (`core.CacheSyncReconcileInterval`,
45s) repairs a lost bump. A reloaded cache is normally not published while a
local collection write is running; if one runs longer than `core.CacheSyncMaxDeferral`
(30s) the publication is forced anyway.
When another instance changes the token secret of an auth collection, or
deletes it, the realtime clients authenticated with it are unset here too.
Raw SQL that touches `_collections` or `_params` bumps the versions as well.

### Realtime bus (opt-in)

With `PB_REALTIME_BUS=1` (or `BaseAppConfig.RealtimeBus`) the instances publish the
record events of their writes to each other's realtime clients. Without it a
client only receives the events of writes made on the instance it is connected to
(and it still needs a sticky connection for `POST /api/realtime`).

A transaction that writes records inserts thin events (collection id, record id,
action) into `_realtimeEvents` as its last statements, numbered with a per-process
sequence. A process has one write connection, so its sequence is in commit order and
no cluster-wide lock is needed. Every other instance polls the events of each live
process (`_realtimeBoots`, 250ms by default, `core.RealtimeBusInterval`), reloads
the record and broadcasts it to its own clients with its own API rules and scope. The
writer doesn't read its own events. There is deliberately no NOTIFY: in a
transaction it makes postgres serialize all commits on one lock.

The bus is best effort. A failure to publish (or more than 1000 non-auth events in
one transaction) never fails the write: it bumps the shared `realtimeGap` version,
and every instance then re-validates the auth of its realtime clients. Events about
auth records are never dropped by the cap. Independently of the bus, every instance
sweeps its authenticated realtime clients every 30s (`PB_REALTIME_AUTH_SWEEP`, seconds,
min 5; this sweep runs even when the bus itself is off): a client whose record
was deleted, whose token key changed or whose auth collection is no longer visible
is unset, so a revoked client normally keeps receiving events for about that long
(the time the sweep's queries take and scheduling come on top; REST requests are
checked against the database on every request). If the sweep can't read the records, delivery to those clients is suspended
and their auth is dropped after three failed sweeps.

Measured with four instances creating records as fast as they can on one local
PostgreSQL (`PB_TEST_BENCH_BUS=1 go test ./core -run TestRealtimeBusCommitThroughput -v`):
about 55% of the throughput without the bus (the extra insert and savepoint per
transaction).

Record deletes made on another instance reach a client only when that can be decided
without the record, which no longer exists (its API rules and subscription filters can't
be evaluated, and the rows they reference may be gone too): the rule of the subscribed
topic is empty (public) and the subscription has no filter, or the client is a superuser
and the subscription has no filter. Such an event carries only `id`, `collectionId` and
`collectionName`, never the record's data. A client whose rule is an expression (e.g.
`@request.auth.id != ''`; superusers excepted) or whose subscription uses a filter
doesn't get the delete of a record removed on another instance; it learns about it
when it refetches the record or refreshes its list. While any `OnRecordEnrich` hook is
registered (a hook can veto an event for a client, and can't run for a deleted
record) remote deletes aren't delivered at all, unless the application sets
`apis.RealtimeRemoteDeletesIgnoreEnrichHooks = true` (once at startup, before serving). A delete handled by the client's own instance is delivered as before. The
auth effect of a deleted auth record is applied on every instance in any case.

Known limits (accepted for the first release):

* Settings are updated in place and read without a lock, as before. Reloads
  outside a transaction are serialized (the lock is reentrant, so a reload hook
  may save settings), hooks included. A reload inside a transaction does not
  take that lock (it owns the single write connection), so it can overlap
  the hooks of a concurrent reload.
* A settings reload hook must not wait for another goroutine that reloads the
  settings: the reload lock is held for the whole reload.
* Editing the scope of an instance (`PUT /api/instance/scope`) at the very moment another
  instance renames a collection can write the old name back into that scope: the scope
  edit isn't serialized with renames. Scope is not a security boundary.
* A collections version bump or its savepoint that fails aborts the collection
  transaction. On MySQL, DDL commits implicitly, so the schema change itself
  cannot be undone.
* A settings change committed on one instance reaches the others within one
  poll (2s by default), and within 45s if its version bump was lost.

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
  A text field without a max is `VARCHAR(255)` on MySQL, so it accepts at
  most 255 characters there (5000 on PostgreSQL). Set a max above 255 to get
  a `TEXT` column. URL fields are also `VARCHAR(255)` on MySQL and validate
  at 255 characters.
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

## Backups

Backups are on demand only: the backup cron and its `backups.cron` /
`backups.cronMaxKeep` settings are removed. Regular protection is the job of
the database server (PostgreSQL base backups and PITR, MySQL binlogs,
managed snapshots). `backups.s3` still selects where archives are stored.

`CreateBackup` (`POST /api/backups`) writes a **portable backup**, a zip
that restores on PostgreSQL and MySQL alike:

```
manifest.json      {"format":"pocketbase-portable","formatVersion":1,
                    "created","sourceDriver","files",collections:[{id,name,rows}]}
collections.json   collections in the import format
params.json        [{id,value}]  _params rows as stored (encrypted settings stay
                   encrypted; restoring needs the same PB_ENCRYPTION key)
migrations.json    [{file,applied}]
records/<collectionId>.jsonl   one JSON object per record: column -> string|null
storage/<collectionId>/<recordId>/<file>   record files, no thumbnails
```

Record values are the text a database returns for each column, which is
the form PgBase already loads records from, so no type is tied to one
driver. Every table is read in one REPEATABLE READ, read-only transaction.
Files are included only for collections stored in this instance's
`pb_data/storage`; S3 and file-server files are not copied.

`RestoreBackup` (`POST /api/backups/{key}/restore`) accepts that format or
a **standard PocketBase backup**: a `pb_data` zip containing `data.db` (at the
root or one folder down) and `storage/`. Supported PocketBase versions:
**v0.23.0 to v0.41.x** (`core.PocketBaseImportVersions`). Older backups have
the pre-v0.23 `_collections` layout and are rejected; upgrade them with
PocketBase v0.23+ first. Field types or options unknown to this build are
rejected too. `auxiliary.db` (logs) is not imported.

Restore runs synchronously and in this order:

1. Read and check everything: collections, settings (decrypted with the
   current key), every record, and on MySQL the indexes and `VARCHAR(255)`
   lengths. Nothing changes if this fails.
2. Copy files into the configured file storage (local, S3 or file server),
   which also migrates PocketBase files into remote storage.
3. Replace the collections (missing ones are deleted), records (raw inserts,
   no hooks), settings, and add migration entries the database lacks.
   PostgreSQL does this in one transaction. MySQL commits schema changes
   implicitly, so only the record step is atomic there.
4. Reload the cached collections and settings.

Kept from the target: file mode, `_fileHosts`, `_instances` scopes, and the
S3 and backups settings. Superusers come from the backup, so current sessions
end. Files of replaced records are not deleted. Other instances sharing the
database must be restarted to reload their caches. Backup and restore require
an instance whose scope is `all`. View queries written for SQLite may need
rewriting; see below.

### View queries in PocketBase imports

PocketBase view queries are SQLite SQL. During a PocketBase import only,
each view query is translated token by token (`core/view_translate.go`):
quoted identifiers, `IFNULL`, `IIF`, `TOTAL`, `CAST` types,
`GROUP_CONCAT`, `json_extract` and `json_array_length`, `date`/`time`/
`datetime`/`strftime` without modifiers (`%Y %m %d %H %M %S %j`), `==`,
`INSTR` and `LENGTH`, `ROUND(x, n)` on PostgreSQL, `||` as `CONCAT` on MySQL,
and case-insensitive `LIKE` (`ILIKE` on PostgreSQL, `LOWER(a) LIKE LOWER(b)`
on MySQL, whose text columns use a binary collation).

Views are then created one at a time, in passes so a view can follow the
views it selects from. A view the database still rejects is undone
(PostgreSQL savepoint; on MySQL its view and collection row are removed)
and skipped; everything else is restored. The restore response lists
`translatedViews` and `skippedViews` with the original and translated
queries, the dashboard shows them, and skipped views are also logged.
Recreate skipped views by hand. Not translated: date modifiers (`'+1 day'`),
other `strftime` codes, `json_each`, `unixepoch`, `LIMIT a, b`, and
non-aggregated columns next to aggregates (which SQLite allows). Results of a
translated view can differ slightly (for example integer division or text
sorting), so check them. Portable backups restore views unchanged.

The SQL API executes multiple statements individually in a transaction and
returns the final statement's result. PostgreSQL DDL rollback is verified;
MySQL's implicit DDL commits remain an engine limitation. Notify files remain
present until cleanup so macOS cannot coalesce a create/delete pair away.

## App migrations review

System migrations always run at bootstrap. App migrations (`pb_migrations`,
Go `core.AppMigrations`) run on their own only on a fresh install, while no
superuser exists (`serve`, or the installer's `complete` step). Afterwards new
ones stay pending until a superuser reviews them in the dashboard popup, or
`migrate up` is run.

* `GET /api/migrations/pending`: `{files}`; nothing is executed.
* `POST /api/migrations/preview`: on PostgreSQL the pending migrations run in
  a transaction that is rolled back, and each one's collection changes
  (created, updated, deleted, with field/rule/index details) are returned.
  Record and raw SQL changes are not listed. MySQL cannot roll back schema
  changes, so it only lists the files (`preview:false`).
* `POST /api/migrations/apply`: `{ignore:{"<file>":["<collection id or name>"]}}`.
  Writes to an ignored collection (create, update, delete through the app) are
  skipped while that migration runs. If a migration then fails, for example
  because it depends on a skipped change, everything is rolled back.

App migration history is kept per migration group. Rows of the shared group
are stored as before; an instance set up as "new" in the installer gets its
own group (`_params` row `migrationGroup:<instanceId>`) and its rows are stored
as `<group>/<file>`. A replica uses the group of the instance it copies. A
restore writes the history of the instance that made the backup into the
restoring instance's group, so only migrations newer than the backup are
pending afterwards: restore first, then review.
