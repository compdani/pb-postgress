# Server database verification — 2026-11-07

This implementation is **incomplete**. The full Postgres suite still fails.
Counts below are top-level Go tests; subtests are excluded to avoid counting
both a parent and its children as independent tests.

`go build ./...` passes. `go.mod` and `go.sum` contain no modernc dependencies.
The branch `t3/bucket-installer-db-flow` was merged at commit `0bcb62dd`.
No changes were made directly in its source worktree, and no branch was pushed.
The merge includes that branch's existing UI changes; no manual UI edits were made.

The disposable Postgres cluster used Homebrew Postgres on 127.0.0.1:55439.
MySQL ran in a `mysql:8` container (server reports 8.4.13) on port 55339.
Each test app allocated/dropped a separate database.

## Verified behavior

- No DSN: database-dependent test apps skip with a setup message. Nil/idempotent
  cleanup is safe; setup errors fail at the helper instead of returning a nil app
  to callers that historically ignored the error.
- Postgres and MySQL: actual cmd/bucket child process uploads, record deletion,
  and feed-driven pruning pass, with both core-first and bucket-first table
  creation. Shared timestamp values are scanned successfully.
- Postgres and MySQL: hidden collection metadata updates and rename attempts
  are rejected before collection hooks; cross-scope cascade and unset pass.
- Postgres: collection-tagged JS cron jobs stop running after a scope change.
- MySQL: integration/file feed, record query/lookup, field validation and scope
  tests pass in the representative subset.
- tools/search passes against real Postgres, including date modifiers and
  geographic distance execution.

## Remaining work

- Complete runtime compatibility and legacy expectation updates in core/apis;
  the detailed failing test inventory follows below.
- Views need further dependency handling and introspection adaptation. Some
  tests still query historical SQLite catalog names.
- JSON paths in the generic log filter resolver still emit JSON_EXTRACT.
- Case-insensitive email/auth lookup still contains a legacy NOCASE query path.
- SQL API tests and archive backup tests need server-database expectations.
- Scope enforcement remains application-level. Untagged custom jobs are global;
  direct SQL is not protected by relation foreign keys/triggers.
- Remaining sidecar API sources are compatibility-only, as explained in
  DATABASE_MIGRATION.md. Their full removal requires coordinating App/JS/UI clients.

## Results

| Run | Pass | Fail | Skip |
| --- | ---: | ---: | ---: |
| No DSN, full suite | 651 | 0 | 314 |
| Postgres, full suite | 921 | 40 | 4 |
| MySQL, representative subset | 11 | 0 | 0 |

Including parent tests and all subtests, the raw event counts are:

- No DSN, full suite: 2193 pass, 0 fail, 1256 skip.
- Postgres, full suite: 4956 pass, 188 fail, 4 skip.
- MySQL, representative subset: 92 pass, 0 fail, 0 skip.

Commands used (local disposable credentials omitted):

```sh
go build ./...
go test -json -p 3 ./...
PB_TEST_DB_DSN=... go test -json -p 3 -parallel 4 ./...
PB_TEST_DB_DRIVER=mysql PB_TEST_DB_DSN=... go test -json ./core \
  -run '^(TestServerDatabaseFileChanges|TestBucketProcessFileLifecycle|TestScopeHiddenCollectionHooksAndCascades|TestFindCollectionByNameOrId|TestTextFieldValidateValue|TestRecordQueryWithDifferentCollectionValues|TestRecordQueryOne|TestRecordQueryAll|TestFindRecordById|TestFindRecordsByIds|TestFindAllRecords)$' -count=1
```

## Failing Postgres top-level tests

- `apis: TestDefaultRateLimitMiddleware`
- `apis: TestLogsStats`
- `apis: TestLogsList`
- `apis: TestCronsList`
- `core: TestRecordFieldResolverResolveCollectionFields`
- `apis: TestCollectionCreate`
- `apis: TestCollectionUpdate`
- `core: TestDryRunView`
- `core: TestCreateViewFieldsWithNumberOnlyInt`
- `core: TestRecordDelete`
- `core: TestCreateViewFields`
- `core: TestSaveView`
- `core: TestCreateViewFieldsWithDiscardedNestedTransaction`
- `core: TestDeleteView`
- `apis: TestBackupsRestore`
- `apis: TestBackupsDelete`
- `core: TestModelQuery`
- `core: TestRecordSave`
- `core: TestDeleteTable`
- `core: TestTableInfo`
- `core: TestSingleVsMultipleValuesNormalization`
- `core: TestCollectionQuery`
- `apis: TestBackupsList`
- `apis: TestBackupsCreate`
- `apis: TestBackupsDownload`
- `apis: TestSQLRun`
- `core: TestCollectionSaveViewWrapping`
- `core: TestCollectionSaveIndirectViewsUpdate`
- `core: TestCollectionDelete`
- `core: TestNotifyWatcher_CollectionsUpdate`
- `core: TestNotifyWatcher_SettingsUpdate`
- `core: TestCanAccessRecord`
- `core: TestRecordFieldResolverUpdateQuery`
- `apis: TestRecordCrudCreate`
- `core: TestFindAuthRecordByEmail`
- `apis: TestRecordCrudList`
- `core: TestExpandRecordsQuerySkipDuplicatedIds`
- `core: TestCollectionValidate`
- `apis: TestRecordAuthWithPassword`
- `apis: TestRecordAuthWithOAuth2`

Full JSON logs are retained locally under `/tmp/pb-migration-test/` as
`postgres-verified.json`, `noenv-verified.json`, and `mysql-verified.json`.
