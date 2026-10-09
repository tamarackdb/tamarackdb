---
title: "Key Features"
description: "The key aspects of TamarackDB: a DCB event store with transactions and projections, served over HTTP, stored in one SQLite file, simple to run."
slug: "key-features"
weight: 1
---

TamarackDB is an open source event store written in Go. It follows the
[DCB specification](https://dcb.events/specification/), serves an HTTP API, and keeps its data in one SQLite file.

## For developers

- **DCB.** A command reads the events it needs, selected by type and tags. Its write is refused if an event matching
  that read was appended in the meantime. There are no streams to design up front. See
  [Concepts](/docs/development/concepts/).
- **Projections.** An application can store the state it computes from events next to them, read by type and id.
  Each write checks the version that was read, so two writes never overwrite each other. Projections can be rebuilt
  from events at any time. See [Concepts](/docs/development/concepts/).
- **Transactions.** A command reads, decides, and writes in a transaction held by the server. At commit, its events
  and projections are written together, or not at all. See [Concepts](/docs/development/concepts/#transactions).
- **HTTP API.** Plain JSON requests, from any language. Reads stream events as NDJSON. See
  [HTTP API](/docs/development/http-api/).
- **Client library.** A PHP client covers the whole API. See [Client Libraries](/docs/development/client-libraries/).

## For operators

- **Simple deployment.** Static Linux binaries for amd64 and arm64, with no runtime and no external service. A Docker
  image and a systemd unit are ready to use. See [Install](/docs/operations/install/).
- **One SQLite file.** Events and projections live in one database file, easy to inspect and to copy.
- **Single instance.** One process and one writer, with no clustering. TamarackDB fits applications with modest write
  throughput.
- **Built-in backup.** `tamarackdb-backup` keeps an incremental copy of an instance's events, from a local or a remote
  instance. See [Backup and Import](/docs/operations/backup/).
- **Import.** `tamarackdb-init --import` builds a new database from a dump of another event store's history. Each
  event keeps its sequence and its time. See [Backup and Import](/docs/operations/backup/).
- **Security.** The server listens on a unix socket by default. A Bearer token is optional, and a reverse proxy adds
  TLS. See [Security](/docs/operations/security/).
- **Monitoring.** `GET /health` for a supervisor or a load balancer, `GET /stats` for counters, and an access log. See
  [Monitoring](/docs/operations/monitoring/).
