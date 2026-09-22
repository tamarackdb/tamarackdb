![](docs/tamarackdb-logo.png)

TamarackDB is an event store in pure Go, compliant with the [DCB (Dynamic Consistency
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
- Incremental backup and schema migration tooling.
- Built-in monitoring and troubleshooting endpoints.

## Documentation

- [Build](docs/build.md): how to build and test TamarackDB from source.
- [Deployment](docs/deploy.md): how to configure, run, and deploy an instance.
- [Integration](docs/integration.md): how to read and write events and
  documents over HTTP, for apps and client libraries.
- [Backup](docs/backup.md): how to keep a local copy of an instance's events.
- [Design](docs/design.md): how TamarackDB is built inside.

## Contributing

TamarackDB is under active development. Issues and pull requests are
welcome for bug reports and feature ideas; see
[CONTRIBUTING.md](CONTRIBUTING.md).
