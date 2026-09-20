![](docs/tamarackdb-logo.png)

TamarackDB is an event store in pure Go, compliant with the [DCB (Dynamic Consistency
Boundaries) specification](https://dcb.events/specification/), accessible via HTTP,
using SQLite as the storage engine.

[![release](https://img.shields.io/github/v/release/tamarackdb/tamarackdb)](https://github.com/tamarackdb/tamarackdb/releases/latest)
[![ci](https://github.com/tamarackdb/tamarackdb/actions/workflows/ci.yml/badge.svg)](https://github.com/tamarackdb/tamarackdb/actions/workflows/ci.yml)

## Features

- Compliant with the DCB specification, with optimistic concurrency on
  writes.
- Full HTTP API to read and write events, with pagination for large
  result sets.
- Optional document store, for projections that stay in sync with the
  events that changed them.
- Single-instance design, so consistency never depends on coordination
  across a cluster.
- Single static binary deployment, with no external dependency.
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

## Project status

TamarackDB's source is public, but the project doesn't accept issues or
pull requests at this time; development is managed privately. No support
is offered, and the code is available as-is, at your own risk.
