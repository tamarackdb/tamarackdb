![](docs/tamarackdb-logo.png)

TamarackDB is an event store in Go, compliant with the [DCB (Dynamic Consistency
Boundaries) specification](https://dcb.events/specification/), accessible via HTTP,
using SQLite as the storage engine.

It runs as a single instance ("single brain"), not a cluster: each application owns
its own TamarackDB instance, backed by its own SQLite database file. It targets
internal enterprise systems with modest throughput and few concurrent writers — not
a general-purpose product competing with DCB implementations built for larger scale.

See `docs/design.md` for the full design specification (data model, HTTP API, query
grammar, concurrency handling, storage schema).

## Build

```sh
make build
```

Produces three binaries under `bin/`:

| Binary | Purpose |
|---|---|
| `tamarackdb` | The HTTP server |
| `tamarackdb-migrate` | Standalone schema migration tool, run once between a schema change and a server deployment |
| `tamarackdb-init` | Provisions a new, empty SQLite database file with the current schema applied |

### Cross-compiling for other platforms

Dedicated Makefile targets build for other operating systems, placing each
platform's binaries under `bin/<os>-<arch>/`:

```sh
make build-linux    # bin/linux-amd64/, bin/linux-arm64/
make build-windows  # bin/windows-amd64/, bin/windows-arm64/ (.exe)
make build-macos    # bin/darwin-amd64/, bin/darwin-arm64/
make build-all      # all of the above
```

## Configure

Copy `config.json.dist` to `config.json` and adjust as needed:

```sh
cp config.json.dist config.json
```

| Key | Environment variable | Description |
|---|---|---|
| `bindAddress` / `port` | `TAMARACKDB_BIND_ADDRESS` / `TAMARACKDB_PORT` | Address and port the server listens on |
| `enableTls` / `tlsCertFile` / `tlsKeyFile` | `TAMARACKDB_ENABLE_TLS` / `TAMARACKDB_TLS_CERT_FILE` / `TAMARACKDB_TLS_KEY_FILE` | TLS termination (Go's own `ListenAndServeTLS`, no reverse proxy) |
| `enableAuth` / `authToken` | `TAMARACKDB_ENABLE_AUTH` / `TAMARACKDB_AUTH_TOKEN` | Bearer token authentication on every endpoint |
| `databasePath` | `TAMARACKDB_DATABASE_PATH` | Path to the SQLite database file |
| `defaultLimit` / `maxLimit` | `TAMARACKDB_DEFAULT_LIMIT` / `TAMARACKDB_MAX_LIMIT` | Default and maximum page size for `QUERY /read` |
| `maxEventSize` | `TAMARACKDB_MAX_EVENT_SIZE` | Maximum size in bytes of a single event |

`config.json` is optional: any field it omits, or the whole file if it's absent, falls
back to the matching `TAMARACKDB_*` environment variable, then to a built-in default.
A value set in `config.json` always wins over the environment. This makes
`config.json` the right tool for production, one file per instance, while plain
environment variables cover a Docker deployment with no file at all.

## Run

```sh
./bin/tamarackdb-init -p /path/to/tamarack.db
./bin/tamarackdb -config config.json
```

`tamarackdb -version` prints the running build's version (from `VERSION`) and exits
without loading the configuration file or opening the store.

## Test

```sh
make test
```

## Other Makefile targets

- `make demo` — builds `tamarackdb-demo`
- `make build-linux` / `make build-windows` / `make build-macos` / `make build-all` — cross-compile for other platforms, see [Cross-compiling for other platforms](#cross-compiling-for-other-platforms)
- `make fmt` / `make vet` / `make tidy` — standard Go housekeeping
- `make clean` — removes `bin/`
