# Bucket

`bucket` is a standalone file server for one PocketBase database. It stores bytes on disk and serves them to other instances. It does not import the PocketBase app, and it does not use SQLite. A process is either Postgres or MySQL, the same single database as the main instance.

S3 mode is configured on the main instance. This process is only the filesystem file server (`mode=bucket`).

## Run

```sh
export PB_DB_DRIVER=postgres
export PB_DB_DSN='postgres://user:pass@127.0.0.1:5432/app?sslmode=disable'
export PB_BUCKET_PUBLIC_URL='https://files.example'

go run ./cmd/bucket --dir ./pb_bucket --http 127.0.0.1:8091
```

MySQL uses a `go-sql-driver` DSN:

```sh
export PB_DB_DRIVER=mysql
export PB_DB_DSN='user:pass@tcp(127.0.0.1:3306)/app'
```

Flags override the environment: `--dir` (`PB_BUCKET_DIR`), `--http` (`PB_BUCKET_HTTP`), `--apiKey` (`PB_BUCKET_API_KEY`), `--publicURL` (`PB_BUCKET_PUBLIC_URL`), `--dbDriver` (`PB_DB_DRIVER`), `--dbDSN` (`PB_DB_DSN`), `--maxBytes` (`PB_BUCKET_MAX_BYTES`), `--cors` (`PB_BUCKET_CORS`).

If `--apiKey` is omitted, a bearer secret is generated, stored in `<dir>/.secret`, and printed once as `PB_BUCKET_API_KEY=...`.

On startup the process registers itself in `_fileHosts` (`id`, `url`, `mode`, `secret`, `registered_at`) with `mode=bucket`, replacing earlier bucket rows. It refuses to start if a `full` or `s3` host is registered. PocketBase uses the bucket only when its file mode is `fileServer`. It creates `_fileHosts` and `_fileChanges` when they are missing, and adds any of those columns that an existing table does not have yet.

## HTTP API

Other instances write bytes here. The database is not a byte channel.

| Method | Path | Auth |
| --- | --- | --- |
| `GET` | `/healthz` | public |
| `PUT` | `/files/{collection}/{recordId}/{filename...}` | bearer |
| `GET`, `HEAD` | `/files/{collection}/{recordId}/{filename...}` | bearer or signed token |
| `DELETE` | `/files/{collection}/{recordId}/{filename...}` | bearer |
| `DELETE` | `/files/{collection}/{recordId}/` | bearer; deletes the record directory |
| `GET` | `/files/?prefix=&after=` | bearer |

`{filename...}` may nest up to four segments (PocketBase thumbnails use `thumbs_<file>/<size>_<file>`). Nothing is served anonymously: PocketBase checks record and field access rules and then proxies the download with the bearer secret. A signed token grants one key until it expires.

`GET /files/?prefix=` returns `{"prefix","items":[{key,size,updated,...}],"truncated"}` in key order, at most 1000 items. When `truncated` is true, repeat with `after=<last key>`.

```
Authorization: Bearer <secret>
```

`PUT` is atomic (temp file in the destination directory, then rename) and idempotent. The first write is `201`; a replacement is `200`. The body is the file bytes. `X-Protected: true` (or `?protected=1`) marks the object protected. Size and content type are stored beside the object under `meta/<collection>/<recordId>/<filename>.pbmeta`.

`GET` sets `Content-Type`, `ETag`, `Cache-Control`, and `Accept-Ranges`. A single `Range` returns `206`. `X-Protected` only changes caching (`no-store`).

Signed token (HMAC-SHA256, secret is the bearer secret):

```
payload = base64url(collection/recordId/filename) + "." + unixExpiry
token   = payload + "." + base64url(HMAC-SHA256(secret, payload))
```

The token is expired when `now >= expiry`. A main instance that can read `_fileHosts.secret` can mint the same token.

`..`, absolute paths, backslashes, and symlinks that point outside the storage root are rejected.

## Change feed

`_fileChanges` is append-only. The main instance inserts a row when a record file changes and, on Postgres, `NOTIFY pb_file_changes`.

| Column | |
| --- | --- |
| `id` | `BIGSERIAL` or `BIGINT AUTO_INCREMENT` |
| `op` | `upsert` or `delete` |
| `collection`, `record_id`, `filename` | object key |
| `created_at` | insert time |

The bucket reads `id` greater than its cursor. Ids are allocated at insert but visible at commit, so a row behind a missing id waits up to 30 seconds (from its `created_at`) for the earlier id to commit; after that the gap is treated as a rollback. `delete` removes the object unless it was written after the row's `created_at`, or the whole record directory when `filename` is empty or `*`. PocketBase also deletes files directly through `_fileDeleteQueue`, so the feed is a backstop. `upsert` recomputes the ETag from the file on disk and drops the cached metadata. Postgres uses `LISTEN pb_file_changes` and also polls. MySQL polls. The cursor is `<dir>/.cursor`. If it cannot be read, the watcher stops instead of replaying from 0; remove the file to replay deliberately.

Real database tests run only when `PB_TEST_PG_DSN` or `PB_TEST_MYSQL_DSN` is set.
