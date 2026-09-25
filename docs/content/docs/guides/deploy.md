---
title: "Deployment"
slug: "deployment"
weight: 1
---

This is for whoever runs a TamarackDB instance: configuring it, starting it, and
watching it run. For how to build it, see [Building from source](/docs/contributing/building-from-source/). For how to call its
HTTP API, see [Integration](/docs/guides/integration/). For backing up an instance, see
[Backup](/docs/guides/backup/).

## Configure

Generate a starter `config.toml` and adjust it as needed:

```sh
./bin/tamarackdb-server --default-config > config.toml
```

The file uses TOML, so lines can be commented out with `#`. Configuration
lives under a `[server]` section, so the same file can also hold
`tamarackdb-backup`'s `[backup]` section (see [Backup](/docs/guides/backup/)):

```sh
./bin/tamarackdb-backup --default-config >> config.toml
```

Each binary reads only its own section and ignores the rest, so a shared
file works whether you run one binary or both.

`socketPath`
: Unix socket the server listens on.
: Env: `TAMARACKDB_SOCKET_PATH`
: Default: `/var/run/tamarackdb-server.sock`

`bindAddress` / `port`
: Address and port the server listens on instead of a unix socket.
: Env: `TAMARACKDB_BIND_ADDRESS` / `TAMARACKDB_PORT`
: Default: `127.0.0.1` / `8085`, used only once either one is set; otherwise the server listens on `socketPath`

