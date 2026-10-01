![](docs/static/tamarackdb-logo.png)

TamarackDB is an open source event store in pure Go, compliant with the [DCB (Dynamic Consistency
Boundaries) specification](https://dcb.events/specification/), accessible via HTTP,
using SQLite as the storage engine.

[![release](https://img.shields.io/github/v/release/tamarackdb/tamarackdb)](https://github.com/tamarackdb/tamarackdb/releases/latest)
[![ci](https://github.com/tamarackdb/tamarackdb/actions/workflows/ci.yml/badge.svg)](https://github.com/tamarackdb/tamarackdb/actions/workflows/ci.yml)
[![license](https://img.shields.io/github/license/tamarackdb/tamarackdb)](LICENSE)

## Features

- Compliant with the DCB specification, with optimistic concurrency on
  writes.
- Full HTTP API to read and write events, with pagination for large
  result sets.
- Optional projection store, written with the events or on its own: each
  command atomic, or eventually consistent, as the application chooses.
- Single-instance design with no external dependency.
- Plain SQLite storage with no opaque format lock-in.
- Bearer token authentication.
- Incremental backup tooling.
- Built-in monitoring and troubleshooting endpoints.

## Documentation

The full documentation is at <https://tamarackdb.github.io/>.

## Contributing

TamarackDB is under active development. Issues and pull requests are
welcome for bug reports and feature ideas; see
[CONTRIBUTING.md](CONTRIBUTING.md).

## Trivia

TamarackDB takes its name from the tamarack (*Larix laricina*), a conifer
native to Quebec's boreal forest. It's one of the few conifers used in
dendrochronology, because its growth rings are unusually clear and easy to
read. Each ring records one season, laid down once and never changed. You can
read the tree's whole history by reading the rings from the center out. This
event store works the same way: an ordered, append-only list of facts that
never change, from which you rebuild current state by replaying them.
