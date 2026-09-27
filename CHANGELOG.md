# Changelog

This file follows [Keep a Changelog](https://keepachangelog.com), and
TamarackDB follows [Semantic Versioning](https://semver.org). Before 1.0, a
minor version may break the API.

## [Unreleased]

### Fixed

- A boolean set to `false` in `config.toml` (`enableTls`, `enableAuth`,
  `devMode`) now wins over a `TAMARACKDB_*` variable set to `true`, as
  documented.
- The server no longer deletes a file or a directory found at
  `socketPath`. It removes only a socket left by an earlier run, and
  refuses to start otherwise.
- On shutdown, requests waiting for their turn are turned away right away.
  The server no longer gives out tickets that no client can use, and no
  longer waits up to 10 seconds for them.
- `GET /debug` writes its times in UTC with 6 fractional digits, like an
  event's `time`.
- `tamarackdb-backup` creates the directory of `databasePath` if needed. A
  first run with the default settings used to fail.
- `tamarackdb-backup` gives up on a page request after 5 minutes, instead
  of hanging and holding the backup file's lock.
- `tamarackdb-backup --default-config` lists `sourceToken`.
- `tamarackdb-init --version` in the Docker image prints the release
  version instead of `dev`.

## [0.19.0] - 2026-09-26

### Added

- Transactions that span several HTTP calls. `POST /begin` returns a ticket,
  carried by every call of the transaction in the `X-Tamarackdb-Ticket`
  header, and `POST /commit` or `POST /rollback` ends it. A command's
  decision models and event handlers read what it just appended, then
  everything commits together, or nothing does.
- One transaction at a time, holding the write lock until it ends. Other
  `POST /begin` requests wait in arrival order, up to
  `maxQueuedTransactions`. A waiting client ends its wait by closing the
  connection.
- An idle timeout (`transactionTimeout`, 5 seconds by default) and a total
  ceiling (`maxTransactionDuration`, 15 seconds by default) roll back a
  transaction whose client crashed or hangs. The server logs the ticket of
  an expired transaction.
- `POST /events` appends events inside a transaction, with an optional
  Append Condition (DCB). Each event gets a Sequence Position and a `time`
  set by the server.
- `QUERY /events` reads events as NDJSON, with a query grammar over types,
  identifiers, and metadata, `afterSequence` and `time` filters, and
  pagination. With a ticket, it also sees the events appended earlier in
  the same transaction.
- Optional projections, identified by `type` + `id`, written with
  `POST /projections` in the same transaction as the events. A call takes
  `create`, `replace`, and `delete` lists. Every projection has a version,
  a random UUID returned in `X-Tamarackdb-Version`; a write with a stale
  version gets `409 ConcurrencyException`.
- Projection rebuilds while the server is paused: `POST /pause`,
  `DELETE /projections[/{type}]`, `POST /projections` without a ticket,
  `POST /resume`. The pause survives a restart.
- `GET /health`, `GET /metrics` (Prometheus), and `GET /debug` for
  observability.
- Bearer token authentication and TLS.
- `POST /reset` with `devMode` on, to empty the store between test runs.
- `tamarackdb-init` to create a data directory, `tamarackdb-backup` for
  incremental backups of the events, and `tamarackdb-demo` to seed a large
  made-up dataset.

[Unreleased]: https://github.com/tamarackdb/tamarackdb/compare/v0.19.0...HEAD
[0.19.0]: https://github.com/tamarackdb/tamarackdb/releases/tag/v0.19.0
