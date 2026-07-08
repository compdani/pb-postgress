# API_DOCS — Go Framework Reference for AI Agents

> **Audience:** AI agents and automated tooling building on the **pb-postgress** fork.
>
> **Scope:** Go framework API only — hooks, routes, `core.App`, `*core.BaseApp`, PostgreSQL helpers.
> For REST endpoints see [README.md](README.md). For standard PocketBase behavior see [upstream Go docs](https://pocketbase.io/docs/go-overview/).

## Identity

**pb-postgress** is a drop-in replacement for upstream `[github.com/pocketbase/pocketbase](https://github.com/pocketbase/pocketbase)` with optional PostgreSQL-backed collection records.

- Imports stay unchanged: `github.com/pocketbase/pocketbase`, `.../core`, `.../apis`, etc.
- PostgreSQL is **opt-in** via `PB_POSTGRES_URL`. Without it, behavior matches standard PocketBase (SQLite only).
- Public API (`pocketbase.New()`, `core.App`, hooks, routes) is unchanged.



### Dependency swap

```go
// go.mod
module myapp

go 1.25

require github.com/pocketbase/pocketbase v0.0.0

replace github.com/pocketbase/pocketbase => github.com/compdani/pb-postgress v0.0.0
```

---



## Architecture

Read this before writing any code. Agents often assume a single database — this fork uses two.


| Layer             | Storage                      | What lives there                                                                         |
| ----------------- | ---------------------------- | ---------------------------------------------------------------------------------------- |
| System            | SQLite (`pb_data`)           | Settings, superusers, collection metadata cache, non-PG collections, logs, file metadata |
| Records (PG mode) | PostgreSQL                   | Records for `postgresRecords` and `external` collections                                 |
| PG metadata       | PostgreSQL                   | `_pb_collections`, `_pb_table_schemas` (multi-instance sync)                             |
| Auth satellites   | PostgreSQL (when PG enabled) | `_mfas`, `_otps`, `_externalAuths`, `_authOrigins`                                       |


**Key rule:** Hooks and record APIs (`app.FindRecordById`, `app.OnRecordCreate`, etc.) work the same regardless of backing store. Routing to SQLite or PostgreSQL is internal.

### Decision flow

```mermaid
flowchart TD
    start[Agent starts task] --> identify{Needs Postgres?}
    identify -->|No| standardPB[Use standard PocketBase Go patterns]
    identify -->|Yes| checkEnv[Set PB_POSTGRES_URL]
    checkEnv --> mode{Collection mode?}
    mode -->|New collection| newPG["collection.PostgresRecords = true before Save()"]
    mode -->|Existing PG table| import["ba.ImportPostgresTable()"]
    mode -->|Existing SQLite collection| migrate["ba.MigrateCollectionToPostgres()"]
    newPG --> hooks[Use normal hooks and routes]
    import --> hooks
    migrate --> hooks
    hooks --> customSQL{Custom SQL needed?}
    customSQL -->|Yes| recordDB["ba.RecordReadDB(c) / RecordWriteDB(c)"]
    customSQL -->|No| appAPI["app.RecordQuery(), app.FindRecordById(), etc."]
```





### Collection storage modes


| Mode                | Flags                                              | PB manages table schema? | Record DB  |
| ------------------- | -------------------------------------------------- | ------------------------ | ---------- |
| Default SQLite      | (none)                                             | Yes (SQLite)             | SQLite     |
| PB-managed Postgres | `postgresRecords: true`                            | Yes (PostgreSQL)         | PostgreSQL |
| External/imported   | `external: true`                                   | No                       | PostgreSQL |
| Auth satellites     | `_mfas`, `_otps`, `_externalAuths`, `_authOrigins` | Yes (when PG enabled)    | PostgreSQL |


---



## App Lifecycle

Source: `[pocketbase.go](pocketbase.go)`, `[core/base.go](core/base.go)`


| Step           | API                                              | Notes                                                                           |
| -------------- | ------------------------------------------------ | ------------------------------------------------------------------------------- |
| Create         | `pocketbase.New()` or `NewWithConfig(Config)`    | App is **not** bootstrapped yet — no DB, settings, or migrations                |
| Register hooks | `app.OnServe().BindFunc(...)`, etc.              | Do this **before** `Start()`                                                    |
| Start          | `app.Start()`                                    | Registers CLI commands (`serve`, `superuser`), calls `Bootstrap()`, runs server |
| Manual serve   | `app.Bootstrap()` then `apis.Serve(app, config)` | For embedding without CLI (`[apis/serve.go](apis/serve.go)`)                    |




### Bootstrap sequence (internal)

1. `initDataDB()` + `initAuxDB()` (SQLite)
2. `initPostgresDB()` if `PB_POSTGRES_URL` is set
3. `RunSystemMigrations()`
4. If PG: `InitPostgresMetadata()` → `SyncCollectionsFromPostgres()`
5. `ReloadCachedCollections()`
6. If PG: `InitPostgresSatelliteTables()`

Set `PB_POSTGRES_URL` **before** `Bootstrap()` / `Start()`.

### Minimal canonical example

```go
package main

import (
    "log"
    "os"

    "github.com/pocketbase/pocketbase"
    "github.com/pocketbase/pocketbase/core"
)

func main() {
    os.Setenv("PB_POSTGRES_URL", "postgres://user:pass@localhost:5432/mydb?sslmode=disable")
    // os.Setenv("PB_POSTGRES_SCHEMA", "public") // optional

    app := pocketbase.New()

    app.OnServe().BindFunc(func(se *core.ServeEvent) error {
        se.Router.GET("/health", func(re *core.RequestEvent) error {
            return re.JSON(200, map[string]bool{"ok": true})
        })
        return se.Next()
    })

    app.OnBootstrap().BindFunc(func(e *core.BootstrapEvent) error {
        if err := e.Next(); err != nil {
            return err
        }
        ba := core.AsBaseApp(e.App)
        if ba == nil || !ba.HasPostgres() {
            return nil
        }
        // Optional: import an existing PG table at startup
        _, err := ba.ImportPostgresTable(core.PostgresImportConfig{
            Schema:         "public",
            Table:          "products",
            CollectionName: "products",
        })
        return err
    })

    if err := app.Start(); err != nil {
        log.Fatal(err)
    }
}
```



### Entry point types


| Type                    | File            | Role                                                       |
| ----------------------- | --------------- | ---------------------------------------------------------- |
| `pocketbase.PocketBase` | `pocketbase.go` | CLI launcher; embeds `core.App`                            |
| `pocketbase.Config`     | `pocketbase.go` | `DefaultDataDir`, `DefaultDev`, DB pool sizes, `DBConnect` |
| `core.App`              | `core/app.go`   | Main interface — do not implement manually                 |
| `core.BaseApp`          | `core/base.go`  | Default implementation; exposes all hooks                  |


---



## Hook System

Source: `[core/base.go](core/base.go)`, `[tools/hook/](tools/hook/)`

### Agent-critical patterns

1. **Every handler must call** `e.Next()` or the chain stops.
2. **Tagged hooks** filter by collection id or name: `app.OnRecordCreate("orders")`.
3. **Priority:** lower `hook.Handler.Priority` runs first.
4. **Bind styles:** `BindFunc(fn)` shorthand, or `Bind(&hook.Handler[T]{Id, Priority, Func})`.

```go
app.OnRecordCreate("orders").BindFunc(func(e *core.RecordEvent) error {
    e.Record.Set("status", "pending")
    return e.Next()
})
```



### Lifecycle hooks


| Hook                                     | Event                  | When to use                                     |
| ---------------------------------------- | ---------------------- | ----------------------------------------------- |
| `OnBootstrap()`                          | `*BootstrapEvent`      | PG import/migrate at startup, post-DB init work |
| `OnServe()`                              | `*ServeEvent`          | Custom routes, middleware, static files         |
| `OnTerminate()`                          | `*TerminateEvent`      | Cleanup on shutdown                             |
| `OnBackupCreate()` / `OnBackupRestore()` | `*BackupEvent`         | Backup operations                               |
| `OnSettingsReload()`                     | `*SettingsReloadEvent` | After settings are replaced                     |


`ServeEvent` fields: `App`, `Router`, `Server`, `CertManager`, `Listener`, `InstallerFunc`, `UIExtensions`.

### Record hooks (preferred over model hooks)


| Hook                                                                                       | Phase                              |
| ------------------------------------------------------------------------------------------ | ---------------------------------- |
| `OnRecordValidate`                                                                         | Before validation                  |
| `OnRecordCreate` / `OnRecordUpdate` / `OnRecordDelete`                                     | Pre-execute                        |
| `OnRecordCreateExecute` / `OnRecordUpdateExecute` / `OnRecordDeleteExecute`                | At execute time                    |
| `OnRecordAfterCreateSuccess` / `OnRecordAfterUpdateSuccess` / `OnRecordAfterDeleteSuccess` | After commit                       |
| `OnRecordAfterCreateError` / `OnRecordAfterUpdateError` / `OnRecordAfterDeleteError`       | On error                           |
| `OnRecordEnrich`                                                                           | Response shaping / computed fields |


Model-level equivalents exist (`OnModelCreate`, `OnModelValidate`, etc.) but prefer record hooks for record work.

### Collection hooks

`OnCollectionValidate`, `OnCollectionCreate`, `OnCollectionCreateExecute`, `OnCollectionAfterCreateSuccess`, `OnCollectionAfterCreateError` — same pattern for Update and Delete.

### API request hooks (most-used)


| Hook                         | Purpose                     |
| ---------------------------- | --------------------------- |
| `OnRecordsListRequest`       | Intercept list API          |
| `OnRecordViewRequest`        | Intercept view API          |
| `OnRecordCreateRequest`      | Intercept create API        |
| `OnRecordUpdateRequest`      | Intercept update API        |
| `OnRecordDeleteRequest`      | Intercept delete API        |
| `OnCollectionCreateRequest`  | Intercept collection create |
| `OnCollectionsImportRequest` | Intercept bulk import       |
| `OnBatchRequest`             | Intercept batch API         |


Auth request hooks: `OnRecordAuthRequest`, `OnRecordAuthWithPasswordRequest`, `OnRecordAuthWithOAuth2Request`, `OnRecordAuthRefreshRequest`, password reset, verification, email change, OTP hooks.

Mail hooks: `OnMailerSend`, `OnMailerRecordPasswordResetSend`, `OnMailerRecordVerificationSend`, etc.

Realtime hooks: `OnRealtimeConnectRequest`, `OnRealtimeMessageSend`, `OnRealtimeSubscribeRequest`.

### Save execution order

```
OnRecordCreate → OnRecordValidate → OnRecordCreateExecute → INSERT
→ OnRecordAfterCreateSuccess (fires after transaction commit)
```

Update and delete follow the same pattern with their respective hooks.

---



## Custom Routes and Middleware

Sources: `[apis/base.go](apis/base.go)`, `[apis/middlewares.go](apis/middlewares.go)`, `[core/event_request.go](core/event_request.go)`

### Registering routes

```go
app.OnServe().BindFunc(func(se *core.ServeEvent) error {
    se.Router.GET("/health", func(re *core.RequestEvent) error {
        return re.String(200, "ok")
    })

    api := se.Router.Group("/api/custom").Bind(apis.RequireAuth("users"))
    api.POST("/action", handler)

    return se.Next()
})
```

Routes registered via `OnServe` on `se.Router` are added to the router created by `apis.NewRouter`, which already has the default middleware stack applied globally.

### Default middleware stack (global)

Registered in `apis.NewRouter` in order:

1. `activityLogger()`
2. `panicRecover()`
3. `rateLimit()`
4. `loadAuthToken()`
5. `superuserIPsWhitelist()`
6. `securityHeaders()`
7. `BodyLimit(DefaultMaxBodySize)`

Built-in API routes are under the `/api` group. Custom routes on `se.Router` do **not** automatically get the `/api` prefix — add it explicitly if needed.

### Exported middleware (`apis` package)


| Function                                  | Purpose                       |
| ----------------------------------------- | ----------------------------- |
| `RequireAuth(collections...)`             | Record JWT required           |
| `RequireSuperuserAuth()`                  | Superuser only                |
| `RequireGuestOnly()`                      | Must NOT have auth token      |
| `RequireSuperuserOrOwnerAuth(param)`      | Superuser or record owner     |
| `RequireSameCollectionContextAuth(param)` | Auth collection matches route |
| `BodyLimit(bytes)`                        | Request size cap              |
| `CORS(config)`                            | CORS headers                  |
| `Gzip()` / `GzipWithConfig()`             | Compression                   |
| `SkipSuccessActivityLog()`                | Suppress success logs         |
| `Static(fsys, indexFallback)`             | Static file serving           |
| `WrapStdHandler(h)`                       | Bridge std `http.Handler`     |
| `WrapStdMiddleware(m)`                    | Bridge std middleware         |


Per-route middleware:

```go
route := se.Router.GET("/path/{id}", handler)
route.Bind(apis.RequireAuth())
```



### `core.RequestEvent`

Embeds `router.Event`. Key helpers: `String`, `JSON`, `Stream`, `BindBody`, `Next`, `BadRequestError`, `UnauthorizedError`, `ForbiddenError`, `NotFoundError`, `RealIP()`, `RequestInfo()`, `HasSuperuserAuth()`.

Fields: `App`, `Auth *Record`, `Request`, `Response`.

---



## `core.App` Method Groups

Source: `[core/app.go](core/app.go)` — full godoc lives there. This is a condensed task-oriented reference.


| Group           | Key methods                                                                                                                                                                                                 |
| --------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Lifecycle       | `Bootstrap()`, `ResetBootstrapState()`, `IsBootstrapped()`, `DataDir()`, `IsDev()`, `Settings()`, `Store()`, `Cron()`, `ReloadSettings()`, `Restart()`                                                      |
| Collections     | `FindCollectionByNameOrId`, `FindCachedCollectionByNameOrId`, `FindAllCollections`, `CollectionQuery`, `Save`, `ImportCollections`, `SyncRecordTableSchema`, `TruncateCollection`, `IsCollectionNameUnique` |
| Records         | `FindRecordById`, `FindAllRecords`, `FindRecordsByFilter`, `FindFirstRecordByFilter`, `CountRecords`, `RecordQuery`, `Save`, `Delete`, `ExpandRecord`, `ExpandRecords`                                      |
| Auth            | `FindAuthRecordByToken`, `FindAuthRecordByEmail`, `CanAccessRecord`                                                                                                                                         |
| Auth satellites | `FindAllMFAsByRecord`, `FindAllOTPsByRecord`, `FindAllExternalAuthsByRecord`, `FindAllAuthOriginsByRecord`                                                                                                  |
| DB (SQLite)     | `DB()`, `ConcurrentDB()`, `NonconcurrentDB()`, `AuxDB()`, `AuxConcurrentDB()`, `AuxNonconcurrentDB()`                                                                                                       |
| Transactions    | `RunInTransaction(fn)`, `AuxRunInTransaction(fn)`, `TxInfo().OnComplete(fn)`                                                                                                                                |
| Files / mail    | `NewFilesystem`, `NewFilesystemForCollection`, `NewBackupsFilesystem`, `NewMailClient`                                                                                                                      |
| Migrations      | `RunSystemMigrations`, `RunAppMigrations`, `RunAllMigrations` — register via `core.AppMigrations`                                                                                                           |
| Realtime        | `SubscriptionsBroker()`                                                                                                                                                                                     |
| Backups         | `CreateBackup(ctx, name)`, `RestoreBackup(ctx, name)`                                                                                                                                                       |
| Unsafe          | `UnsafeWithoutHooks()` — shallow copy without hooks; do not use for normal CRUD                                                                                                                             |


For field types, validators, and collection schema details, see upstream [Go overview](https://pocketbase.io/docs/go-overview/) and [collection fields](https://pocketbase.io/docs/collections/).

### Collection types

```go
core.CollectionTypeBase = "base"
core.CollectionTypeAuth = "auth"
core.CollectionTypeView = "view"
```

---



## PostgreSQL Fork APIs

Sources: `[core/collection_external.go](core/collection_external.go)`, `[core/collection_import_postgres.go](core/collection_import_postgres.go)`, `[core/collection_migrate_postgres.go](core/collection_migrate_postgres.go)`, `[core/postgres_config.go](core/postgres_config.go)`, `[core/collection_external_options.go](core/collection_external_options.go)`

### Configuration


| Env var              | Constant                 | Default          |
| -------------------- | ------------------------ | ---------------- |
| `PB_POSTGRES_URL`    | `core.EnvPostgresURL`    | empty = disabled |
| `PB_POSTGRES_SCHEMA` | `core.EnvPostgresSchema` | `"public"`       |


```go
type PostgresConfig struct {
    URL           string
    DefaultSchema string
}

func LoadPostgresConfigFromEnv() PostgresConfig
func (c PostgresConfig) Enabled() bool
```

Loaded automatically during `BaseApp.Bootstrap()` → `initPostgresDB()`.

### Unwrap pattern (required for all PG helpers)

```go
ba := core.AsBaseApp(app)
if ba == nil || !ba.HasPostgres() {
    return nil
}
```

Works on `*pocketbase.PocketBase`, `*core.BaseApp`, and transaction wrappers via `BaseAppAccessor`.

### `*core.BaseApp` PostgreSQL methods


| Method                                                 | Use when                                          |
| ------------------------------------------------------ | ------------------------------------------------- |
| `HasPostgres()`                                        | Check PG is connected                             |
| `PostgresConfig()`                                     | Get loaded URL and default schema                 |
| `PostgresConcurrentDB()` / `PostgresNonconcurrentDB()` | Raw `dbx.Builder` access                          |
| `IsPostgresBacked(collection)`                         | Check if collection records live in PG            |
| `ManagesPostgresRecordSchema(collection)`              | PB creates/syncs the PG table                     |
| `RecordReadDB(c)` / `RecordWriteDB(c)`                 | Custom SQL — **never assume** `app.DB()`          |
| `RecordTable(c)`                                       | Qualified `"schema"."table"` name                 |
| `CollectionDialect(c)`                                 | `DialectSQLite` or `DialectPostgres`              |
| `ImportPostgresTable(config)`                          | Register existing PG table as external collection |
| `ListPostgresTables(search)`                           | Discover importable tables                        |
| `GetPostgresTablePreview(schema, table, live)`         | Preview column → field mapping                    |
| `IntrospectPostgresTable(schema, table)`               | Raw `[]PostgresColumnInfo`                        |
| `RefreshPostgresTableSchemaByTable(schema, table)`     | Re-sync external collection fields                |
| `MigrateCollectionToPostgres(collection, config)`      | Move SQLite records to PG                         |
| `RunInPostgresTransaction(fn)`                         | PG-only transactions                              |
| `RunSatelliteCascade(fn)`                              | PG tx when satellites are PG-backed               |




### Collection PostgreSQL options

Embedded in `Collection` via `collectionExternalOptions`:


| Field             | JSON key          | Set when                    | Immutable after save?          |
| ----------------- | ----------------- | --------------------------- | ------------------------------ |
| `PostgresRecords` | `postgresRecords` | New PG-managed collection   | Yes                            |
| `External`        | `external`        | Import existing table       | Yes                            |
| `PostgresTable`   | `postgresTable`   | Override default table name | Yes                            |
| `PostgresSchema`  | `postgresSchema`  | Override default schema     | Yes                            |
| `S3Files`         | `s3Files`         | Per-collection S3 storage   | Preserved if omitted on update |


Helper methods: `IsExternal()`, `UsesPostgresRecords()`, `PostgresTableName(app)`, `PostgresSchemaName(app)`, `UsesS3Files(app)`.

**Validation rules:**

- `external` and `postgresRecords` require `PB_POSTGRES_URL`
- `postgresRecords` + `external` together is invalid
- View collections cannot use `postgresRecords`
- `s3Files: true` requires PG-backed collection + global S3 enabled



### Workflow 1 — New PG-backed collection

Set `PostgresRecords` **before** first `Save()`. It is immutable after creation.

```go
ba := core.AsBaseApp(app)
if ba == nil || !ba.HasPostgres() {
    return errors.New("postgres not configured")
}

collection := core.NewCollection(core.CollectionTypeBase, "orders")
collection.PostgresRecords = true
collection.Fields.Add(
    &core.TextField{Name: "name", Required: true},
)
if err := ba.Save(collection); err != nil {
    return err
}
```



### Workflow 2 — Import existing PostgreSQL table

Table must have an `id` column. Sets `external: true`.

```go
ba := core.AsBaseApp(app)

collection, err := ba.ImportPostgresTable(core.PostgresImportConfig{
    Schema:         "public",
    Table:          "products",
    CollectionName: "products",
    Type:           core.CollectionTypeBase,
    DryRun:         false, // true = preview without saving
})
```

`PostgresImportConfig` fields:


| Field            | Required | Default                         |
| ---------------- | -------- | ------------------------------- |
| `Schema`         | No       | `PB_POSTGRES_SCHEMA` / `public` |
| `Table`          | **Yes**  | —                               |
| `CollectionName` | No       | table name                      |
| `Type`           | No       | `CollectionTypeBase`            |
| `DryRun`         | No       | `false`                         |
| `S3Files`        | No       | `true` for imports              |


PostgreSQL → PocketBase field type mapping:


| PostgreSQL type                                                                   | PocketBase field |
| --------------------------------------------------------------------------------- | ---------------- |
| `boolean`, `bool`                                                                 | bool             |
| `integer`, `bigint`, `smallint`, `numeric`, `double precision`, `real`, `decimal` | number           |
| `json`, `jsonb`                                                                   | json             |
| timestamp/date/time variants                                                      | date             |
| everything else                                                                   | text             |




### Workflow 3 — Migrate SQLite collection to PostgreSQL

Cannot migrate view collections, external collections, or already-PG-backed collections.

```go
ba := core.AsBaseApp(app)

// Dry run first
result, err := ba.MigrateCollectionToPostgres("products", core.CollectionPostgresMigrationConfig{
    DryRun: true,
})
// result.MigratedCount shows how many records would be copied

// Run migration
result, err = ba.MigrateCollectionToPostgres("products", core.CollectionPostgresMigrationConfig{
    DryRun: false,
})
// result: { CollectionId, CollectionName, MigratedCount, DryRun }
```

`CollectionPostgresMigrationConfig` fields:


| Field              | Default         | Notes                                    |
| ------------------ | --------------- | ---------------------------------------- |
| `DryRun`           | `false`         | Preview only                             |
| `DeleteSQLiteData` | `true`          | Drop SQLite record table after success   |
| `BatchSize`        | `500`           | Records per copy batch                   |
| `PostgresSchema`   | env default     | Destination schema                       |
| `PostgresTable`    | collection name | Destination table                        |
| `S3Files`          | unchanged       | Enable per-collection S3 after migration |


Migration steps (internal):

1. Create matching PostgreSQL table
2. Copy records from SQLite in batches
3. Set `postgresRecords: true` on collection metadata
4. Drop old SQLite record table (unless `DeleteSQLiteData: false`)

On failure after partial copy, the partially created PG table is dropped automatically.

---



## Constraints and Common Agent Mistakes


| DO                                                                             | DON'T                                                             |
| ------------------------------------------------------------------------------ | ----------------------------------------------------------------- |
| Use `ba.RecordReadDB(c)` / `RecordWriteDB(c)` for custom SQL on PG collections | Use `app.DB()` for PG record data                                 |
| Set `PostgresRecords` before first `Save()`                                    | Toggle `postgresRecords` / `external` after creation              |
| Call `e.Next()` in every hook handler                                          | Return early without `e.Next()` unless intentionally blocking     |
| Migrate related collections first if they share relation IDs                   | Assume file field blobs move with migration (only filenames copy) |
| Use normal `OnRecordCreate` / `OnRecordUpdate` hooks                           | Add Postgres-specific hook variants                               |
| Set `PB_POSTGRES_URL` before `Bootstrap()`                                     | Expect `/api/sql` to query PostgreSQL (SQLite only)               |
| Use `ba.IsPostgresBacked(c)` before custom SQL                                 | Assume all collections use the same database                      |
| Back up `pb_data` before production migration                                  | Migrate view or external collections                              |


---



## Testing

Source: `[tests/app.go](tests/app.go)`

```go
import "github.com/pocketbase/pocketbase/tests"

app, err := tests.NewTestApp()
if err != nil { /* ... */ }
defer app.Cleanup()

// app embeds *core.BaseApp; EventCalls tracks which hooks fired
```

- `ok tests.NewTestApp(optTestDataDir...)` — clones default test data into a temp dir
- `tests.NewTestAppWithConfig(core.BaseAppConfig{...})` — custom config
- `app.Cleanup()` — resets state and removes temp data dir
- `app.EventCalls` — map of hook names → invocation count

See upstream [testing guide](https://pocketbase.io/docs/testing) for writing custom application tests.

---



## Plugins (optional)

The `[examples/base/main.go](examples/base/main.go)` registers optional plugins:


| Plugin       | Package               | Purpose                                                   |
| ------------ | --------------------- | --------------------------------------------------------- |
| `jsvm`       | `plugins/jsvm/`       | JavaScript hooks/migrations (`pb_hooks`, `pb_migrations`) |
| `migratecmd` | `plugins/migratecmd/` | CLI migration scaffolding                                 |
| `ghupdate`   | `plugins/ghupdate/`   | GitHub self-update                                        |


JavaScript hook API is documented upstream at [Extend with JavaScript](https://pocketbase.io/docs/js-overview/). Not covered here.

---



## Upstream References

Use these for behavior not specific to this fork:

- [Go overview](https://pocketbase.io/docs/go-overview/) — hooks, routing, extending PocketBase
- [Collection fields](https://pocketbase.io/docs/collections/) — field types and schema
- [API rules and filters](https://pocketbase.io/docs/api-rules-and-filters/) — access rules syntax
- [Testing](https://pocketbase.io/docs/testing) — test patterns
- [README.md](README.md) — PostgreSQL REST endpoints, deployment, multi-instance setup

