# TamarackDB

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

## Configure

Copy `config.json.dist` to `config.json` and adjust as needed:

```sh
cp config.json.dist config.json
```

| Key | Description |
|---|---|
| `bindAddress` / `port` | Address and port the server listens on |
| `enableTls` / `tlsCertFile` / `tlsKeyFile` | TLS termination (Go's own `ListenAndServeTLS`, no reverse proxy) |
| `enableAuth` / `authToken` | Bearer token authentication on every endpoint |
| `databasePath` | Path to the SQLite database file |
| `defaultLimit` / `maxLimit` | Default and maximum page size for `QUERY /read` |
| `maxEventSize` | Maximum size in bytes of a single event |

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
- `make fmt` / `make vet` / `make tidy` — standard Go housekeeping
- `make clean` — removes `bin/`
