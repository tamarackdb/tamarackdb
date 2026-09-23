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
- Optional document store for projections that stay in sync with the
  events that changed them.
- Single-instance design with no external dependency.
- Plain SQLite storage with no opaque format lock-in.
- Authentication and TLS encryption.
- Incremental backup tooling.
- Built-in monitoring and troubleshooting endpoints.

## Documentation

The full documentation is at <https://tamarackdb.github.io/>.

## Contributing

TamarackDB is under active development. Issues and pull requests are
welcome for bug reports and feature ideas; see
[CONTRIBUTING.md](CONTRIBUTING.md).
