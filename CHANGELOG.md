# Changelog

This file follows [Keep a Changelog](https://keepachangelog.com), and
TamarackDB follows [Semantic Versioning](https://semver.org). Before 1.0, a
minor version may break the API.

## [0.20.0] - 2026-09-27

### Added

- The Docker image is published for `linux/arm64` as well as
  `linux/amd64`.

### Changed

- An unknown key in `config.toml`, or a key outside any section, stops
  `tamarackdb-server` and `tamarackdb-backup` at startup with an error
  naming it. It used to be ignored.
- A request body over 8 MiB gets `413 PayloadTooLarge`.
- A request body with anything but whitespace after its JSON value gets
  `400 InvalidRequest`.
- A request turned away because the server is shutting down gets
  `503 ShuttingDown` instead of `500 InternalError`.

### Fixed

- A client sending a `POST /projections` body slowly during a rebuild no
  longer holds off `POST /resume`.
- A configuration error no longer names `config.toml` when no such file
  exists.

## [0.19.1] - 2026-09-27

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

[0.20.0]: https://github.com/tamarackdb/tamarackdb/releases/tag/v0.20.0
[0.19.1]: https://github.com/tamarackdb/tamarackdb/releases/tag/v0.19.1
[0.19.0]: https://github.com/tamarackdb/tamarackdb/releases/tag/v0.19.0
