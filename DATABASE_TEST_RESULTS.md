# Server database verification — 2026-11-07

The full suite passes against real PostgreSQL. The no-DSN suite and expanded
MySQL subset also pass. Counts below are top-level Go tests; subtests are
excluded so parent tests and children are not counted as independent tests.

| Run | Pass | Fail | Skip |
| --- | ---: | ---: | ---: |
| PostgreSQL, full suite | 964 | 0 | 4 |
| No DSN, full suite | 652 | 0 | 316 |
| MySQL, expanded subset | 21 | 0 | 0 |

Raw test events, including parents and subtests:

- PostgreSQL: 5164 pass, 0 fail, 4 skip.
- No DSN: 2196 pass, 0 fail, 1259 skip.
- MySQL: 183 pass, 0 fail, 0 skip.

`go build ./...` and `git diff --check` pass. No changes were made to `ui/`
or `tools/bucket/`. The existing merged changes in those directories were
left untouched. Work was committed on `t3/deprecate-sqlite-add-mysql`; no push.

## Environment and commands

PostgreSQL 14 used the disposable local cluster on 127.0.0.1:55439. MySQL 8
used a disposable container on 127.0.0.1:55339. The test harness creates and
drops a separate database for each test app. The DSN user needs permission to
create/drop databases. No production databases were used.

```sh
go build ./...
env -u PB_TEST_DB_DSN -u PB_TEST_DB_DRIVER go test -json -p 3 ./...
PB_TEST_DB_DSN=... go test -json -p 3 -parallel 4 ./...
PB_TEST_DB_DRIVER=mysql PB_TEST_DB_DSN=... go test -json -parallel 4 ./core ./apis \
  -run '^(TestServerDatabaseFileChanges|TestBucketProcessFileLifecycle|TestScopeHiddenCollectionHooksAndCascades|TestFindCollectionByNameOrId|TestTextFieldValidateValue|TestRecordQueryWithDifferentCollectionValues|TestRecordQueryOne|TestRecordQueryAll|TestFindRecordById|TestFindRecordsByIds|TestFindAllRecords|TestDeleteView|TestSaveView|TestFindAuthRecordByEmail|TestLogsList|TestLogsStats|TestRecordAuthWithPassword|TestRecordAuthWithOAuth2|TestServerDatabaseArchiveUnsupported|TestRecordFilterRequestEachExecution)$' \
  -count=1
```

Final JSON logs are retained locally in `/tmp/pb-migration-test/`:

- `implementation-postgres-final.json`
- `implementation-no-dsn-final.json`
- `implementation-mysql-final-report.json`

## Verified runtime fixes

- Log filters and request-array filters use dialect JSON helpers. Numeric
  PostgreSQL JSON comparisons, typed create-rule rows, and relation/multi-match
  joins execute on the server.
- Auth email/password/OAuth lookups use dialect-correct, index-compatible
  expressions. Case-sensitive MySQL fields use binary collation; case-insensitive
  indexes and lookups agree on LOWER and optional-email NULLIF expressions.
- PostgreSQL view introspection, transactional dependency preservation and
  rollback, discarded nested DDL errors, and numeric/text id wrapping pass.
  The dedicated dependency test verifies incompatible replacement leaves the
  original dependent view executable.
- Collection create/update validation and schema synchronization pass. API
  request binding no longer mutates shared cached metadata before schema sync.
  Indexes are dropped from actual table metadata; conflicting names are checked
  before writes. Record deletion/cascades and read-only view record saves pass.
- Archive create/restore explicitly return ErrServerDatabaseBackupUnsupported.
  API restore reports this synchronously. Tests cover the core error for both
  drivers, the authorized API error, authentication, active-file protection,
  archive list/download/delete, and missing files. Use native server dump tools
  and back up uploaded files separately.
- Multiple SQL API statements execute individually in a transaction; rollback
  and final-statement results pass on PostgreSQL. Notify files persist until
  cleanup so macOS observes writes. Background file cleanup returns an error
  when the database has closed rather than panicking.
- Legacy SQLite SQL/catalog expectations now verify PostgreSQL SQL or resulting
  schema/behavior. No failing tests were disabled to obtain these results.

## Remaining failures and skips

No failing tests remain in the three reported runs. PostgreSQL's four skips
are existing opt-in integration tests with separate environment configuration:

- `core.TestMigrateCollectionToPostgresIntegration`
- `core.TestMigrateCollectionToPostgresDryRunIntegration`
- `tools/bucket.TestPostgresFileFeed`
- `tools/bucket.TestMySQLFileFeed`

No-DSN database tests intentionally skip with a setup message. The MySQL run
is an expanded subset, not the full suite. MySQL DDL implicit commits,
existing-schema collation upgrades, complete removal of legacy sidecar APIs,
and database-enforced scope isolation remain outside this verification;
see DATABASE_MIGRATION.md.
