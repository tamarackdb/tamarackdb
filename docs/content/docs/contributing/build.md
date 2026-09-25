---
title: "Building from source"
slug: "building-from-source"
weight: 1
---

This is for anyone building TamarackDB from source: contributors and packagers.
For how to run it, see [Deployment](/docs/guides/deployment/). For how it works inside, see
[Architecture](/docs/architecture/).

## Build

```sh
make build
```

This builds three binaries under `bin/`:

| Binary | Purpose |
|---|---|
| `tamarackdb-server` | The HTTP server |
| `tamarackdb-init` | Creates a new data directory |
| `tamarackdb-backup` | Copies new events from a remote instance into a local backup file |

Each binary also has its own target with the same name, so `make tamarackdb-init`
builds only that one.

The running build's version comes from `git describe --tags --always --dirty`,
evaluated at build time. It is baked into every binary at build time, not read at
runtime. See [Architecture](/docs/architecture/#versioning) for how that works.

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
./bin/tamarackdb-demo --data-dir /path/to/data --events 1000000 --documents 100000 --seed 1
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

## Documentation site

The documentation is a [Hugo](https://gohugo.io/) site in `docs/`, using the
[Doks](https://getdoks.org/) theme. You need Hugo extended and Node.js 20 or
later. From `docs/`:

```sh
npm ci
npm run dev
```

`npm run dev` serves the site at `http://localhost:1313/` and reloads on every
change. `npm run build` writes the static site to `docs/public/`.

The site is published to <https://tamarackdb.github.io/> each time a `v*` tag
is pushed.
