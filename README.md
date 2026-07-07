<p align="center">
    <a href="https://pocketbase.io" target="_blank" rel="noopener">
        <img src="https://i.imgur.com/aCBbjKx.png" alt="PocketBase - open source backend in 1 file" />
    </a>
</p>

<p align="center">
    <a href="https://github.com/pocketbase/pocketbase/actions/workflows/release.yaml" target="_blank" rel="noopener"><img src="https://github.com/pocketbase/pocketbase/actions/workflows/release.yaml/badge.svg" alt="build" /></a>
    <a href="https://github.com/pocketbase/pocketbase/releases" target="_blank" rel="noopener"><img src="https://img.shields.io/github/release/pocketbase/pocketbase.svg" alt="Latest releases" /></a>
    <a href="https://pkg.go.dev/github.com/pocketbase/pocketbase" target="_blank" rel="noopener"><img src="https://godoc.org/github.com/pocketbase/pocketbase?status.svg" alt="Go package documentation" /></a>
</p>

[PocketBase](https://pocketbase.io) is an open source Go backend that includes:

- embedded database (_SQLite_) with **realtime subscriptions**
- optional **PostgreSQL** backing for collection records (_this fork_)
- built-in **files and users management**
- convenient **Admin dashboard UI**
- and simple **REST-ish API**

**For documentation and examples, please visit https://pocketbase.io/docs.**

> [!WARNING]
> Please keep in mind that PocketBase is still under active development
> and therefore full backward compatibility is not guaranteed before reaching v1.0.0.

## PostgreSQL support

This fork extends PocketBase with optional PostgreSQL integration. You can use it as a drop-in replacement for standard PocketBase when you need PostgreSQL-backed collection data — for example to scale horizontally, integrate with an existing database, or run alongside other services that already use Postgres.

Standard PocketBase behavior is unchanged when PostgreSQL is not configured. Set `PB_POSTGRES_URL` to enable it.

### Architecture

PocketBase still uses SQLite in `pb_data` as the local system database (settings, superusers, collections that are not PostgreSQL-backed, auxiliary data, etc.).

When PostgreSQL is configured, **collection record data** for eligible collections is stored in PostgreSQL instead of SQLite:

| Storage | What lives there |
| --- | --- |
| SQLite (`pb_data`) | App settings, admins, local-only collections, file metadata, logs |
| PostgreSQL | Records for `postgresRecords` and external collections, plus auth satellite tables (`_mfas`, `_otps`, `_externalAuths`, `_authOrigins`) when Postgres is enabled |
| PostgreSQL metadata tables | `_pb_collections`, `_pb_table_schemas` — mirrored collection schema used to sync multiple PocketBase instances against the same database |

The REST API, Admin UI, auth rules, and SDK clients work the same regardless of where records are stored.

### Configuration

Set these environment variables before starting PocketBase:

```sh
export PB_POSTGRES_URL="postgres://user:password@localhost:5432/mydb?sslmode=disable"
export PB_POSTGRES_SCHEMA="public"   # optional, defaults to "public"
```

Then run PocketBase as usual:

```sh
./pocketbase serve
```

On startup, PocketBase will:

1. Connect to PostgreSQL
2. Create the metadata tables (`_pb_collections`, `_pb_table_schemas`) if they do not exist
3. Sync mirrored collection metadata from PostgreSQL into local SQLite
4. Ensure auth satellite tables exist in PostgreSQL

### Replacing standard PocketBase with PostgreSQL

There are two ways to store collection records in PostgreSQL. Choose based on whether you already have tables in Postgres.

#### Option A — New PocketBase-managed collections

Use this when starting fresh or adding new collections that should live in PostgreSQL.

1. Configure `PB_POSTGRES_URL` (and optionally `PB_POSTGRES_SCHEMA`).
2. Start PocketBase and open the Admin dashboard.
3. Create a new collection and enable **Store records in PostgreSQL**.
4. PocketBase creates and maintains a matching table in PostgreSQL (schema syncs automatically when you change fields).

> [!IMPORTANT]
> The PostgreSQL storage option can only be set at collection creation time — unless you use the [SQLite to PostgreSQL migration](#option-c--migrate-existing-sqlite-backed-collections) described below (Admin UI: **Settings → Sync → Migrate to PostgreSQL**, or the collection edit modal).

Via the collections API, set `postgresRecords: true` when creating the collection:

```json
POST /api/collections
{
  "name": "products",
  "type": "base",
  "postgresRecords": true,
  "fields": [ ... ]
}
```

#### Option B — Import existing PostgreSQL tables

Use this when you already have tables in PostgreSQL and want PocketBase to expose them through its API, auth rules, and Admin UI.

1. Configure `PB_POSTGRES_URL`.
2. Open **Settings → Sync → Import from PostgreSQL** in the Admin dashboard.
3. Select a table and review the inferred field mapping.
4. Import it as a PocketBase collection.

Requirements for import:

- The table must have an `id` column (used as the primary record identifier).
- Supported PostgreSQL column types are mapped automatically:

| PostgreSQL type | PocketBase field |
| --- | --- |
| `boolean`, `bool` | bool |
| `integer`, `bigint`, `smallint`, `numeric`, `double precision`, `real`, `decimal` | number |
| `json`, `jsonb` | json |
| `timestamp`, `timestamptz`, `date`, `time` | date |
| everything else | text |

Imported collections are marked as **external** — PocketBase reads and writes the existing table but does not recreate it. Use **Refresh schema** in the Admin UI (or the API below) after you change the underlying table structure.

#### Option C — Migrate existing SQLite-backed collections

Use this when you already have collections storing records in SQLite and want to move them to PocketBase-managed PostgreSQL tables without recreating the collection or losing its schema, rules, and record IDs.

Prerequisites:

1. Configure `PB_POSTGRES_URL` (and optionally `PB_POSTGRES_SCHEMA`).
2. Back up your `pb_data` directory before migrating production data.
3. Ensure the collection is not a view or external/imported collection.

Migration steps:

1. Preview the migration with a dry run (no changes are made):

```json
POST /api/collections/{collectionNameOrId}/migrate/postgres
{
  "dryRun": true
}
```

2. Run the migration:

```json
POST /api/collections/{collectionNameOrId}/migrate/postgres
{}
```

The migration will:

1. Create a matching PostgreSQL table for the collection schema
2. Copy all records from SQLite into PostgreSQL (in batches of 500)
3. Update the collection metadata to set `postgresRecords: true`
4. Drop the old SQLite record table

Optional request fields:

| Field | Description |
| --- | --- |
| `dryRun` | Validate and return the record count without migrating |
| `deleteSQLiteData` | Drop the SQLite record table after migration (default: `true`) |
| `batchSize` | Records copied per batch (default: `500`) |
| `postgresSchema` | Destination schema (default: `PB_POSTGRES_SCHEMA` or `public`) |
| `postgresTable` | Destination table name (default: collection name) |
| `s3Files` | Enable per-collection S3 file storage after migration |

Example response:

```json
{
  "collectionId": "pbc_123",
  "collectionName": "products",
  "migratedCount": 42,
  "dryRun": false
}
```

From Go (after bootstrap):

```go
ba := core.AsBaseApp(app)
result, err := ba.MigrateCollectionToPostgres("products", core.CollectionPostgresMigrationConfig{
    DryRun: false,
})
```

Notes:

- File field values (filenames) are copied as-is; uploaded files remain in their current storage location unless you also enable `s3Files`.
- Relation fields store record IDs — migrate related collections first if they also need to move to PostgreSQL.
- View collections cannot be migrated.
- If migration fails after records were copied, the partially created PostgreSQL table is dropped automatically.

### Multiple PocketBase instances

Multiple PocketBase instances can point at the same PostgreSQL database and `PB_POSTGRES_URL`. Collection metadata is mirrored into `_pb_collections` so instances stay in sync on bootstrap.

Each instance still has its own local `pb_data` directory for settings and non-PostgreSQL data. Plan your deployment accordingly (shared S3 for files, consistent settings, etc.).

### S3 file storage

PostgreSQL-backed collections can optionally store uploaded files in S3 instead of local disk. This requires global S3 to be enabled under **Settings → Files storage**, and per-collection `s3Files: true` (enabled by default for imported collections).

### PostgreSQL API endpoints

Superuser auth required:

| Method | Endpoint | Description |
| --- | --- | --- |
| `GET` | `/api/collections/postgres/status` | Whether PostgreSQL is configured |
| `GET` | `/api/collections/postgres/tables` | List importable tables (`?search=`) |
| `GET` | `/api/collections/postgres/tables/{schema}/{table}` | Preview table schema (`?live=true` for live introspection) |
| `POST` | `/api/collections/import/postgres` | Import a table as a collection |
| `POST` | `/api/collections/import/postgres/refresh` | Refresh an imported collection's schema from Postgres |
| `POST` | `/api/collections/{collection}/migrate/postgres` | Migrate a SQLite-backed collection to PostgreSQL |

Example import request:

```json
POST /api/collections/import/postgres
{
  "schema": "public",
  "table": "products",
  "collectionName": "products",
  "type": "base",
  "dryRun": false
}
```

Set `"dryRun": true` to preview the inferred collection without saving it.

### Using as a Go framework

If you already build on PocketBase as a Go library — custom `main.go`, hooks, plugins, migrations, routes — this fork is a **drop-in replacement** for upstream [`github.com/pocketbase/pocketbase`](https://github.com/pocketbase/pocketbase). The public API (`pocketbase.New()`, `core.App`, record/collection hooks, etc.) is unchanged. PostgreSQL is opt-in via environment variables and does not require restructuring your app.

#### Swap the dependency

Point your `go.mod` at this fork instead of the official release:

```go
// go.mod
module myapp

go 1.25

require github.com/pocketbase/pocketbase v0.0.0

replace github.com/pocketbase/pocketbase => github.com/compdani/pb-postgress v0.0.0
```

All existing imports (`github.com/pocketbase/pocketbase`, `.../core`, `.../apis`, plugins, etc.) stay the same. Rebuild and deploy as you normally would.

#### Enable PostgreSQL in your custom app

PostgreSQL is configured through `PB_POSTGRES_URL` and `PB_POSTGRES_SCHEMA`, which are read automatically during `app.Bootstrap()` — the same path whether you call `app.Start()` or bootstrap manually. Set them in your process environment, or in `main()` before starting the app:

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

    // your existing hooks, plugins, and routes work unchanged
    app.OnServe().BindFunc(func(se *core.ServeEvent) error {
        se.Router.GET("/health", func(re *core.RequestEvent) error {
            return re.String(200, "ok")
        })
        return se.Next()
    })

    if err := app.Start(); err != nil {
        log.Fatal(err)
    }
}
```

Without `PB_POSTGRES_URL`, the app behaves exactly like standard PocketBase (SQLite only).

#### Programmatic PostgreSQL APIs

After bootstrap, access PostgreSQL helpers through `core.AsBaseApp(app)`. This unwraps `*core.BaseApp` from the `core.App` interface (including from `PocketBase` instances and transaction wrappers).

```go
app.OnBootstrap().BindFunc(func(e *core.BootstrapEvent) error {
    if err := e.Next(); err != nil {
        return err
    }

    ba := core.AsBaseApp(e.App)
    if ba == nil || !ba.HasPostgres() {
        return nil
    }

    // import an existing PostgreSQL table as a PocketBase collection
    _, err := ba.ImportPostgresTable(core.PostgresImportConfig{
        Schema:         "public",
        Table:          "products",
        CollectionName: "products",
        Type:           core.CollectionTypeBase,
    })
    return err
})
```

Create a new collection whose records are managed in PostgreSQL:

```go
collection := core.NewCollection(core.CollectionTypeBase, "orders")
collection.PostgresRecords = true // immutable after save — set before first Save()
collection.Fields.Add(
    &core.TextField{Name: "name", Required: true},
)
if err := ba.Save(collection); err != nil {
    return err
}
```

Useful methods on `*core.BaseApp`:

| Method | Description |
| --- | --- |
| `HasPostgres()` | Whether PostgreSQL connected successfully |
| `PostgresConfig()` | Loaded URL and default schema |
| `IsPostgresBacked(collection)` | Whether a collection's records are stored in PostgreSQL |
| `ImportPostgresTable(config)` | Register an existing table as an external collection |
| `ListPostgresTables(search)` | List tables available for import |
| `GetPostgresTablePreview(schema, table, live)` | Preview column → field mapping |
| `RefreshPostgresTableSchemaByTable(schema, table)` | Re-sync fields from the live table |
| `MigrateCollectionToPostgres(collection, config)` | Migrate a SQLite-backed collection's records to PostgreSQL |
| `RecordReadDB(collection)` / `RecordWriteDB(collection)` | Get the correct `dbx.Builder` for a collection |

Record and collection hooks (`OnRecordCreate`, `OnCollectionCreate`, etc.) do not need Postgres-specific changes — routing to SQLite or PostgreSQL happens internally based on the collection's options.

Use `ba.IsPostgresBacked(collection)` only when you need to run custom SQL and must target the correct database.

#### Migrating an existing Go + PocketBase project

1. Replace the `go.mod` dependency with this fork (see above).
2. Run `go mod tidy` and fix any compile errors (there should be none for typical apps).
3. Set `PB_POSTGRES_URL` in your deployment config (Docker, systemd, Kubernetes, etc.).
4. For **new** collections, set `postgresRecords: true` at creation (Admin UI, API, or `collection.PostgresRecords = true` in Go).
5. For **existing** PostgreSQL tables, call `ImportPostgresTable` or use the Admin UI import flow.
6. For **existing** SQLite-backed collections, call `MigrateCollectionToPostgres` or use `POST /api/collections/{collection}/migrate/postgres`.

The repo's [`examples/base/main.go`](examples/base/main.go) is the same entry point as upstream; build it from this fork and set `PB_POSTGRES_URL` to get PostgreSQL support with no code changes.

## API SDK clients

The easiest way to interact with the PocketBase Web APIs is to use one of the official SDK clients:

- **JavaScript - [pocketbase/js-sdk](https://github.com/pocketbase/js-sdk)** (_Browser, Node.js, React Native_)
- **Dart - [pocketbase/dart-sdk](https://github.com/pocketbase/dart-sdk)** (_Web, Mobile, Desktop, CLI_)

You could also check the recommendations in https://pocketbase.io/docs/how-to-use/.


## Overview

### Use as standalone app

You could download the prebuilt executable for your platform from the [Releases page](https://github.com/pocketbase/pocketbase/releases).
Once downloaded, extract the archive and run `./pocketbase serve` in the extracted directory.

The prebuilt executables are based on the [`examples/base/main.go` file](https://github.com/pocketbase/pocketbase/blob/master/examples/base/main.go) and comes with the JS VM plugin enabled by default which allows to extend PocketBase with JavaScript (_for more details please refer to [Extend with JavaScript](https://pocketbase.io/docs/js-overview/)_).

### Use as a Go framework/toolkit

PocketBase is distributed as a regular Go library package which allows you to build
your own custom app specific business logic and still have a single portable executable at the end.

To use PostgreSQL-backed collections in a custom app, depend on this fork and set `PB_POSTGRES_URL` — see [Using as a Go framework](#using-as-a-go-framework) for the full migration guide, `go.mod` replace directive, and programmatic APIs.

Here is a minimal example:

0. [Install Go 1.25+](https://go.dev/doc/install) (_if you haven't already_)

1. Create a new project directory with the following `main.go` file inside it:
    ```go
    package main

    import (
        "log"

        "github.com/pocketbase/pocketbase"
        "github.com/pocketbase/pocketbase/core"
    )

    func main() {
        app := pocketbase.New()

        app.OnServe().BindFunc(func(se *core.ServeEvent) error {
            // registers new "GET /hello" route
            se.Router.GET("/hello", func(re *core.RequestEvent) error {
                return re.String(200, "Hello world!")
            })

            return se.Next()
        })

        if err := app.Start(); err != nil {
            log.Fatal(err)
        }
    }
    ```

2. To init the dependencies, run `go mod init myapp && go mod tidy`.

3. To start the application, run `go run main.go serve`.

4. To build a statically linked executable, you can run `CGO_ENABLED=0 go build` and then start the created executable with `./myapp serve`.

_For more details please refer to [Extend with Go](https://pocketbase.io/docs/go-overview/)._

### Building and running the repo main.go example

To build the minimal standalone executable, like the prebuilt ones in the releases page, you can simply run `go build` inside the `examples/base` directory:

0. [Install Go 1.25+](https://go.dev/doc/install) (_if you haven't already_)
1. Clone/download the repo
2. Navigate to `examples/base`
3. Run `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build`
   (_https://go.dev/doc/install/source#environment_)
4. Start the created executable by running `./base serve`.

Note that the supported build targets by the pure Go SQLite driver at the moment are:

```
darwin  amd64
darwin  arm64
freebsd amd64
freebsd arm64
linux   386
linux   amd64
linux   arm
linux   arm64
linux   loong64
linux   ppc64le
linux   riscv64
linux   s390x
windows 386
windows amd64
windows arm64
```

### Testing

PocketBase comes with mixed bag of unit and integration tests.
To run them, use the standard `go test` command:

```sh
go test ./...
```

Check also the [Testing guide](http://pocketbase.io/docs/testing) to learn how to write your own custom application tests.

## Security

If you discover a security vulnerability within PocketBase, please send an e-mail to **support at pocketbase.io**.

All reports will be promptly addressed and you'll be credited in the fix release notes.

## Contributing

PocketBase is free and open source project licensed under the [MIT License](LICENSE.md).
You are free to do whatever you want with it, even offering it as a paid service.

You could help continuing its development by:

- [Contribute to the source code](CONTRIBUTING.md)
- [Suggest new features and report issues](https://github.com/pocketbase/pocketbase/issues)

Please refrain creating PRs for _new features_ without previously discussing the implementation details.
PocketBase has a [roadmap](https://github.com/orgs/pocketbase/projects/2) and I try to work on issues in specific order and such PRs often come in out of nowhere and skew all initial planning with tedious back-and-forth communication.

Don't get upset if I close your PR, even if it is well executed and tested. This doesn't mean that it will never be merged.
Later we can always refer to it and/or take pieces of your implementation when the time comes to work on the issue (don't worry you'll be credited in the release notes).

> [!IMPORTANT]
> Due to recent LLM spam, PRs are temporary disabled and only existing collaborators can open a PR.
> If you stumble on a problem that you want to fix, please consider instead opening an issue or discussion with link to your fork _(if not obvious - LLM contributions are not welcome)_.
> This status may change in the future in case GitHub finally decide to do something about the constant spam, or when I find time to move the project somewhere else.
