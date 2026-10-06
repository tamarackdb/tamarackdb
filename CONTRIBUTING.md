# Contributing to TamarackDB

TamarackDB is pre-1.0 and under active development. Issues and pull
requests are welcome. Breaking changes to schema, config, or internal
APIs can happen at any time; keep that in mind when proposing a
change.

## Reporting bugs and proposing ideas

Open an issue. Include enough detail to reproduce a bug, or explain the
problem an idea would solve.

## Pull requests

- Open an issue first for anything beyond a small fix, so the approach
  can be discussed before you spend time on it.
- Keep pull requests focused on one change.
- Run `make fmt`, `make vet`, and `make test` before opening a pull
  request. The CI also rejects code that isn't formatted with `gofmt`,
  and runs the tests with the race detector (`make test-race`).
- Follow the existing code and documentation style in the repository.

The maintainer reviews every pull request, and may push changes
directly to your branch rather than asking for another round of edits
(GitHub enables this by default for pull requests from forks). Merge
and release decisions rest with the maintainer, on no fixed schedule.

## Concurrent code

Concurrent code is hard to read, to test, and to reason about. These
recommendations help pick the simplest design. They aren't rules to
follow to the letter: when two of them disagree, the first one wins.
TamarackDB isn't built for many concurrent writes: SQLite has a single
writer, so a design doesn't have to shine under a heavy write load.

1. **A rare case must be safe, not pleasant.** Nothing is lost,
   nothing stays blocked, nothing leaks. Beyond that, what matters is
   who pays. If only the client at fault pays, the simplest behavior is
   enough. If other clients pay, handle the case, however rare. For
   example, a request that joined the FIFO runs even if its client
   leaves: only that client has to deal with a lost response.
2. **Sequential by default.** Do the work in the request that needs
   it, when it needs it. Add a goroutine only when doing the work in
   sequence would make one client pay for another. It then has an owner
   that stops it and waits for it to end. Idle transactions expire when
   the registry is next used, and `PRAGMA optimize` runs at startup and
   on `POST /optimize`: neither needs a goroutine. What stays parallel
   is one goroutine per HTTP request and the reads, since in sequence
   every client would wait for every other.
3. **Exclude a race rather than repair it.** Use a lock, the FIFO, an
   order imposed on the client (written on the documentation site), or
   refuse the state that allows the race. Check and act under the same
   lock, or in the same turn. Every lock says what it guards, in the
   comment of its field. Code that detects a race after the fact is
   hard to reason about and to test. For example, a second call on a
   busy transaction gets `409 TransactionBusy` instead of waiting, so
   no call ever has to check what another one did to the transaction.
4. **Don't wait for another client.** A request whose end depends on
   another client answers "not yet", and the client calls again:
   `POST /pause` answers `202` while transactions are open. A short,
   bounded wait, like a turn in the FIFO, is fine.

What a client sees when it leaves mid-request is part of the API: it's
on the site, under "The client leaving" in the HTTP API conventions.

## Building from source

For how to run TamarackDB, see the documentation at
<https://tamarackdb.github.io/>.

### Build

```sh
make build
```

This builds three binaries under `bin/`:

| Binary | Purpose |
|---|---|
| `tamarackdb-server` | The HTTP server |
| `tamarackdb-init` | Creates a new database, which the server needs to start |
| `tamarackdb-backup` | Copies new events from a remote instance into a local backup file |

Each binary also has its own target with the same name, so
`make tamarackdb-init` builds only that one.

The Makefile bakes the output of `git describe --tags --always --dirty`
into every binary, by setting `internal/buildinfo.Version` through
`-ldflags "-X ..."`. The version is not read at runtime.

`--version` prints the running build's version and exits, without
loading a config file or opening the database. Every TamarackDB binary
accepts it.

### Test

```sh
make test
```

For concurrent code, also run `make test-race` after every change.
Each mechanism that excludes a race has a test that forces the
interleaving it excludes: the test holds the turn of the FIFO
(`holdTurn`), or moves a test clock, instead of counting on chance or
on `time.Sleep`. Check that the test fails without the fix: a test of
concurrent code can pass without testing anything.

### Demo dataset

`cmd/tamarackdb-demo` fills a data directory with a large set of
made-up data. Events get random types, one or two identifiers, one
metadata tag, and filler text as payload. Projections get a random
type, a numeric id, and longer filler text as payload. The tool writes
straight to the store, not through the HTTP server, so it can seed a
large database fast. Use it to try `QUERY /events` and
`GET /projections/{type}/{id}` at scale, or to test a client library
against realistic volume.

```sh
make tamarackdb-demo
./bin/tamarackdb-demo --data-dir /path/to/data --events 1000000 --projections 100000 --seed 1
```

`--events` sets how many events to write (default 1000000).
`--projections` sets how many projections to create (default 0). Set
one of them to 0 to seed only the other. `--seed` makes the run
repeatable.

Projections have types `ProjectionType1` to `ProjectionType5`, and ids
always run from 1 to `--projections`. Each id exists under only one of
those types, picked at random. A second run on the same data directory
replaces the projections of the first one.

Run it before starting `tamarackdb-server`, or against a separate data
directory.

### Other Makefile targets

- `make run`: build, then start the server. Each setting comes from
  `config.toml` at the repository root if it sets it, then from its
  `TAMARACKDB_*` environment variable, then from its built-in default.
  The default `socketPath` sits in `/run/tamarackdb`, which usually
  doesn't exist on a development machine: set `socketPath`, or
  `bindAddress` and `port`. Create the database once first with
  `./bin/tamarackdb-init --data-dir <dir>`, using the same directory as
  `dataDir`.
- `make test-race`: run the tests with Go's race detector, as the CI
  does.
- `make fmt` / `make vet` / `make tidy`: standard Go housekeeping.
- `make clean`: removes `bin/`.

### Documentation site

The documentation is a [Hugo](https://gohugo.io/) site in `docs/`,
using the [Doks](https://getdoks.org/) theme. You need Hugo extended
and Node.js 20 or later. From `docs/`:

```sh
npm ci
npm run dev
```

`npm run dev` serves the site at `http://localhost:1313/` and reloads
on every change. `npm run build` writes the static site to
`docs/public/`.

The site is published to <https://tamarackdb.github.io/> each time a
`v*` tag is pushed. A maintainer can also publish it by hand with
`gh workflow run docs.yml --ref <branch-or-tag>`. The writing rules for
the documentation are in [CLAUDE.md](CLAUDE.md).