`enableTls` / `tlsCertFile` / `tlsKeyFile`
: TLS termination (Go's own `ListenAndServeTLS`, no reverse proxy).
: Env: `TAMARACKDB_ENABLE_TLS` / `TAMARACKDB_TLS_CERT_FILE` / `TAMARACKDB_TLS_KEY_FILE`
: Default: `false` / none / none

`enableAuth` / `authToken`
: Bearer token check on every endpoint.
: Env: `TAMARACKDB_ENABLE_AUTH` / `TAMARACKDB_AUTH_TOKEN`
: Default: `false` / none

`dataDir`
: Directory holding all of TamarackDB's data. Its internal layout is managed by TamarackDB and may change between versions: don't rely on it, and don't edit its contents directly.
: Env: `TAMARACKDB_DATA_DIR`
: Default: `data`

`defaultLimit` / `maxLimit`
: Default and maximum page size for `QUERY /events`.
: Env: `TAMARACKDB_DEFAULT_LIMIT` / `TAMARACKDB_MAX_LIMIT`
: Default: `1000` / `10000`

`maxEventSize`
: Maximum size in bytes of a single event.
: Env: `TAMARACKDB_MAX_EVENT_SIZE`
: Default: `65536` (64 KiB)

`maxDocumentSize`
: Maximum size in bytes of a single document's payload.
: Env: `TAMARACKDB_MAX_DOCUMENT_SIZE`
: Default: `65536` (64 KiB)

`maxDocumentsPerWrite`
: Maximum documents in a single `POST /documents` call.
: Env: `TAMARACKDB_MAX_DOCUMENTS_PER_WRITE`
: Default: `100`

`transactionTimeout`
: Seconds a transaction may go without a call before it's rolled back. Each call renews it.
: Env: `TAMARACKDB_TRANSACTION_TIMEOUT`
: Default: `5`

`transactionCeiling`
: Seconds a transaction may last in total, however many calls it makes. Must be at least `transactionTimeout`.
: Env: `TAMARACKDB_TRANSACTION_CEILING`
: Default: `15`

`maxTransactionWait`
: Seconds a `POST /begin` or `POST /pause` may wait for its turn before it gets `503 TransactionWaitTimeout`.
: Env: `TAMARACKDB_MAX_TRANSACTION_WAIT`
: Default: `30`

`maxQueuedTransactions`
: Maximum requests waiting for their turn at once. One more gets `503 TransactionQueueFull`.
: Env: `TAMARACKDB_MAX_QUEUED_TRANSACTIONS`
: Default: `100`

`readPoolSize`
: SQLite connections available for reads without a ticket (`QUERY /events`, `GET /documents/{type}/{id}`), and so how many can run at once.
: Env: `TAMARACKDB_READ_POOL_SIZE`
: Default: `8`

`devMode`
: Turns on `POST /reset` (deletes every event and document) and `/debug/pprof/*` (profiling endpoints). Never enable this in production.
: Env: `TAMARACKDB_DEV_MODE`
: Default: `false`

`logLevel`
: Minimum severity for the log lines: `debug`, `info`, `warning`, or `error`.
: Env: `TAMARACKDB_LOG_LEVEL`
: Default: `warning`

By default, TamarackDB listens on a unix socket instead of a TCP port. This
keeps it off the network entirely unless you opt in, the way MySQL's own
default socket does. Set `bindAddress` or `port` to switch to TCP instead;
`socketPath` wins whenever it's set, even alongside `bindAddress`/`port`, and
`enableTls` is ignored in that case, since a unix socket is already local to
the host.

Run TamarackDB on the same host as the application, and keep the unix socket.
Every transaction makes several calls, and a unix socket keeps each one short.

Turn `enableTls` on whenever TamarackDB runs on a different host than the
application calling it: without it, request and response bodies, and the
`authToken` itself if `enableAuth` is on, travel in clear text over a network
outside your control. It's safe to leave off only when TamarackDB and its
caller share a trust boundary already enforced another way, e.g. both on the
same host, or on a private network segment or VPN.

`config.toml` is optional. Any field it leaves out, or the whole file if it's
missing, falls back to the matching `TAMARACKDB_*` environment variable, then to a
built-in default. A value set in `config.toml` always wins over the environment.
Use a `config.toml` file per instance in production. In Docker, plain environment
variables cover a deployment with no file at all.

### Sizing the transaction queue

Only one transaction runs at a time. The others wait their turn, in the order
they arrived. A healthy transaction ends in well under a second, so the queue
is usually empty or short.

The limits matter when a client fails. A client that crashed holds its
transaction until `transactionTimeout`. A client that keeps calling but never
ends its transaction holds it until `transactionCeiling`. A request waiting
behind `N` such transactions may wait up to `N` times `transactionCeiling`.

Size `maxQueuedTransactions` and `maxTransactionWait` against how many users
the application serves at once, and how long you'd rather they wait than get
an error. There's no "no limit" value: every deployment gets a bound.

## Run

```sh
./bin/tamarackdb-init --data-dir /path/to/data
./bin/tamarackdb-server --config /path/to/config.toml
```

Once running, the server logs one line per request to stdout, tagged with a
severity level: method, path, status code, response size, and time taken,
e.g. `tamarackdb-server: [WARNING] POST /begin 503 38B 30.00s`. Only lines
at or above `logLevel` are printed; by default that's `warning`, so a plain
successful request or an expected rejection like a concurrency conflict
stays quiet, and only capacity issues and real failures show up. See
[Logs](#logs) for the full list of severities. It opens everything it needs
under `dataDir`, creating it, with its schema, if it doesn't exist yet.

## Provisioning and migration

`tamarackdb-init` creates a new data directory, as shown above. `tamarackdb-migrate` brings an existing database up to
the schema this binary expects, run once between a schema change and rolling
out the new server. Point it at a config file so it can find the data
directory:

```sh
./bin/tamarackdb-migrate --config /path/to/config.toml
```

The server itself never changes the schema on its own. See
[Architecture](/docs/architecture/#schema) for why migration is a separate tool.

## Docker

```sh
docker build -t tamarackdb .
docker run -d -p 8085:8085 -v tamarackdb-data:/data tamarackdb
```

The image is set up entirely through `TAMARACKDB_*` environment variables (see
Configure above); no `config.toml` is needed inside the container. It sets
`TAMARACKDB_BIND_ADDRESS=0.0.0.0` and `TAMARACKDB_PORT=8085` itself, so it
listens over TCP by default, unlike a plain `tamarackdb-server` binary. It
also sets `TAMARACKDB_DATA_DIR=/data`, so mount a volume on `/data` to keep
the database, and the pause state (see [Pause](#pause)), across restarts.

To use a unix socket instead, e.g. for a reverse proxy container in the same
pod or `docker-compose` setup, override `TAMARACKDB_SOCKET_PATH`: it wins over
the image's own `TAMARACKDB_BIND_ADDRESS`/`TAMARACKDB_PORT`. Mount a shared
volume for the socket path so the other container can reach it.

`tamarackdb-migrate` and `tamarackdb-init` are also in the image, for running
against the mounted volume:

```sh
docker exec <container> ./tamarackdb-init --data-dir /data
```

## Health check

```sh
curl --unix-socket /var/run/tamarackdb-server.sock http://localhost/health
```

Or, on a TCP deployment:

```sh
curl http://127.0.0.1:8085/health
```

```json
{ "status": "ok", "version": "1.2.3", "paused": false }
```

This confirms the process is up and SQLite is reachable. Point a process
supervisor or load balancer at it. It responds `503 Unavailable` when SQLite
can't be reached. `curl --unix-socket` works the same way against any
endpoint below, not just `/health`.

A paused server is healthy: `/health` still responds `200 OK`, with
`"paused": true`. Don't have a supervisor restart the process on a pause: it
would start paused again (see [Pause](#pause)).

## Pause

The application pauses the server to rebuild its projections (see
[Integration](/docs/guides/integration/#projection-rebuilds)). While paused, the
server gives out no transactions: every `POST /begin` gets `503 Paused`, and
the application can't change anything. Reads still work.

The pause survives a restart. It's stored as a file, `tamarackdb.paused`, in
`dataDir`. A server that starts with that file present starts paused. This is
on purpose: a rebuild interrupted by a crash leaves the server paused, so the
application can't write on half-rebuilt projections until the rebuild is run
again.

To see whether the server is paused, and since when:

- `/health` reports `"paused": true`.
- `/metrics` reports `tamarackdb_paused 1`.
- `/debug` reports `"paused": {"since": "..."}`.

If a rebuild failed and won't be run again, or a pause was left behind by
mistake, end it yourself:

```sh
curl --unix-socket /var/run/tamarackdb-server.sock -X POST http://localhost/resume
```

Use `POST /resume`, not a manual delete of the pause file: the running server
keeps the pause in memory, and only reads the file at startup.

## Observability

Two more endpoints show the server's own in-memory state: the active
transaction, the requests waiting for their turn, the pause, and the SQLite
connection pools. They matter when you are chasing a slow or stuck
transaction, a queue that keeps growing, or a read pool that looks saturated.

- `GET /metrics`: Prometheus text format. Shows whether the server is paused,
  whether a transaction is active, how many requests are waiting and the
  longest current wait, transactions started, committed, and rolled back (by
  reason: client, error, expired, shutdown, reset), how long transactions
  last, and how many appends failed their Append Condition.
- `GET /debug`: a JSON snapshot with the pause state, a `write` object (the
  active transaction, if any, with its deadline, ceiling, and call count;
  every waiting request with its wait time; the write SQLite pool's usage)
  and a `read` object (reads without a ticket in flight, and the read SQLite
  pool's usage, sized by `readPoolSize`).

Neither ever shows a ticket. See
[Architecture](/docs/architecture/#queue-and-connection-pool-observability) for the exact
metric names and JSON shape.

A rising count of `expired` rollbacks means a client is crashing or hanging
in the middle of its transactions. A queue that keeps growing means
transactions take longer than they should, or arrive faster than they end.

## Developer mode

`devMode` (see Configure above) turns on two things at once, both meant for a
local instance or a controlled troubleshooting session, never a production
deployment:

- `POST /reset`, which deletes every event and every document, and cuts off
  the active transaction, if any.
- `/debug/pprof/*`, Go's standard profiling endpoints (CPU, heap, goroutine,
  and so on).

Turn it on only for as long as you need it, then turn it back off.

### Profiling a request

With `devMode` on, start a CPU profile, then trigger the request you want to
look at from another terminal while it collects samples:

```sh
go tool pprof -http=:0 "http://127.0.0.1:8085/debug/pprof/profile?seconds=30"
```

This opens the result as a flame graph once the 30 seconds are up (or once the
request finishes, if that takes longer, in which case raise `seconds`
accordingly). For a request that allocates heavily, such as a `/events` page
with many rows, also check `/debug/pprof/allocs`. For a timeline instead of
an aggregate, use `/debug/pprof/trace?seconds=30` with `go tool trace`.

## Logs

The server logs one line per request to stdout, tagged with a severity level:
method, path, status code, response size, and time taken, e.g.
`tamarackdb-server: [WARNING] POST /begin 503 38B 30.00s`. By default only
`warning` and `error` lines print; set `logLevel` to `debug` to see every
request, including successful ones, while testing an integration.

Each outcome carries a fixed level, not derived from the status code alone:

| Outcome | Status | Level |
|---|---|---|
| Successful request | 2XX | `debug` |
| Document not found | 404 | `debug` |
| Concurrency conflict | 409 | `debug` |
| Invalid request | 400 | `info` |
| Payload too large | 413 | `info` |
| Missing or invalid bearer token | 401 | `info` |
| Call only accepted while paused | 409 | `info` |
| Transaction no longer active | 410 | `info` |
| Transaction queue full | 503 | `warning` |
| Waited too long for a transaction | 503 | `warning` |
| Server paused | 503 | `warning` |
| Transaction expired | none | `warning` |
| Internal error | 500 | `error` |
| Storage unreachable | 503 | `error` |

A successful request and an expected rejection, such as a concurrency
conflict or a document that doesn't exist, are both `debug`: the server did
exactly what it was supposed to do. A malformed or oversized request, a bad
token, a call made at the wrong time, or a call on a transaction that already
ended is `info`: not the server's fault, but worth knowing about. A full or
slow transaction queue is `warning`: a real signal of capacity or contention.
So is a paused server turning a request away, so a forgotten pause shows up.

A transaction that reaches its idle timeout or its ceiling is rolled back by
the server itself, with no request to log. It gets its own `warning` line
instead, saying which limit was reached and how long the transaction lasted.

An internal error or an unreachable store is `error`: a real failure.

At startup, the server also prints a banner and its resolved configuration
(bind address, port, data directory, transaction limits, and so on), so you
can confirm what a given instance is actually running with.
