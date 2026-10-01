---
title: "Backup"
slug: "backup"
weight: 3
---

This is for whoever needs a standing backup copy of an instance's events: an
off-site copy, a warm standby, or a database to test against without touching
production. For how to run the server itself, see [Deployment](/docs/guides/deployment/).

`tamarackdb-backup` copies new events from a remote TamarackDB instance into a
local SQLite file, named after the store ID of the instance. It does one
catch-up run and exits: schedule it with cron
or a systemd timer, don't run it as a long-running process. A run that's
missed or late isn't a problem: the next one picks up from where the last one
stopped.

Generate a starter config and adjust it as needed:

```sh
./bin/tamarackdb-backup --default-config > backup-config.toml
```

```sh
./bin/tamarackdb-backup --config /path/to/backup-config.toml
```

`sourceUrl`
: Base URL of the instance to copy events from: an `http://` or `https://` address.
: Env: `TAMARACKDB_BACKUP_SOURCE_URL`
: Default: none

`sourceSocket`
: Path of the unix socket the instance listens on, for an instance on the same host (see [A source on the same host](#a-source-on-the-same-host)).
: Env: `TAMARACKDB_BACKUP_SOURCE_SOCKET`
: Default: none

`sourceToken`
: Bearer token for the source instance, when it has `enableAuth` on.
: Env: `TAMARACKDB_BACKUP_SOURCE_TOKEN`
: Default: none

`dataDir`
: Directory the backup files are written to. Each file is named after the store ID of the source, `<store ID>.sqlite`, so a reset of the source starts a new file and leaves the old one as it is. A file holds every event, readable by anyone with access to it, so a missing directory is created as `0700` and a new backup file as `0600`. A directory that already exists keeps its permissions: give it `0700` if other users can reach it. Don't point it at the server's own `dataDir`.
: Env: `TAMARACKDB_BACKUP_DATA_DIR`
: Default: `backup`

`pageLimit`
: Page size used when reading from the source. It must not exceed the source's `maxEventsPerPage`, or every run fails with `400 Bad Request`.
: Env: `TAMARACKDB_BACKUP_PAGE_LIMIT`
: Default: `1000`

The config file is TOML, with these keys under a `[backup]` section. An
unknown key in it stops the run with an error naming the key. If it holds
`sourceToken`, make it readable by the backup's user only (`chmod 600`). That
section can live in its own file, as shown above, or share one file with the
server's `[server]` section (see [Deployment](/docs/guides/deployment/#configure)); either
way `tamarackdb-backup` reads only `[backup]`.

Set exactly one of `sourceUrl` and `sourceSocket`: a run with both, or
neither, stops with an error. When the file sets one of them, the
`TAMARACKDB_BACKUP_SOURCE_*` variables are ignored, so a variable left in the
environment can't clash with the file's choice.

## How it works

Every TamarackDB instance names the history it holds with a store ID (see
[Integration](/docs/guides/integration/#store-id)). A backup file is named
after it, `<store ID>.sqlite`, in `dataDir`, so the events of one store never
land in the file of another. Each run:

1. Asks the source for its first event, only for the store ID its response
   carries. The event itself is dropped.
2. Opens the file named after that store ID, or creates it, and resumes after
   the last event it holds.
3. Reads the new events page by page. Every page must carry the same store
   ID. If it changes during the run, the source was reset in the meantime:
   the run stops with an error, without importing that page, and the next run
   starts the new file.

The store ID comes from the network, so a run checks that it's a UUID before
using it as a file name.

The store ID only changes when the source is reset with `POST /reset`, which
exists in developer mode only. Each reset starts a new file, and the files of
earlier store IDs stay as they are. On a development instance, they pile up:
delete the ones you no longer need.

## A source on the same host

For an instance on the same host, listening on its unix socket, read straight
from the socket:

```toml
[backup]
sourceSocket = "/run/tamarackdb/tamarackdb.sock"
```

Nothing is exposed over the network. The socket's permissions decide who may
connect, as for the application: the backup's user must be allowed by the
server's `socketMode`. Run the backup as the server's own user, or set
`socketMode = "0660"` and add the backup's user to the server's group (see
[Deployment](/docs/guides/deployment/#configure)).

## A source on another host

`tamarackdb-backup` reaches an instance on another host over HTTPS. The server
itself only speaks plain HTTP, so put a reverse proxy in front of its socket,
responsible for TLS, and point `sourceUrl` at the proxy's `https://` address.
nginx and Caddy both relay the `QUERY` method.

Two things to get right:

- **Who can reach the proxy.** Over the socket, the file's permissions are
  what keep other users out, and `enableAuth` is often off. The proxy gives
  full access to the API, writes included, to anyone who reaches its port.
  Turn `enableAuth` on in the server, with the token in `sourceToken`: the
  proxy passes the `Authorization` header through.
- **The proxy's access to the socket.** The proxy's user must be allowed by
  the server's `socketMode`: set it to `"0660"`, and add that user to the
  server's group (see [Deployment](/docs/guides/deployment/#configure)).

## What the backup holds

A backup file is a regular TamarackDB database. If the source is ever lost,
serve the file of its current store ID as the new instance. That's the file
the last run wrote to: each run logs its path.

1. Wait for any `tamarackdb-backup` run to finish, and stop scheduling new ones.
2. Create a new data directory, owned by the user the server runs as (here
   `tamarackdb`) and readable by that user only, and copy the backup file into
   it under the name `tamarackdb.sqlite`, the only name the server opens
   inside its `dataDir`:

   ```sh
   sudo install -d -o tamarackdb -g tamarackdb -m 700 /path/to/new-data
   sudo install -o tamarackdb -g tamarackdb -m 600 /path/to/backup/<store ID>.sqlite /path/to/new-data/tamarackdb.sqlite
   ```

   A directory made with a plain `mkdir`, or a file copied with a plain `cp`,
   gets the umask's permissions, which usually let every user read the
   events. A file copied by another user, such as root, is one the server
   can't write, and it refuses to start.
3. Start `tamarackdb-server` with `dataDir` set to that directory.

It holds events only, not projections. Before an application uses a restored
backup, it must rebuild its projections (see
[Integration](/docs/guides/integration/#projection-rebuilds)).

A backup file has a store ID of its own, drawn when the run created it, not
the source's. Served as an instance, it's seen as another store: anything
that kept a position on the source starts over. That's right, since the
backup can be behind the source.

Don't serve the backup file while `tamarackdb-backup` still writes to it: the
two can't hold the file at the same time.

A run that fails exits with a non-zero code and writes the error to stderr.
The next run resumes where the failed one stopped.

See [Architecture](/docs/architecture/#backup) for how a run works.

## Scheduling

A typical cron entry:

```
*/5 * * * * /usr/local/bin/tamarackdb-backup --config /etc/tamarackdb/backup-config.toml >> /var/log/tamarackdb-backup.log 2>&1
```

Or a systemd timer:

```ini
# /etc/systemd/system/tamarackdb-backup.service
[Service]
Type=oneshot
ExecStart=/usr/local/bin/tamarackdb-backup --config /etc/tamarackdb/backup-config.toml
```

```ini
# /etc/systemd/system/tamarackdb-backup.timer
[Timer]
OnCalendar=*:0/5
Persistent=true

[Install]
WantedBy=timers.target
```
