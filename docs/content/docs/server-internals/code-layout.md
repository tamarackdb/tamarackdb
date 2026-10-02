---
title: "Code layout"
slug: "code-layout"
weight: 7
---

Where each part of TamarackDB lives in the Go code.

| Package | Role |
|---|---|
| `cmd/tamarackdb-server` | The server binary: configuration, startup, signals, ordered shutdown, the hourly `PRAGMA optimize` |
| `cmd/tamarackdb-init` | Creates an empty database file |
| `cmd/tamarackdb-backup` | The backup tool (see [Backup](/docs/backup/)) |
| `cmd/tamarackdb-demo` | Fills a data directory with made-up data |
| `internal/api` | The HTTP API: routing, decoding, validation, errors, logging, `/metrics`, `/debug` |
| `internal/queue` | The FIFO (see [The write FIFO](/docs/server-internals/write-fifo/)) |
| `internal/txn` | The write manager: a turn in the FIFO for each write, bulk delete, reset, and `PRAGMA optimize`, and the write counters |
| `internal/store` | SQLite: the transaction of a write, reads, the store ID, the Sequence Position counter, the query-to-SQL translation |
| `internal/dcb` | Events, tags, queries, Append Conditions, and their validation |
| `internal/projection` | The projection wire shape and its validation, independent of `internal/dcb` |
| `internal/config` | Loading and checking the configuration of the server and of the backup |
| `internal/ndjson` | Writing NDJSON lines |
| `internal/buildinfo` | The version string, set at build time |

The shared matcher cases live in `testdata/query-cases.json` (see
[Query grammar](/docs/http-api/query-grammar/#shared-test-cases)).
