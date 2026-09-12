# Building TamarackDB

This is for anyone building TamarackDB from source: contributors and packagers.
For how to run it, see [deploy.md](deploy.md). For how it works inside, see
[design.md](design.md).

## Build

```sh
make build
```

This builds three binaries under `bin/`:

| Binary | Purpose |
|---|---|
| `tamarackdb` | The HTTP server |
| `tamarackdb-migrate` | Standalone schema migration tool |
| `tamarackdb-init` | Creates a new database file, a default config file, or both |

The running build's version comes from the `VERSION` file at the root of the repo. It
is baked into the binary at build time, not read at runtime. See
[design.md](design.md#versioning) for how that works.

### Cross-compiling for amd64/arm64

TamarackDB only targets Linux (Docker covers every other platform). One Makefile
target builds both architectures, one binary set per folder:

```sh
make build-linux    # bin/linux-amd64/, bin/linux-arm64/
```

## Test

```sh
make test
```

## Demo dataset

`cmd/demo` fills a SQLite file with a large set of made-up events: random types,
one or two identifiers, one metadata tag, and filler text as payload. It writes
straight to the store, not through the HTTP server, so it can seed a large
database fast. Use it to try `QUERY /read` at scale.

```sh
make demo
./bin/tamarackdb-demo -db /path/to/tamarack.db -n 1000000 -seed 1
```

`-n` sets how many events to write, `-seed` makes the run repeatable.

## Other Makefile targets

- `make run`: build, then start the server with the default config
- `make fmt` / `make vet` / `make tidy`: standard Go housekeeping
- `make clean`: removes `bin/`
