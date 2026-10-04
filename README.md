![TamarackDB](docs/static/tamarackdb-logo.png)

TamarackDB is an open source event store written in Go. It follows the
[DCB specification](https://dcb.events/specification/), serves an HTTP
API, and keeps its data in one SQLite file.

[![release](https://img.shields.io/github/v/release/tamarackdb/tamarackdb)](https://github.com/tamarackdb/tamarackdb/releases/latest)
[![ci](https://github.com/tamarackdb/tamarackdb/actions/workflows/ci.yml/badge.svg)](https://github.com/tamarackdb/tamarackdb/actions/workflows/ci.yml)
[![license](https://img.shields.io/github/license/tamarackdb/tamarackdb)](LICENSE)

## Features

- DCB: optimistic concurrency on writes, checked against the events a
  command read.
- HTTP API: send plain JSON requests from any language or platform.
- Transactions: a command's decisions, events, and projections are
  written together, or not at all.
- Simple deployment: static Linux binaries with no runtime and no
  external service to install.
- SQLite storage: events and projections live in one SQLite file, easy
  to inspect.
- Built-in backup: keep an incremental copy of an instance's events.
- Authentication: an optional bearer token.
- Monitoring: a health check endpoint and server counters.

## Documentation

The full documentation is at <https://tamarackdb.github.io/>.

## Contributing

TamarackDB is under active development. Issues and pull requests are
welcome; see [CONTRIBUTING.md](CONTRIBUTING.md).

---

*TamarackDB takes its name from the tamarack (*Larix laricina*), a
conifer native to Quebec's boreal forest, whose growth rings are clear
and easy to read. Each ring records one season, laid down once and never
changed. You can read the tree's whole history by reading the rings from
the center out. This event store works the same way: an ordered,
append-only list of facts that never change, from which you rebuild
current state by replaying them.*
