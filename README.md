![](docs/static/tamarackdb-logo.png)

TamarackDB is an open source event store in pure Go, compliant with the [DCB (Dynamic Consistency
Boundaries) specification](https://dcb.events/specification/), accessible via HTTP,
using SQLite as the storage engine.

[![release](https://img.shields.io/github/v/release/tamarackdb/tamarackdb)](https://github.com/tamarackdb/tamarackdb/releases/latest)
[![ci](https://github.com/tamarackdb/tamarackdb/actions/workflows/ci.yml/badge.svg)](https://github.com/tamarackdb/tamarackdb/actions/workflows/ci.yml)
[![license](https://img.shields.io/github/license/tamarackdb/tamarackdb)](LICENSE)

## Features

- DCB compliant: follows the DCB specification, with optimistic
  concurrency on writes.
- HTTP API: send plain JSON requests from any language or platform.
- Atomic or eventual projections: store updated projections with new
  events, or catch up later.
- Simple deployment: static Linux binaries with no runtime and no
  external service to install.
- SQLite storage: events and projections live in one SQLite file, easy
  to inspect.
- Built-in backup: keep an incremental copy of an instance's events.
- Bearer token authentication.
- Built-in monitoring and troubleshooting endpoints.

## Documentation

The full documentation is at <https://tamarackdb.github.io/>.

## Contributing

TamarackDB is under active development. Issues and pull requests are
welcome for bug reports and feature ideas; see
[CONTRIBUTING.md](CONTRIBUTING.md).

---

*TamarackDB takes its name from the tamarack (*Larix laricina*), a conifer
native to Quebec's boreal forest. It's one of the few conifers used in
dendrochronology, because its growth rings are unusually clear and easy to
read. Each ring records one season, laid down once and never changed. You can
read the tree's whole history by reading the rings from the center out. This
event store works the same way: an ordered, append-only list of facts that
never change, from which you rebuild current state by replaying them.*
