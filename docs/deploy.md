# Deploying TamarackDB

This is for whoever runs a TamarackDB instance: configuring it, starting it, and
watching it run. For how to build it, see [build.md](build.md). For how to call its
HTTP API, see [integration.md](integration.md). For backing up an instance, see
[backup.md](backup.md).

## Configure

Generate a starter `config.json` and adjust it as needed:

```sh
./bin/tamarackdb-server --default-config > config.json
```

| Key | Environment variable | Default | Description |
|---|---|---|---|
| `bindAddress` / `port` | `TAMARACKDB_BIND_ADDRESS` / `TAMARACKDB_PORT` | none, required | Address and port the server listens on |
| `enableTls` / `tlsCertFile` / `tlsKeyFile` | `TAMARACKDB_ENABLE_TLS` / `TAMARACKDB_TLS_CERT_FILE` / `TAMARACKDB_TLS_KEY_FILE` | `false` / none / none | TLS termination (Go's own `ListenAndServeTLS`, no reverse proxy) |
| `enableAuth` / `authToken` | `TAMARACKDB_ENABLE_AUTH` / `TAMARACKDB_AUTH_TOKEN` | `false` / none | Bearer token check on every endpoint |
| `databasePath` | `TAMARACKDB_DATABASE_PATH` | none, required | Path to the SQLite database file |
| `defaultLimit` / `maxLimit` | `TAMARACKDB_DEFAULT_LIMIT` / `TAMARACKDB_MAX_LIMIT` | `1000` / `10000` | Default and maximum page size for `QUERY /read` |
| `maxEventSize` | `TAMARACKDB_MAX_EVENT_SIZE` | `65536` (64 KiB) | Maximum size in bytes of a single event |
| `maxQueuedWriters` | `TAMARACKDB_MAX_QUEUED_WRITERS` | `100` | Maximum writers waiting to append at once |
| `devMode` | `TAMARACKDB_DEV_MODE` | `false` | Turns on `DELETE /`, which wipes the whole database. Never enable this in production. |

`config.json` is optional. Any field it leaves out, or the whole file if it's
missing, falls back to the matching `TAMARACKDB_*` environment variable, then to a
built-in default. A value set in `config.json` always wins over the environment.
Use a `config.json` file per instance in production. In Docker, plain environment
variables cover a deployment with no file at all.

## Run

```sh
./bin/tamarackdb-init --db /path/to/tamarack.sqlite
./bin/tamarackdb-server --config /path/to/config.json
```

`tamarackdb-server --version` prints the running build's version and exits. It does not
load the config file or open the database.

Once running, the server logs one line per request to stdout: method, path, status
code, and time taken, e.g. `tamarackdb: POST /append 200 1.23ms`.

## Provisioning and migration

`tamarackdb-init` creates a new, empty database file, as shown above.
`tamarackdb-migrate` brings an existing database up to the schema this binary
expects, run once between a schema change and rolling out the new server.
Point it at a config file so it can find the database:

```sh
./bin/tamarackdb-migrate --config /path/to/config.json
```

The server itself never changes the schema on its own. See
[design.md](design.md#schema) for why migration is a separate tool.

## Docker

```sh
docker build -t tamarackdb .
docker run -d -p 8085:8085 -v tamarackdb-data:/data tamarackdb
```

The image is set up entirely through `TAMARACKDB_*` environment variables (see
Configure above); no `config.json` is needed inside the container. The database
file defaults to `/data/tamarack.sqlite`, so mount a volume on `/data` to keep it
across restarts.

`tamarackdb-migrate` and `tamarackdb-init` are also in the image, for running
against the mounted volume:

```sh
docker exec <container> ./tamarackdb-init --db /data/tamarack.sqlite
```

## Health check

```sh
curl http://127.0.0.1:8085/health
```

```json
{ "status": "ok", "version": "1.2.3" }
```

This confirms the process is up and SQLite is reachable. Point a process
supervisor or load balancer at it.

## Observability

Two more endpoints show the server's own in-memory state. They matter when you are
chasing a slow or stuck `append`, not during normal `read`/`append` use.

- `GET /metrics`: Prometheus text format. Shows whether a writer is active right
  now, how many writes are queued, the longest current wait, and write/failure
  counts.
- `GET /debug`: a JSON snapshot of the current active writer, if any, and every
  queued writer with its wait time.

See [design.md](design.md#nice-to-have-queue-observability) for the exact metric
names and JSON shape.

## Logs

While testing an integration, watch the server's stdout: one line per request, with
method, path, status code, and time taken, e.g. `tamarackdb: POST /append 200
1.23ms`. At startup, it also prints a banner and its resolved configuration (bind
address, port, database path, limits, and so on), so you can confirm what a given
instance is actually running with.
