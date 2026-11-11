<h1 align="center">PgBase</h1>

<p align="center">An open source Go backend on a shared PostgreSQL / MySQL database, with OAuth2 single sign-on and passkeys.</p>

> [!NOTE]
> **PgBase is a fork of [PocketBase](https://pocketbase.io)** by [Gani Georgiev](https://ganigeorgiev.com) ([source](https://ganigeorgiev.com/git/pocketbase/pocketbase)), used under the [MIT License](LICENSE.md). All credit for the original project goes to PocketBase and its contributors. This fork is not affiliated with or endorsed by the PocketBase project; please don't send it support requests or security reports for this fork.

PgBase keeps PocketBase's features:

- database with **realtime subscriptions**
- built-in **files and users management**
- convenient **Admin dashboard UI**
- and simple **REST-ish API**

and adds a shared **PostgreSQL / MySQL** server database instead of SQLite, an **OAuth2 server** and **passkeys** for single sign-on.

For the upstream features and API, see the PocketBase docs at https://pocketbase.io/docs. They apply here except where this README says otherwise.

> [!WARNING]
> Like PocketBase, this project is under active development and full backward compatibility is not guaranteed before reaching v1.0.0.


## What PgBase adds

- a shared **PostgreSQL / MySQL** server database instead of SQLite, so several instances can run against one database ([docs](docs/database.md))
- on-demand portable **backups**, and restore of PocketBase v0.23.0 to v0.41.x backups ([docs](docs/backups.md))
- an **OAuth2 / OIDC server** on auth collections, with popup sign-in for your frontends ([docs](docs/oauth2-server.md))
- usernameless **passkeys** ([docs](docs/passkeys.md))
- file hosting through a **file server** or S3 ([docs](docs/file-storage.md))

Requires **Go 1.27.3 or later**. Includes the PocketBase fixes up to **v0.41.1** that fit this fork.

## Quick start

```sh
git clone --branch v0.41.1 https://github.com/compdani/pb-postgress.git
cd pb-postgress
go build -o pgbase ./examples/base
./pgbase serve --dbDriver postgres --dbDSN "postgres://user:pass@localhost:5432/mydb?sslmode=disable"
```

Without `--dbDriver` / `--dbDSN`, `serve` starts the installer and prints a setup URL. The dashboard is at `/_/`.

## Documentation

See the [docs folder](docs/README.md): [getting started](docs/getting-started.md), [database](docs/database.md), [backups](docs/backups.md), [OAuth2 server](docs/oauth2-server.md), [passkeys](docs/passkeys.md), [file storage](docs/file-storage.md), [testing](docs/testing.md).

## API SDK clients

- **JavaScript - [pocketbase/js-sdk](https://github.com/pocketbase/js-sdk)** (_Browser, Node.js, React Native_)
- **Dart - [pocketbase/dart-sdk](https://github.com/pocketbase/dart-sdk)** (_Web, Mobile, Desktop, CLI_)

## Security

Report vulnerabilities in this fork to its maintainer through this repository. For vulnerabilities in PocketBase itself, follow the [upstream security policy](https://ganigeorgiev.com/git/pocketbase/pocketbase/src/branch/master/SECURITY.md).

## License and attribution

PgBase is licensed under the [MIT License](LICENSE.md). The original copyright, `Copyright (c) 2022 - present, Gani Georgiev`, is kept as the license requires. Contributions to this fork follow [CONTRIBUTING.md](CONTRIBUTING.md); for changes that belong in PocketBase itself, please use the upstream project.

Database transition: PgBase now configures PostgreSQL or MySQL through `PB_DB_DRIVER` / `PB_DB_DSN` (or `--dbDriver` / `--dbDSN`). See [server database transition and outstanding work](DATABASE_MIGRATION.md).
