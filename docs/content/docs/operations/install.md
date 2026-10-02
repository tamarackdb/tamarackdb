---
title: "Install"
description: "Getting a TamarackDB instance running: on your own machine with Docker, in production with systemd, from the binaries, or alongside an app in Docker."
slug: "install"
weight: 1
---

Getting an instance running: on your own machine, in production with systemd, or in Docker. For building the binaries,
see [Building from source](/docs/contributing/building-from-source/).

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
  from an empty store (see [Developer mode](/docs/operations/dev-mode/)).
- `TAMARACKDB_LOG_LEVEL=debug` logs every request, which helps while you
  write the integration (see [Logs](/docs/operations/logs/)). Read them with
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
   yourself the same permissions (see [Security](/docs/operations/security/#files)).
3. **The same host, over the unix socket.** Installed directly on the host,
   run TamarackDB next to the application, on the socket. If the application
   runs as another user, set `socketMode = "0660"` and add that user to the
   server's group (see [Security](/docs/operations/security/#unix-socket)). In Docker, use TCP on a
   private network instead (see
   [Alongside the application](#alongside-the-application)).
4. **A reverse proxy and a token over the network.** If the application must
   reach TamarackDB from another host, put a reverse proxy in front of the
   socket to handle TLS, and turn `enableAuth` on (see
   [Security](/docs/operations/security/#another-host)).
5. **A protected configuration file.** A `config.toml` that holds `authToken`
   is readable by the server's user only (`chmod 600`).
6. **No developer mode.** `devMode` stays off: it exposes `POST /reset`,
   which deletes every event (see [Developer mode](/docs/operations/dev-mode/)).
7. **A sized queue.** Set `maxQueuedWrites` from how many writes the
   application sends at once (see
   [Sizing the write queue](/docs/operations/configuration/#sizing-the-write-queue)).
8. **Monitoring.** Point a supervisor at `/health`, scrape `/metrics`, and
   keep `logLevel` at `warning` (see [Health check](/docs/operations/health-check/),
   [Observability](/docs/operations/observability/), and [Logs](/docs/operations/logs/)).
9. **Backups.** Schedule `tamarackdb-backup`. On the same host, it reads
   straight from the socket with `sourceSocket`; from another host, through a
   reverse proxy that handles TLS (see [Backup](/docs/backup/)).

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
order: it turns away the requests waiting for their turn, with `503
ShuttingDown`, and lets a write already running finish.

## Run

Run every command that creates or rewrites files under `dataDir` as the user
the server runs as: the server, `tamarackdb-init`, and any `sqlite3` you run on
the database by hand. The data directory is readable by its owner only, so a
file created by another user, such as root, is one the server can't open, and
it refuses to start. The examples below assume the server runs as a user named
`tamarackdb`:

Create the default socket's directory first (see
[Configuration](/docs/operations/configuration/#listening)):

```sh
sudo install -d -o tamarackdb -g tamarackdb -m 755 /run/tamarackdb
sudo -u tamarackdb ./bin/tamarackdb-init --data-dir /path/to/data
sudo -u tamarackdb ./bin/tamarackdb-server --config /path/to/config.toml
```

`/run` is emptied at every reboot, so a directory created by hand is gone the
next time the machine starts. For a lasting service, use the systemd unit in
[Production](#production): `User=tamarackdb` replaces `sudo`, and
`RuntimeDirectory=tamarackdb` creates the directory at every start.

`tamarackdb-init` is optional: the server creates `dataDir` and its database,
with the schema, on its first start. On every start, it checks that the
database's schema version matches the one built into the binary, and refuses
to start if it doesn't; it never changes the schema on its own (see
[Architecture](/docs/server-internals/schema/#schema-version)). Once running, it logs one line
per request to stdout (see [Logs](/docs/operations/logs/)).

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
reverse proxy in front of it for TLS (see [Security](/docs/operations/security/#another-host)).

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
[Configuration](/docs/operations/configuration/)); no `config.toml` is needed inside the container. It sets
`TAMARACKDB_BIND_ADDRESS=0.0.0.0` and `TAMARACKDB_PORT=8085` itself, so it
listens over TCP, on port `8085`, unlike a plain `tamarackdb-server` binary.
The unix socket is for a server installed directly on the host (see
[Production](#production)); in a container, use TCP. It also sets `TAMARACKDB_DATA_DIR=/data`, so mount a volume on `/data` to keep
the database across restarts.

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

## Startup banner

At startup, before opening the store, the server prints a banner and its resolved configuration to stdout: bind
address, port, the socket path and mode, the auth flag (`authToken` itself is never printed), data directory, dev mode,
and the pagination, size, write, and queue-depth limits. Use it to check what an instance actually runs with. It's not
a machine-readable format.
