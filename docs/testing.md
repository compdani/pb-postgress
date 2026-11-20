# Testing

PgBase comes with mixed bag of unit and integration tests.
To run them, use the standard `go test` command:

```sh
go test ./...
```

Tests run on a disposable server database and are skipped without one. Set `PB_TEST_DB_DSN` (for example `postgres://user@127.0.0.1:5432/postgres?sslmode=disable`) to choose the server; each test creates its own `UTF8` database on it, so the user needs `CREATE DATABASE` permission.

Check also the [Testing guide](http://pocketbase.io/docs/testing) to learn how to write your own custom application tests.

