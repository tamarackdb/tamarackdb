---
title: "Install"
description: "Getting a TamarackDB instance running: on your own machine with Docker, in production with systemd, from the binaries, or alongside an app in Docker."
slug: "install"
weight: 1
---

Getting an instance running: on your own machine, from the release binaries, in production with systemd, or in Docker.

## Local development

```sh
docker run -d --rm --name tamarackdb -p 127.0.0.1:8085:8085 \
  -e TAMARACKDB_DEV_MODE=true \
  -e TAMARACKDB_LOG_LEVEL=debug \
  ghcr.io/tamarackdb/tamarackdb:latest
```

- The API is at `http://127.0.0.1:8085`, reachable from your machine only.
- `TAMARACKDB_DEV_MODE=true` turns on `POST /reset`, to start each test run from an empty store (see
  [Development mode](/docs/operations/maintenance/#development-mode)).
- `TAMARACKDB_LOG_LEVEL=debug` logs every request. Read them with `docker logs -f tamarackdb`.
- `--rm` deletes the data when the container stops. Mount a volume on `/data` to keep it (see [Docker](#docker)).

## Binaries

Each release publishes three binaries for Linux, on amd64 and arm64:

| Binary | What it does |
|---|---|
| `tamarackdb-server` | The HTTP server |
| `tamarackdb-init` | Creates a new database, empty or from a dump of events. The server needs one to start |
| `tamarackdb-backup` | Copies new events from an instance into a backup file |

Download a release and install its binaries (see the
[releases](https://github.com/tamarackdb/tamarackdb/releases) for the latest version):

```sh
VERSION=v0.32.0 ARCH=amd64
curl -fLO https://github.com/tamarackdb/tamarackdb/releases/download/$VERSION/tamarackdb-$VERSION-linux-$ARCH.zip
unzip tamarackdb-$VERSION-linux-$ARCH.zip
sudo install -m 755 tamarackdb-$VERSION-linux-$ARCH/tamarackdb-* /usr/local/bin/
```

- The binaries are static: no runtime and no library to install.
- `--version` prints a binary's version and exits.
- To build them from source instead, run `make build` in a clone of the repository, with Go. The binaries land in
  `bin/`. Details are in
  [CONTRIBUTING.md](https://github.com/tamarackdb/tamarackdb/blob/main/CONTRIBUTING.md#building-from-source).

## Production

Check each point before an instance holds real data:

1. Run the server as its own system user, never as root.
2. Keep `dataDir` readable by that user only (`0700`).
3. Run the server on the application's host, on its unix socket (see
   [Security](/docs/operations/security/#unix-socket)).
4. From another host, go through a reverse proxy for TLS, and turn `enableAuth` on (see
   [Security](/docs/operations/security/#another-host)).
5. Make a `config.toml` that holds `authToken` readable by the server's user only (`chmod 600`).
6. Leave `devMode` off.
7. Size `maxQueuedWrites` (see [Configuration](/docs/operations/configuration/#write-queue)).
8. Watch `/health` and `/stats` (see [Monitoring](/docs/operations/monitoring/)).
9. Schedule `tamarackdb-backup` (see [Backup and Import](/docs/operations/backup/)), and the daily `POST /optimize`
   (see [Maintenance](/docs/operations/maintenance/#query-statistics)).

### systemd

A unit for the server, running as user `tamarackdb`:

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
RuntimeDirectory=tamarackdb
RuntimeDirectoryMode=0755
StateDirectory=tamarackdb
StateDirectoryMode=0700
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

systemd creates `/run/tamarackdb` for the socket, and `/var/lib/tamarackdb` for the data, both owned by `tamarackdb`.
With this `config.toml`:

```toml
[server]
socketMode = "0660"
dataDir = "/var/lib/tamarackdb"
```

Create the user and the database, then start the service:

```sh
sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin tamarackdb
sudo install -d -o tamarackdb -g tamarackdb -m 700 /var/lib/tamarackdb
sudo -u tamarackdb /usr/local/bin/tamarackdb-init --data-dir /var/lib/tamarackdb
sudo systemctl enable --now tamarackdb
```

On `systemctl stop`, the server lets a write already running finish, and turns away the requests still waiting with
`503 ShuttingDown`.

## Running the binaries

Run every command that touches `dataDir` as the server's user: the server, `tamarackdb-init`, and any `sqlite3` you
run by hand. A file created by another user, such as root, is one the server can't open, and it refuses to start.

```sh
sudo install -d -o tamarackdb -g tamarackdb -m 755 /run/tamarackdb
sudo -u tamarackdb /usr/local/bin/tamarackdb-init --data-dir /path/to/data
sudo -u tamarackdb /usr/local/bin/tamarackdb-server --config /path/to/config.toml
```

- `/run` is emptied at every reboot. For a lasting service, use the [systemd unit](#systemd).
- `tamarackdb-init` creates `dataDir` and a new database. It never overwrites an existing database.
- The server never creates a database: it refuses to start when the file is missing.
- The server refuses to start when the database's schema version doesn't match its own. It never changes the schema.
- At startup, the server prints its resolved configuration to stdout (never `authToken`).

## Docker

Each release publishes an image for `linux/amd64` and `linux/arm64`. Pin a version with
`ghcr.io/tamarackdb/tamarackdb:v<version>` (see the [releases](https://github.com/tamarackdb/tamarackdb/releases)).

```sh
docker run -d -p 127.0.0.1:8085:8085 -v tamarackdb-data:/data ghcr.io/tamarackdb/tamarackdb:latest
```

- The image is set up with `TAMARACKDB_*` environment variables only (see
  [Configuration](/docs/operations/configuration/)).
- It listens over TCP on port `8085`, and keeps the database in `/data`.
- Publish the port on `127.0.0.1` only. A plain `-p 8085:8085` opens the API to anyone who can reach the host. To
  expose it, turn `enableAuth` on and put a reverse proxy in front for TLS (see
  [Security](/docs/operations/security/#another-host)).
- The server runs as UID and GID `10001`. To mount a host directory instead of a named volume, give it to that user
  first: `sudo install -d -o 10001 -g 10001 -m 700 /srv/tamarackdb`.
- When `/data` holds no database, the image creates one and logs `tamarackdb-init: created /data/tamarackdb.sqlite`.
  If that line shows up on a restart, the container got an empty volume: check its name and mount point.

### Alongside the application

When the application runs in a container too, put both on the same Docker network and publish no port. The
application reaches TamarackDB by its service name:

```yaml
services:
  tamarackdb:
    image: ghcr.io/tamarackdb/tamarackdb:latest
    volumes:
      - tamarackdb-data:/data
  app:
    image: my-app
    environment:
      TAMARACKDB_URL: http://tamarackdb:8085

volumes:
  tamarackdb-data:
```

Every container on that network can reach the API. If some of them aren't trusted, turn `enableAuth` on.
