# Database

Postgress Pocket replaces the embedded SQLite database with a shared **PostgreSQL or MySQL** server, so several PocketBase instances can run against one database. It also adds an OAuth2 server for auth collections and usernameless passkeys. It needs **Go 1.27.3 or later**. SQLite is used only to read `data.db` from imported PocketBase backups.

Upstream sync: includes the fixes from PocketBase up to **v0.41.1** that fit this fork (log data size limit, thumbnail pixel cap, delayed JSON response headers, mailer fixes, `Event.Extra`), and restores backups made by PocketBase v0.23.0 to v0.41.x.

## Connect a database

```sh
./pocketbase serve --dbDriver postgres --dbDSN "postgres://user:pass@localhost:5432/mydb?sslmode=disable"
# or: PB_DB_DRIVER=postgres PB_DB_DSN=... ./pocketbase serve
```

`driver` is `postgres` or `mysql`. In Go use `pocketbase.Config{DBDriver, DBDSN}`.

Without a database, `serve` starts only the installer and prints a setup URL (`/_/#/pbinstall/<token>`) where you test the connection, create the first superuser and choose file storage (local, file server or S3). The installer details, instance scopes (`/api/instance/scope`) and remaining work are in [DATABASE_MIGRATION.md](../DATABASE_MIGRATION.md).

