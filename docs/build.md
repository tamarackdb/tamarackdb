# Building TamarackDB

This is for anyone building TamarackDB from source: contributors and packagers.
For how to run it, see [deploy.md](deploy.md). For how it works inside, see
[design.md](design.md).

## Build

```sh
make build
```

This builds four binaries under `bin/`:

| Binary | Purpose |
|---|---|
| `tamarackdb-server` | The HTTP server |
| `tamarackdb-migrate` | Standalone schema migration tool |
| `tamarackdb-init` | Creates a new data directory |
| `tamarackdb-backup` | Copies new events from a remote instance into a local backup file |

Each binary also has its own target with the same name, so `make tamarackdb-migrate`
builds only that one.

The running build's version comes from `git describe --tags --always --dirty`,
evaluated at build time. It is baked into every binary at build time, not read at
runtime. See [design.md](design.md#versioning) for how that works.

`--version` prints the running build's version and exits, without loading a config
file or opening the database. Every TamarackDB binary accepts it.

## Test

```sh
make test
```

## Demo dataset

`cmd/tamarackdb-demo` fills a data directory with a large set of made-up data.
Events get random types, one or two identifiers, one metadata tag, and filler
text as payload. Documents get a random type, a numeric id, and longer filler
text as payload; each is created at version 1. The tool writes straight to the
store, not through the HTTP server, so it can seed a large database fast. Use it
to try `QUERY /events` and `GET /documents/{type}/{id}` at scale.

```sh
make tamarackdb-demo
./bin/tamarackdb-demo --dataDir /path/to/data --events 1000000 --documents 100000 --seed 1
```

`--events` sets how many events to write (default 1000000). `--documents` sets
how many documents to create (default 0). Set one of them to 0 to seed only the
other. `--seed` makes the run repeatable.

Document ids always run from 1 to `--documents`. A second run that creates
documents on the same data directory fails with a conflict, so start from an
empty directory when you need fresh documents.

## Other Makefile targets

- `make run`: build, then start the server with the default config
- `make fmt` / `make vet` / `make tidy`: standard Go housekeeping
- `make clean`: removes `bin/`
