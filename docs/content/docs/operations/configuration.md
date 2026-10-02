---
title: "Configuration"
slug: "configuration"
weight: 2
---

Every setting of `tamarackdb-server`. The settings of `tamarackdb-backup` are in [Backup](/docs/backup/#configuration).

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## Sources

A setting comes from, in this order:

1. A TOML file, passed with `--config` (`config.toml` in the working directory by default). Settings live under a
   `[server]` section.
2. A `TAMARACKDB_*` environment variable, one per setting.
3. A built-in default, for the settings that have one.

- A value set in the file always wins over the environment variable.
- The file is optional. Use one file per instance in production. In Docker, environment variables cover a deployment
  with no file at all.
- The file is checked as a whole, `[server]` and `[backup]` sections included. An unknown key, or a key outside any
  section, stops the server at startup with an error naming it, so a typo never leaves a setting at its default.
- The same file can hold the `[backup]` section of `tamarackdb-backup`. Each binary reads only its own section.

Generate a starter file, with every setting commented out at its default:

```sh
./bin/tamarackdb-server --default-config > config.toml
./bin/tamarackdb-backup --default-config >> config.toml
```

A file that holds `authToken` is a secret: see [Security](/docs/operations/security/#files).

## Settings

| Key | Environment variable | Default | What it sets |
|---|---|---|---|
| `socketPath` | `TAMARACKDB_SOCKET_PATH` | `/run/tamarackdb/tamarackdb.sock` | The unix socket to listen on. At most 107 bytes, the Linux limit |
| `socketMode` | `TAMARACKDB_SOCKET_MODE` | `"0600"` | The socket's permissions, as an octal string. Only with `socketPath` |
| `bindAddress` | `TAMARACKDB_BIND_ADDRESS` | `127.0.0.1` | The address to listen on over TCP |
| `port` | `TAMARACKDB_PORT` | `8085` | The port to listen on over TCP |
| `enableAuth` | `TAMARACKDB_ENABLE_AUTH` | `false` | Whether every request needs the Bearer token |
| `authToken` | `TAMARACKDB_AUTH_TOKEN` | none | The Bearer token |
| `dataDir` | `TAMARACKDB_DATA_DIR` | `data` | The directory holding the database file |
| `logLevel` | `TAMARACKDB_LOG_LEVEL` | `warning` | The lowest level logged: `debug`, `info`, `warning`, or `error` (see [Logs](/docs/operations/logs/)) |
| `devMode` | `TAMARACKDB_DEV_MODE` | `false` | Turns on `POST /reset` and the profiling endpoints (see [Dev mode](/docs/operations/dev-mode/)) |
| `defaultEventsPerPage` | `TAMARACKDB_DEFAULT_EVENTS_PER_PAGE` | `1000` | The `limit` of a `QUERY /events` that leaves it out |
| `maxEventsPerPage` | `TAMARACKDB_MAX_EVENTS_PER_PAGE` | `10000` | The highest `limit` a `QUERY /events` may ask for |
| `maxEventSize` | `TAMARACKDB_MAX_EVENT_SIZE` | `65536` (64 KiB) | The largest event, in bytes (see [Events](/docs/concepts/events/#size)) |
| `maxProjectionSize` | `TAMARACKDB_MAX_PROJECTION_SIZE` | `65536` (64 KiB) | The largest projection, in bytes: its `type`, `id`, and `payload` together |
| `maxEventsPerWrite` | `TAMARACKDB_MAX_EVENTS_PER_WRITE` | `100` | The most events, and the most Append Conditions, in one `POST /write` |
| `maxProjectionsPerWrite` | `TAMARACKDB_MAX_PROJECTIONS_PER_WRITE` | `500` | The most projections in one `POST /write`, across its three lists |
| `maxRequestBodySize` | `TAMARACKDB_MAX_REQUEST_BODY_SIZE` | `8388608` (8 MiB) | The largest request body, in bytes, for every endpoint |
| `maxQueuedWrites` | `TAMARACKDB_MAX_QUEUED_WRITES` | `100` | The most requests waiting for their turn at once (see below) |
| `readPoolSize` | `TAMARACKDB_READ_POOL_SIZE` | `8` | SQLite connections for reads, and so how many reads run at once |

## Listening

- By default, the server listens on the unix socket at `socketPath`. Setting `bindAddress` or `port` switches it to
  TCP. `socketPath` wins whenever it's set, even alongside them.
- The default socket's directory, `/run/tamarackdb`, MUST exist and be writable by the server's user. Under systemd,
  `RuntimeDirectory=tamarackdb` creates it (see [Install](/docs/operations/install/#production)); elsewhere, create it
  yourself, or set a path the server's user owns.
- At startup, the server removes a socket left at that path by an earlier run, and refuses to start if the path holds
  anything other than a socket.
- The server speaks plain HTTP either way. Why the socket is the recommended setup, and how to reach the server from
  another host, is in [Security](/docs/operations/security/).

## Data directory

- `dataDir` holds the database file, `tamarackdb.sqlite`. Only the directory is configurable: the file name is fixed,
  the same convention as MySQL's `datadir`.
- Its layout is managed by TamarackDB and may change between versions: don't rely on it, and don't edit its contents
  by hand.
- Its permissions are in [Security](/docs/operations/security/#files).

## Limits

- The size and count limits (`maxEventSize`, `maxProjectionSize`, `maxEventsPerWrite`, `maxProjectionsPerWrite`,
  `maxRequestBodySize`) are a cautious starting point. Find the real limits in development, with the application's
  data, then set the same values in production. Every error from a limit names the setting to raise (see
  [Writing](/docs/http-api/write/#limits)).
- `maxRequestBodySize` isn't checked against the other limits: it's the real bound on a write, and the others are
  rules for each item.
- `defaultEventsPerPage` and `maxEventsPerPage` are settings, not constants, because how fast a projector processes a
  batch varies between applications, and between projectors of one application. The defaults keep a default page easy
  to buffer, and a maximum page done in seconds.
- Every limit MUST be positive, and `defaultEventsPerPage` MUST NOT exceed `maxEventsPerPage`: the server refuses
  to start otherwise.

## Sizing the write queue

`maxQueuedWrites` bounds how many requests wait for their turn at once: `POST /write`, the bulk deletes of
projections, `POST /reset`, and the hourly `PRAGMA optimize` (see
[The write FIFO](/docs/server-internals/write-fifo/)). One more gets `503 WriteQueueFull` instead of joining.

- A write holds the turn only while its own SQLite transaction runs, usually a few milliseconds, so the queue is
  usually empty or short. Reads never wait in it.
- A large write holds the turn longer: a projection rebuild sent in one write holds it for as long as its inserts
  take, and the writes behind it wait that long.
- Size `maxQueuedWrites` against how many writes the application sends at once. There's no "no limit" value: every
  deployment gets a bound.
- The server doesn't cap how long a request waits. Each client sets its own limit and closes the connection when it's
  reached.
