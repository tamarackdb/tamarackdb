# Deploying TamarackDB

This is for whoever runs a TamarackDB instance: configuring it, starting it, and
watching it run. For how to build it, see [build.md](build.md). For how to call its
HTTP API, see [integration.md](integration.md). For backing up an instance, see
[backup.md](backup.md).

## Configure

Generate a starter `config.toml` and adjust it as needed:

```sh
./bin/tamarackdb-server --default-config > config.toml
```

The file uses TOML, so lines can be commented out with `#`. Configuration
lives under a `[server]` section, so the same file can also hold
`tamarackdb-backup`'s `[backup]` section (see [backup.md](backup.md)):

```sh
./bin/tamarackdb-backup --default-config >> config.toml
```

Each binary reads only its own section and ignores the rest, so a shared
file works whether you run one binary or both.

| Key | Environment variable | Default | Description |
|---|---|---|---|
| `socketPath` | `TAMARACKDB_SOCKET_PATH` | `/var/run/tamarackdb-server.sock` | Unix socket the server listens on |
| `bindAddress` / `port` | `TAMARACKDB_BIND_ADDRESS` / `TAMARACKDB_PORT` | none / none | Address and port the server listens on instead of a unix socket |
| `enableTls` / `tlsCertFile` / `tlsKeyFile` | `TAMARACKDB_ENABLE_TLS` / `TAMARACKDB_TLS_CERT_FILE` / `TAMARACKDB_TLS_KEY_FILE` | `false` / none / none | TLS termination (Go's own `ListenAndServeTLS`, no reverse proxy) |
| `enableAuth` / `authToken` | `TAMARACKDB_ENABLE_AUTH` / `TAMARACKDB_AUTH_TOKEN` | `false` / none | Bearer token check on every endpoint |
| `dataDir` | `TAMARACKDB_DATA_DIR` | `data` | Directory holding all of TamarackDB's data. Its internal layout is managed by TamarackDB and may change between versions: don't rely on it, and don't edit its contents directly |
| `defaultLimit` / `maxLimit` | `TAMARACKDB_DEFAULT_LIMIT` / `TAMARACKDB_MAX_LIMIT` | `1000` / `10000` | Default and maximum page size for `QUERY /events` |
| `maxEventSize` | `TAMARACKDB_MAX_EVENT_SIZE` | `65536` (64 KiB) | Maximum size in bytes of a single event |
| `maxDocumentSize` | `TAMARACKDB_MAX_DOCUMENT_SIZE` | `65536` (64 KiB) | Maximum size in bytes of a single document's payload |
| `maxDocumentsPerWrite` | `TAMARACKDB_MAX_DOCUMENTS_PER_WRITE` | `100` | Maximum documents in a single `POST /write` call |
| `maxQueuedWriters` | `TAMARACKDB_MAX_QUEUED_WRITERS` | `100` | Maximum writers waiting to write at once |
| `readPoolSize` | `TAMARACKDB_READ_POOL_SIZE` | `8` | SQLite connections available for `/events`, and so how many can run at once |
| `devMode` | `TAMARACKDB_DEV_MODE` | `false` | Turns on `DELETE /events` (wipes every event) and `/debug/pprof/*` (profiling endpoints). Never enable this in production. |

By default, TamarackDB listens on a unix socket instead of a TCP port. This
keeps it off the network entirely unless you opt in, the way MySQL's own
default socket does. Set `bindAddress` or `port` to switch to TCP instead;
`socketPath` wins whenever it's set, even alongside `bindAddress`/`port`, and
`enableTls` is ignored in that case, since a unix socket is already local to
the host.

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

## Run

```sh
./bin/tamarackdb-init --dataDir /path/to/data
./bin/tamarackdb-server --config /path/to/config.toml
```

Once running, the server logs one line per request to stdout: method, path, status
code, response size, and time taken, e.g. `tamarackdb-server: POST /write 200 42B
1.23ms`. It opens everything it needs under `dataDir` (creating it, with its
schema, if it doesn't exist yet), so the documents mechanism (see
[design.md](design.md#documents)) is ready without a separate provisioning
step.

## Provisioning and migration

`tamarackdb-init` creates a new data directory, as shown above. `tamarackdb-migrate` brings an existing events database up to
the schema this binary expects, run once between a schema change and rolling
out the new server. Point it at a config file so it can find the data
directory:

```sh
./bin/tamarackdb-migrate --config /path/to/config.toml
```

The server itself never changes the schema on its own. See
[design.md](design.md#schema) for why migration is a separate tool.

`tamarackdb-migrate` only ever touches the events database file
(`tamarackdb.sqlite`). The documents database file
(`tamarackdb-documents.sqlite`) has never had a schema change, so there's
nothing yet for it to do there; the server creates it itself on startup if
it's missing (see Run above).

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
both SQLite files across restarts.

To use a unix socket instead, e.g. for a reverse proxy container in the same
pod or `docker-compose` setup, override `TAMARACKDB_SOCKET_PATH`: it wins over
the image's own `TAMARACKDB_BIND_ADDRESS`/`TAMARACKDB_PORT`. Mount a shared
volume for the socket path so the other container can reach it.

`tamarackdb-migrate` and `tamarackdb-init` are also in the image, for running
against the mounted volume:

```sh
docker exec <container> ./tamarackdb-init --dataDir /data
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
{ "status": "ok", "version": "1.2.3" }
```

This confirms the process is up and SQLite is reachable. Point a process
supervisor or load balancer at it. `curl --unix-socket` works the same way
against any endpoint below, not just `/health`.

## Observability

Two more endpoints show the server's own in-memory state. They matter when you are
chasing a slow or stuck `write`, or a read connection pool that looks saturated,
not during normal `events`/`write` use.

- `GET /metrics`: Prometheus text format. Shows whether a writer is active right
  now, how many writes are queued, the longest current wait, write/failure
  counts, and how many documents have had a best-effort payload write fail
  (see [design.md](design.md#documents)).
- `GET /debug`: a JSON snapshot with a `write` object (the current active writer,
  if any, every queued writer with its wait time, and the write SQLite pool's
  usage) and a `read` object (in-flight `/events` requests and the read SQLite
  pool's usage, sized by `readPoolSize` below).

See [design.md](design.md#queue-and-connection-pool-observability) for the exact
metric names and JSON shape.

## Developer mode

`devMode` (see Configure above) turns on two things at once, both meant for a
local instance or a controlled troubleshooting session, never a production
deployment:

- `DELETE /events`, which wipes every event; documents are untouched.
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

While testing an integration, watch the server's stdout: one line per request, with
method, path, status code, response size, and time taken, e.g. `tamarackdb-server:
POST /write 200 42B 1.23ms`. At startup, it also prints a banner and its resolved
configuration (bind address, port, data directory, limits, and so on), so you
can confirm what a given instance is actually running with.
