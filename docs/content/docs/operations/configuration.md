---
title: "Configuration"
description: "Every setting of tamarackdb-server: where settings come from, the full table of keys, environment variables, and defaults, and how to size the limits."
slug: "configuration"
weight: 2
---

Every setting of `tamarackdb-server`. The settings of `tamarackdb-backup` are in
[Backup and Import](/docs/operations/backup/#settings).

## Sources

A setting comes from, in this order:

1. A TOML file, passed with `--config` (`config.toml` in the working directory by default), under a `[server]`
   section.
2. A `TAMARACKDB_*` environment variable.
3. A built-in default.

- The file wins over the environment. It's optional: in Docker, environment variables are enough.
- An unknown key stops the server at startup, with an error naming it.
- The same file can hold the `[backup]` section of `tamarackdb-backup`. Each binary reads only its own section.

Generate a starter file, with every setting at its default:

```sh
tamarackdb-server --default-config > config.toml
tamarackdb-backup --default-config >> config.toml
```

## Settings

| Key | Environment variable | Default | What it sets |
|---|---|---|---|
| `socketPath` | `TAMARACKDB_SOCKET_PATH` | `/run/tamarackdb/tamarackdb.sock` | The unix socket to listen on |
| `socketMode` | `TAMARACKDB_SOCKET_MODE` | `"0600"` | The socket's permissions, as an octal string |
| `bindAddress` | `TAMARACKDB_BIND_ADDRESS` | `127.0.0.1` | The address to listen on over TCP |
| `port` | `TAMARACKDB_PORT` | `8085` | The port to listen on over TCP |
| `enableAuth` | `TAMARACKDB_ENABLE_AUTH` | `false` | Whether every request needs the Bearer token |
| `authToken` | `TAMARACKDB_AUTH_TOKEN` | none | The Bearer token |
| `dataDir` | `TAMARACKDB_DATA_DIR` | `data` | The directory holding the database file, `tamarackdb.sqlite` |
| `logLevel` | `TAMARACKDB_LOG_LEVEL` | `warning` | The lowest level logged: `debug`, `info`, `warning`, or `error` |
| `devMode` | `TAMARACKDB_DEV_MODE` | `false` | Turns on `POST /reset` and the profiling endpoints |
| `defaultEventsPerPage` | `TAMARACKDB_DEFAULT_EVENTS_PER_PAGE` | `1000` | The `limit` of a `QUERY /events` that leaves it out |
| `maxEventsPerPage` | `TAMARACKDB_MAX_EVENTS_PER_PAGE` | `10000` | The highest `limit` a `QUERY /events` may ask for |
| `maxEventSize` | `TAMARACKDB_MAX_EVENT_SIZE` | `65536` (64 KiB) | The largest event, in bytes |
| `maxProjectionSize` | `TAMARACKDB_MAX_PROJECTION_SIZE` | `65536` (64 KiB) | The largest projection, in bytes |
| `maxEventsPerTx` | `TAMARACKDB_MAX_EVENTS_PER_TX` | `100` | The most events one transaction writes |
| `maxReadsPerTx` | `TAMARACKDB_MAX_READS_PER_TX` | `100` | The most reads of events in one transaction |
| `maxProjectionsPerTx` | `TAMARACKDB_MAX_PROJECTIONS_PER_TX` | `500` | The most projections one transaction writes |
| `maxProjectionsPerWrite` | `TAMARACKDB_MAX_PROJECTIONS_PER_WRITE` | `500` | The most projections in one `POST /projections` |
| `maxRequestBodySize` | `TAMARACKDB_MAX_REQUEST_BODY_SIZE` | `8388608` (8 MiB) | The largest request body, in bytes |
| `maxQueuedWrites` | `TAMARACKDB_MAX_QUEUED_WRITES` | `100` | The most writes waiting for their turn at once |
| `readPoolSize` | `TAMARACKDB_READ_POOL_SIZE` | `8` | How many reads run at once |
| `txIdleTimeout` | `TAMARACKDB_TX_IDLE_TIMEOUT` | `60` | How long, in seconds, a transaction lives without a call |
| `maxOpenTx` | `TAMARACKDB_MAX_OPEN_TX` | `1000` | The most transactions open at once |

## Listening

- By default, the server listens on the unix socket at `socketPath`. Setting `bindAddress` or `port` switches it to
  TCP. A `socketPath` that is set wins over both.
- The socket's directory must exist and be writable by the server's user. Under systemd, `RuntimeDirectory` creates it
  (see [Install](/docs/operations/install/#systemd)).
- The server speaks plain HTTP. Reaching it from another host is in [Security](/docs/operations/security/).

## Limits

- The size and count limits are a cautious starting point. Find the right values in development, with the
  application's data, and use the same ones in production. Every error from a limit names the setting to raise.
- Every limit must be positive, and `defaultEventsPerPage` can't exceed `maxEventsPerPage`. The server refuses to
  start otherwise.
- `txIdleTimeout` only ends transactions the application forgot. A transaction that keeps making calls lives as long
  as it needs.
- Past `maxOpenTx`, `POST /tx` gets `503 TooManyTransactions`. The open transactions go on. A commit still writing
  counts as open.

## Write queue

Writes are served one at a time, in the order they arrive: transaction commits, `POST /projections`, bulk deletes of
projections, `POST /pause`, `POST /resume`, `POST /optimize`, and `POST /reset`. Reads never wait.

- A write usually holds its turn a few milliseconds, so the queue stays short.
- A large write, such as a projection rebuild in one request, makes the others wait that long.
- `maxQueuedWrites` bounds how many writes wait at once. One more gets `503 WriteQueueFull`. Set it from how many
  writes the application sends at once.
