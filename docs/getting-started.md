# Getting started

## Use as standalone app

Build it from this repository (releases are tags only, with no prebuilt binaries). You need [Go 1.27.3+](https://go.dev/doc/install).

```sh
git clone --branch v0.41.1 https://github.com/compdani/pb-postgress.git
cd pb-postgress
go build -o pgbase ./examples/base
./pgbase serve --dbDriver postgres --dbDSN "postgres://user:pass@localhost:5432/mydb?sslmode=disable"
```

Without `--dbDriver` / `--dbDSN`, `serve` starts the installer and prints a setup URL. The dashboard is at `/_/`. To update, check out a newer tag and build again.

The executable is based on [`examples/base/main.go`](../examples/base/main.go) and has the JS VM plugin enabled, so it can be extended with JavaScript (see [Extend with JavaScript](https://pocketbase.io/docs/js-overview/) in the PocketBase docs).

## Use as a Go framework/toolkit

PgBase is distributed as a regular Go library package which allows you to build
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

## Building and running the repo main.go example

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

