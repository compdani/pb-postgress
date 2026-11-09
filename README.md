<h1 align="center">Postgress Pocket</h1>

<p align="center">An open source Go backend on a shared PostgreSQL / MySQL database, with OAuth2 single sign-on and passkeys.</p>

> [!NOTE]
> **Postgress Pocket is a fork of [PocketBase](https://pocketbase.io)** by [Gani Georgiev](https://ganigeorgiev.com) ([source](https://ganigeorgiev.com/git/pocketbase/pocketbase)), used under the [MIT License](LICENSE.md). All credit for the original project goes to PocketBase and its contributors. This fork is not affiliated with or endorsed by the PocketBase project; please don't send it support requests or security reports for this fork.

Postgress Pocket keeps PocketBase's features:

- database with **realtime subscriptions**
- built-in **files and users management**
- convenient **Admin dashboard UI**
- and simple **REST-ish API**

and adds a shared **PostgreSQL / MySQL** server database instead of SQLite, an **OAuth2 server** and **passkeys** for single sign-on.

For the upstream features and API, see the PocketBase docs at https://pocketbase.io/docs. They apply here except where this README says otherwise.

> [!WARNING]
> Like PocketBase, this project is under active development and full backward compatibility is not guaranteed before reaching v1.0.0.

## What Postgress Pocket adds

Postgress Pocket replaces the embedded SQLite database with a shared **PostgreSQL or MySQL** server, so several PocketBase instances can run against one database. It also adds an OAuth2 server for auth collections and usernameless passkeys. It needs **Go 1.27.3 or later**. SQLite is used only to read `data.db` from imported PocketBase backups.

Upstream sync: includes the fixes from PocketBase up to **v0.41.1** that fit this fork (log data size limit, thumbnail pixel cap, delayed JSON response headers, mailer fixes, `Event.Extra`), and restores backups made by PocketBase v0.23.0 to v0.41.x.

### Connect a database

```sh
./pocketbase serve --dbDriver postgres --dbDSN "postgres://user:pass@localhost:5432/mydb?sslmode=disable"
# or: PB_DB_DRIVER=postgres PB_DB_DSN=... ./pocketbase serve
```

`driver` is `postgres` or `mysql`. In Go use `pocketbase.Config{DBDriver, DBDSN}`.

Without a database, `serve` starts only the installer and prints a setup URL (`/_/#/pbinstall/<token>`) where you test the connection, create the first superuser and choose file storage (local, file server or S3). The installer details, instance scopes (`/api/instance/scope`) and remaining work are in [DATABASE_MIGRATION.md](DATABASE_MIGRATION.md).

### Backups

Backups are on demand (`POST /api/backups`); the backup cron is removed, so regular protection is the database server's job (base backups, PITR, binlogs).

- A backup is a portable zip that restores on PostgreSQL and MySQL alike, and keeps token and OAuth2 secrets, so users stay signed in after a restore (restoring encrypted settings needs the same `PB_ENCRYPTION` key).
- `POST /api/backups/{key}/restore` also accepts a standard PocketBase backup (`data.db` + `storage/`) from **v0.23.0 to v0.41.x**. View queries are translated from SQLite where possible; a view that still fails is skipped and listed afterwards. `auxiliary.db` (logs) is not imported.

Details: [DATABASE_MIGRATION.md](DATABASE_MIGRATION.md#backups).

### OAuth2 server (single sign-on)

Any auth collection can act as an OAuth2 / OIDC server (**Collection → Options → OAuth2 server**), so other apps can sign its users in with a single token:

- authorization code flow with optional PKCE (S256), `client_secret_basic` or `client_secret_post`, OIDC `id_token`, `userinfo` and discovery;
- the issued access token is the collection's regular auth token;
- a built-in sign-in page with password or passkey, a remembered sign-in cookie (configurable duration), an account chooser (continue, switch account, sign out), `prompt=none|login|select_account|consent`, and `logout`;
- apps that share the database accept the same token. Other PocketBase apps can use it as an `oidc` provider through the normal popup + realtime flow, which lets all of them share one passkey domain.

Endpoints live under `/api/collections/{collection}/oauth2-server/` (`authorize`, `token`, `userinfo`, `logout`, `.well-known/openid-configuration`).

### Passkeys

Enable **Passkeys** on an auth collection for WebAuthn sign-in with no email or username (discoverable credentials, user verification required):

- `request-passkey-registration` / `confirm-passkey-registration` (signed in) register a passkey; `request-passkey` / `auth-with-passkey` sign in;
- credentials are stored in `_passkeys`, and each user can list and delete their own;
- set the relying party ID and allowed origins in the collection options (default: the Application URL).

### File storage

Files can be stored locally, in S3, or through a file server. `cmd/bucket` is a small file host for a shared database: other instances send files to it with `PUT /files/...` and a bearer token. See [tools/bucket/README.md](tools/bucket/README.md).

## API SDK clients

The easiest way to interact with the PocketBase Web APIs is to use one of the official SDK clients:

- **JavaScript - [pocketbase/js-sdk](https://github.com/pocketbase/js-sdk)** (_Browser, Node.js, React Native_)
- **Dart - [pocketbase/dart-sdk](https://github.com/pocketbase/dart-sdk)** (_Web, Mobile, Desktop, CLI_)

You could also check the recommendations in https://pocketbase.io/docs/how-to-use/.


## Overview

### Use as standalone app

Build it from this repository (releases are tags only, with no prebuilt binaries). You need [Go 1.27.3+](https://go.dev/doc/install).

```sh
git clone --branch v0.41.1 https://github.com/compdani/pb-postgress.git
cd pb-postgress
go build -o pocketbase ./examples/base
./pocketbase serve --dbDriver postgres --dbDSN "postgres://user:pass@localhost:5432/mydb?sslmode=disable"
```

Without `--dbDriver` / `--dbDSN`, `serve` starts the installer and prints a setup URL. The dashboard is at `/_/`. To update, check out a newer tag and build again.

The executable is based on [`examples/base/main.go`](examples/base/main.go) and has the JS VM plugin enabled, so it can be extended with JavaScript (see [Extend with JavaScript](https://pocketbase.io/docs/js-overview/) in the PocketBase docs).

### Use as a Go framework/toolkit

PocketBase is distributed as a regular Go library package which allows you to build
your own custom app specific business logic and still have a single portable executable at the end.

In a custom app, keep importing `github.com/pocketbase/pocketbase` and point it at this repository:

```sh
go mod edit -replace github.com/pocketbase/pocketbase=github.com/compdani/pb-postgress@v0.41.1
go mod tidy
```

(`v0.41.1` is the [latest release](https://github.com/compdani/pb-postgress/releases/tag/v0.41.1); use a newer tag, a commit or `master` to move to a newer version.) Your app's `go.mod` must also say `go 1.27` or later. Then set `pocketbase.Config{DBDriver, DBDSN}` or the `PB_DB_DRIVER` / `PB_DB_DSN` variables. The server database is required.

Here is a minimal example:

0. [Install Go 1.27.3+](https://go.dev/doc/install) (_if you haven't already_)

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

0. [Install Go 1.27.3+](https://go.dev/doc/install) (_if you haven't already_)
1. Clone `https://github.com/compdani/pb-postgress`
2. Navigate to `examples/base`
3. Run `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build`
   (_https://go.dev/doc/install/source#environment_)
4. Start the created executable by running `./base serve`.

Note that the supported build targets of the pure Go SQLite driver (used to read imported PocketBase backups) at the moment are:

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

Tests run on a disposable server database and are skipped without one. Set `PB_TEST_DB_DSN` (for example `postgres://user@127.0.0.1:5432/postgres?sslmode=disable`) to choose the database, and `PB_TEST_DB_DRIVER=mysql` when it is a MySQL DSN.

Check also the [Testing guide](http://pocketbase.io/docs/testing) to learn how to write your own custom application tests.

## Security

Report vulnerabilities in this fork to its maintainer through this repository. For vulnerabilities in PocketBase itself, follow the [upstream security policy](https://ganigeorgiev.com/git/pocketbase/pocketbase/src/branch/master/SECURITY.md).

## License and attribution

Postgress Pocket is licensed under the [MIT License](LICENSE.md). The original copyright, `Copyright (c) 2022 - present, Gani Georgiev`, is kept as the license requires. Contributions to this fork follow [CONTRIBUTING.md](CONTRIBUTING.md); for changes that belong in PocketBase itself, please use the upstream project.

Database transition: Postgress Pocket now configures PostgreSQL or MySQL through `PB_DB_DRIVER` / `PB_DB_DSN` (or `--dbDriver` / `--dbDSN`). See [server database transition and outstanding work](DATABASE_MIGRATION.md).
