# Testing

PgBase comes with mixed bag of unit and integration tests.
To run them, use the standard `go test` command:

```sh
go test ./...
```

Tests run on a disposable server database and are skipped without one. Set `PB_TEST_DB_DSN` (for example `postgres://user@127.0.0.1:5432/postgres?sslmode=disable`) to choose the database, and `PB_TEST_DB_DRIVER=mysql` when it is a MySQL DSN.

Check also the [Testing guide](http://pocketbase.io/docs/testing) to learn how to write your own custom application tests.

