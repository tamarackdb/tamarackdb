---
title: "Deployment"
slug: "deployment"
weight: 1
---

This is for whoever runs a TamarackDB instance: configuring it, starting it, and
watching it run. For how to build it, see [Building from source](/docs/contributing/building-from-source/). For how to call its
HTTP API, see [Integration](/docs/guides/integration/). For backing up an instance, see
[Backup](/docs/guides/backup/).

The two sections below get you started, on your own machine or in production.
The rest of the page covers each setting and operation in detail.

## Local development

To code against a local instance, run the published image:

```sh
docker run -d --rm --name tamarackdb -p 127.0.0.1:8085:8085 \
  -e TAMARACKDB_DEV_MODE=true \
  -e TAMARACKDB_LOG_LEVEL=debug \
  ghcr.io/tamarackdb/tamarackdb:latest
```

- The API is at `http://127.0.0.1:8085`, reachable from your machine only.
- `TAMARACKDB_DEV_MODE=true` turns on `POST /reset`, to start each test run
  from an empty store (see [Developer mode](#developer-mode)).
- `TAMARACKDB_LOG_LEVEL=debug` logs every request, which helps while you
  write the integration (see [Logs](#logs)). Read them with
  `docker logs -f tamarackdb`.
- The data lives in the container's own volume, and `--rm` deletes it when
  the container stops (`docker stop tamarackdb`). Mount a named volume on
  `/data` to keep it across runs (see [Docker](#docker)).

Authentication stays off: the port only listens on your own machine.

## Production

Check each point before an instance holds real data:

1. **A dedicated user.** Run the server as its own system user, never as
   root, and run every command that touches `dataDir` as that user (see
   [Run](#run)).
2. **A private data directory.** `dataDir` is readable by the server's user
   only (`0700`). The server creates it that way; give a directory you create
   yourself the same permissions (see [Configure](#configure)).
3. **The same host, over the unix socket.** Run TamarackDB next to the
   application, on the socket. If the application runs as another user, set
   `socketMode = "0660"` and add that user to the server's group (see
   [Configure](#configure)).
4. **A reverse proxy and a token over the network.** If the application must
   reach TamarackDB from another host, put a reverse proxy in front of the
   socket to handle TLS, and turn `enableAuth` on (see
   [Configure](#configure)).
5. **A protected configuration file.** A `config.toml` that holds `authToken`
   is readable by the server's user only (`chmod 600`).
6. **No developer mode.** `devMode` stays off: it exposes `POST /reset`,
   which deletes every event (see [Developer mode](#developer-mode)).
7. **A sized queue.** Set `maxQueuedTransactions` from how many users the
   application serves at once (see
   [Sizing the transaction queue](#sizing-the-transaction-queue)).
8. **Monitoring.** Point a supervisor at `/health`, scrape `/metrics`, and
   keep `logLevel` at `warning` (see [Health check](#health-check),
   [Observability](#observability), and [Logs](#logs)).
9. **Backups.** Schedule `tamarackdb-backup`. On the same host, it reads
   straight from the socket with `sourceSocket`; from another host, through a
   reverse proxy that handles TLS (see [Backup](/docs/guides/backup/)).

A systemd unit for the server, running as user `tamarackdb`:

```ini
# /etc/systemd/system/tamarackdb.service
[Unit]
Description=TamarackDB
After=network.target

[Service]
User=tamarackdb
Group=tamarackdb
ExecStart=/usr/local/bin/tamarackdb-server --config /etc/tamarackdb/config.toml
Restart=on-failure
# Creates /run/tamarackdb for the socket, and /var/lib/tamarackdb as 0700,
# both owned by the user above.
RuntimeDirectory=tamarackdb
RuntimeDirectoryMode=0755
StateDirectory=tamarackdb
StateDirectoryMode=0700

[Install]
WantedBy=multi-user.target
```

With this `config.toml`, readable by `tamarackdb` only:

```toml
[server]
socketMode = "0660"
dataDir = "/var/lib/tamarackdb"
```

The socket stays at its default path, `/run/tamarackdb/tamarackdb.sock`, in the
directory systemd creates.

To create the user and start the service:

```sh
sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin tamarackdb
sudo systemctl enable --now tamarackdb
```

On `systemctl stop`, systemd sends `SIGTERM`, and the server shuts down in
order: it rolls back the active transaction, if any, and never commits one on
its own.

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
: Unix socket the server listens on. The server must be able to create it: the default's directory, `/run/tamarackdb`, must exist and be writable by the server's user. Under systemd, `RuntimeDirectory=tamarackdb` creates it (see [Production](#production)); elsewhere, create it yourself, or set a path the server's user owns. On startup, the server removes a socket left at that path by an earlier run, and refuses to start if the path holds anything other than a socket. The path can be at most 107 bytes long, the limit Linux sets for a unix socket.
: Env: `TAMARACKDB_SOCKET_PATH`
: Default: `/run/tamarackdb/tamarackdb.sock`

`socketMode`
: Permissions of the unix socket, as an octal string, set right after the server creates it. Only used with `socketPath`.
: Env: `TAMARACKDB_SOCKET_MODE`
: Default: `"0600"`

`bindAddress` / `port`
: Address and port the server listens on instead of a unix socket.
: Env: `TAMARACKDB_BIND_ADDRESS` / `TAMARACKDB_PORT`
: Default: `127.0.0.1` / `8085`, used only once either one is set; otherwise the server listens on `socketPath`

`enableAuth` / `authToken`
: Bearer token check on every endpoint.
: Env: `TAMARACKDB_ENABLE_AUTH` / `TAMARACKDB_AUTH_TOKEN`
: Default: `false` / none

`dataDir`
: Directory holding all of TamarackDB's data. Its internal layout is managed by TamarackDB and may change between versions: don't rely on it, and don't edit its contents directly. The database in it can be read by anyone with access to the files, whatever `enableAuth` says, so the server creates a missing directory as `0700` and a new database as `0600`. If you create the directory yourself, give it the same `0700`: the server never changes the permissions of a directory that already exists.
: Env: `TAMARACKDB_DATA_DIR`
: Default: `data`

`defaultEventsPerPage` / `maxEventsPerPage`
: Events per `QUERY /events` page: the `limit` used when a request leaves it out, and the highest `limit` a request may ask for.
: Env: `TAMARACKDB_DEFAULT_EVENTS_PER_PAGE` / `TAMARACKDB_MAX_EVENTS_PER_PAGE`
: Default: `1000` / `10000`

`maxEventSize`
: Maximum size in bytes of a single event.
: Env: `TAMARACKDB_MAX_EVENT_SIZE`
: Default: `65536` (64 KiB)

`maxProjectionSize`
: Maximum size in bytes of a single projection: the combined length of its `type`, `id`, and `payload`.
: Env: `TAMARACKDB_MAX_PROJECTION_SIZE`
: Default: `65536` (64 KiB)

`maxProjectionsPerRequest`
: Maximum projections in a single `POST /projections` call.
: Env: `TAMARACKDB_MAX_PROJECTIONS_PER_REQUEST`
: Default: `100`

The server also caps every request body, at a size derived from `maxEventSize`,
`maxProjectionSize`, and `maxProjectionsPerRequest`, so raising those limits
raises the cap with them. It prints the cap at startup as `maxRequestBody`
(40,779,776 bytes, about 39 MiB, by default). See
[Architecture](/docs/architecture/#error-responses) for how it's computed.

`transactionTimeout`
: Seconds a transaction may go without a call before it's rolled back. Each call renews it.
: Env: `TAMARACKDB_TRANSACTION_TIMEOUT`
: Default: `5`

`maxTransactionDuration`
: Seconds a transaction may last in total, however many calls it makes. Must be at least `transactionTimeout`.
: Env: `TAMARACKDB_MAX_TRANSACTION_DURATION`
: Default: `15`

`maxQueuedTransactions`
: Maximum requests waiting for their turn at once. One more gets `503 TransactionQueueFull`.
: Env: `TAMARACKDB_MAX_QUEUED_TRANSACTIONS`
: Default: `100`

`readPoolSize`
: SQLite connections available for reads without a ticket (`QUERY /events`, `GET /projections/{type}/{id}`), and so how many can run at once.
: Env: `TAMARACKDB_READ_POOL_SIZE`
: Default: `8`

`devMode`
: Turns on `POST /reset` (deletes every event and projection) and `/debug/pprof/*` (profiling endpoints). Never enable this in production.
: Env: `TAMARACKDB_DEV_MODE`
: Default: `false`

`logLevel`
: Minimum severity for the log lines: `debug`, `info`, `warning`, or `error`.
: Env: `TAMARACKDB_LOG_LEVEL`
: Default: `warning`

By default, TamarackDB listens on a unix socket instead of a TCP port. This
keeps it off the network entirely unless you opt in, the way MySQL's own
default socket does. Set `bindAddress` or `port` to switch to TCP instead;
`socketPath` wins whenever it's set, even alongside `bindAddress`/`port`.
Either way, the server speaks plain HTTP: TLS is a reverse proxy's job (see
below).

Run TamarackDB on the same host as the application, and keep the unix socket.
Every transaction makes several calls, and a unix socket keeps each one short.

Connecting to a unix socket takes write permission on it. With the default
`socketMode` of `"0600"`, only the user the server runs as can connect. If the
application runs as another user, set `socketMode = "0660"`, and add the
application's user to the server's group. For a server running as user and
group `tamarackdb`, and an application running as `www-data`:

```sh
sudo usermod -aG tamarackdb www-data
```

The application picks up its new group once it restarts.

If the application must reach TamarackDB from another host, put a reverse
proxy in front of the socket, and let it handle TLS. Without TLS, request and
response bodies, and the `authToken` itself, travel in clear text over a
network outside your control. A reverse proxy renews its certificates on its
own, while the server keeps running. Caddy, for example, gets and renews its
certificates by itself:

```
tamarackdb.example.com {
    reverse_proxy unix//run/tamarackdb/tamarackdb.sock
}
```

The proxy's user must be allowed by `socketMode`, like the application's. Turn
`enableAuth` on too: once the proxy is up, the API is reachable over the
network, and the token is what keeps others out. The proxy passes the
`Authorization` header through unchanged.

`config.toml` is optional. Any field it leaves out, or the whole file if it's
missing, falls back to the matching `TAMARACKDB_*` environment variable, then to a
built-in default. A value set in `config.toml` always wins over the environment.
An unknown key in the file, or a key outside any section, stops the server at
startup with an error naming it, so a typo never silently leaves a setting at its
default.
Use a `config.toml` file per instance in production. In Docker, plain environment
variables cover a deployment with no file at all. A `config.toml` that holds
`authToken` should be readable by the server's user only:

```sh
chmod 600 /path/to/config.toml
```

### Sizing the transaction queue

Only one transaction runs at a time. The others wait their turn, in the order
they arrived. A healthy transaction ends in well under a second, so the queue
is usually empty or short.

The limits matter when a client fails. A client that crashed holds its
transaction until `transactionTimeout`. A client that keeps calling but never
ends its transaction holds it until `maxTransactionDuration`. A request waiting
behind `N` such transactions may wait up to `N` times `maxTransactionDuration`.

Size `maxQueuedTransactions` against how many users the application serves
at once. There's no "no limit" value: every deployment gets a bound. The
server doesn't cap how long a request waits for its turn: each client sets its
own limit and closes the connection when it's reached.

## Run

Run every command that creates or rewrites files under `dataDir` as the user
the server runs as: the server, `tamarackdb-init`, and any `sqlite3` you run on
the database by hand. The data directory is readable by its owner only, so a
file created by another user, such as root, is one the server can't open, and
it refuses to start. The examples below assume the server runs as a user named
`tamarackdb`:

The default socket sits in `/run/tamarackdb`, which must exist and belong to
that user before the server starts:

```sh
sudo install -d -o tamarackdb -g tamarackdb -m 755 /run/tamarackdb
sudo -u tamarackdb ./bin/tamarackdb-init --data-dir /path/to/data
sudo -u tamarackdb ./bin/tamarackdb-server --config /path/to/config.toml
```

`/run` is emptied at every reboot, so a directory created by hand is gone the
next time the machine starts. For a lasting service, use the systemd unit in
[Production](#production): `User=tamarackdb` replaces `sudo`, and
`RuntimeDirectory=tamarackdb` creates the directory at every start.

Once running, the server logs one line per request to stdout, tagged with a
severity level: method, path, status code, response size, and time taken,
e.g. `tamarackdb-server: [WARNING] POST /begin 503 35B 30001.52ms`. Only lines
at or above `logLevel` are printed; by default that's `warning`, so a plain
successful request or an expected rejection like a concurrency conflict
stays quiet, and only capacity issues and real failures show up. See
[Logs](#logs) for the full list of severities. It opens everything it needs
under `dataDir`, creating it, with its schema, if it doesn't exist yet.

## Provisioning

`tamarackdb-init` creates a new data directory, as shown above. The server
also creates the database on its first start if it doesn't exist yet.

On startup, the server checks that the database's schema version matches the
one built into the binary, and refuses to start if it doesn't. The server
never changes the schema on its own (see
[Architecture](/docs/architecture/#schema)).

## Docker

Each release publishes an image for `linux/amd64` and `linux/arm64`:

```sh
docker run -d -p 127.0.0.1:8085:8085 -v tamarackdb-data:/data ghcr.io/tamarackdb/tamarackdb:latest
```

The container speaks plain HTTP, with `enableAuth` off unless you turn it on,
so the examples publish its port on `127.0.0.1` only. A plain `-p 8085:8085`
would publish it on every interface of the host, and give anyone who can reach
the host full access to the API. To expose it to the network, turn
`enableAuth` on (`TAMARACKDB_ENABLE_AUTH`, `TAMARACKDB_AUTH_TOKEN`), and put a
reverse proxy in front of it for TLS (see [Configure](#configure)).

Use a version tag, such as `ghcr.io/tamarackdb/tamarackdb:v<version>`, to pin a
release (see the [releases](https://github.com/tamarackdb/tamarackdb/releases)).
To build the image from source instead:

```sh
docker build -t tamarackdb .
docker run -d -p 127.0.0.1:8085:8085 -v tamarackdb-data:/data tamarackdb
```

The examples below use the local `tamarackdb` image; replace it with the
published one if that's what you run.

The image is set up entirely through `TAMARACKDB_*` environment variables (see
Configure above); no `config.toml` is needed inside the container. It sets
`TAMARACKDB_BIND_ADDRESS=0.0.0.0` and `TAMARACKDB_PORT=8085` itself, so it
listens over TCP, on port `8085`, unlike a plain `tamarackdb-server` binary.
The unix socket is for a server installed directly on the host (see
[Production](#production)); in a container, use TCP. It also sets `TAMARACKDB_DATA_DIR=/data`, so mount a volume on `/data` to keep
the database, and the pause state (see [Pause](#pause)), across restarts.

The server runs as user and group `tamarackdb`, UID and GID `10001`. A named
volume, as above, works as is. To mount a directory of the host instead, give
it to that UID first, readable by it only:

```sh
sudo install -d -o 10001 -g 10001 -m 700 /srv/tamarackdb
docker run -d -p 127.0.0.1:8085:8085 -v /srv/tamarackdb:/data tamarackdb
```

`tamarackdb-init` is also in the image. The server creates its database on its
first start anyway, so you only need it to prepare a volume before that first
start:

```sh
docker run --rm -v tamarackdb-data:/data --entrypoint ./tamarackdb-init tamarackdb --data-dir /data
```

### Alongside the application

When the application runs in a container too, put both on the same Docker
network, and publish no port at all: the application reaches TamarackDB by its
service name, and nothing is reachable from outside the host. With
`docker compose`:

```yaml
services:
  tamarackdb:
    image: ghcr.io/tamarackdb/tamarackdb:latest
    volumes:
      - tamarackdb-data:/data
  app:
    image: my-app
    environment:
      # The application's own setting, whatever it's named.
      TAMARACKDB_URL: http://tamarackdb:8085

volumes:
  tamarackdb-data:
```

Every container on that network can reach the API. If the network holds
containers you don't trust, turn `enableAuth` on (`TAMARACKDB_ENABLE_AUTH` and
`TAMARACKDB_AUTH_TOKEN` on the `tamarackdb` service), and give the token to
the application.

## Health check

```sh
sudo -u tamarackdb curl --unix-socket /run/tamarackdb/tamarackdb.sock http://localhost/health
```

The socket only lets in the users `socketMode` allows (see
[Configure](#configure)): the server's own user by default, hence the
`sudo -u`.

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
sudo -u tamarackdb curl --unix-socket /run/tamarackdb/tamarackdb.sock -X POST http://localhost/resume
```

Use `POST /resume`, not a manual delete of the pause file: the running server
keeps the pause in memory, and only reads the file at startup.

### Reclaiming disk space

SQLite reuses the space of deleted projections on its own, so the database file
doesn't keep growing after a rebuild. To give that space back to the
operating system, run a `VACUUM` by hand. The server must be stopped: it never
runs one itself.

Do it at the end of a rebuild, while the application is already down:

1. Stop `tamarackdb-server`.
2. Run the `VACUUM`, as the server's user (see [Run](#run)):

   ```sh
   sudo -u tamarackdb sqlite3 /path/to/data/tamarackdb.sqlite 'VACUUM;'
   ```

3. Start `tamarackdb-server` again. It starts paused, since the pause file is
   still there.
4. Resume the server with `POST /resume`.

A `VACUUM` rewrites the whole file, events included, and needs free disk space
about the size of the database while it runs.

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

- `POST /reset`, which deletes every event and every projection, and cuts off
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
`tamarackdb-server: [WARNING] POST /begin 503 35B 30001.52ms`. By default only
`warning` and `error` lines print; set `logLevel` to `debug` to see every
request, including successful ones, while testing an integration.

Each outcome carries a fixed level, not derived from the status code alone:

| Outcome | Status | Level |
|---|---|---|
| Successful request | 2XX | `debug` |
| Projection not found | 404 | `debug` |
| Concurrency conflict | 409 | `debug` |
| Invalid request | 400 | `info` |
| Payload too large | 413 | `info` |
| Missing or invalid bearer token | 401 | `info` |
| Call only accepted while paused | 409 | `info` |
| Transaction no longer active | 410 | `info` |
| Server shutting down | 503 | `info` |
| Transaction queue full | 503 | `warning` |
| Server paused | 503 | `warning` |
| Transaction expired | none | `warning` |
| Internal error | 500 | `error` |
| Storage unreachable | 503 | `error` |

A successful request and an expected rejection, such as a concurrency
conflict or a projection that doesn't exist, are both `debug`: the server did
exactly what it was supposed to do. A malformed or oversized request, a bad
token, a call made at the wrong time, or a call on a transaction that already
ended is `info`: not the server's fault, but worth knowing about. So is a
request turned away because the server is shutting down. A full or
slow transaction queue is `warning`: a real signal of capacity or contention.
So is a paused server turning a request away, so a forgotten pause shows up.

A transaction that reaches its idle timeout or its ceiling is rolled back by
the server itself, with no request to log. It gets its own `warning` line
instead, with the transaction's ticket, which limit was reached, and how long
the transaction lasted:

```
tamarackdb-server: [WARNING] transaction a045ad63-5d4b-4847-8eb9-fbddb4e2d65b expired: idle timeout reached after 5.00s
```

If the application logs the ticket it gets for each command, this line tells
you which command it was.

An internal error or an unreachable store is `error`: a real failure.

At startup, the server also prints a banner and its resolved configuration
(bind address, port, data directory, transaction limits, and so on), so you
can confirm what a given instance is actually running with.
