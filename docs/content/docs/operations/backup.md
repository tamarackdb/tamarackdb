---
title: "Backup and Import"
description: "Backing up an instance's events with tamarackdb-backup, restoring a backup, and importing the history of another event store with tamarackdb-init."
slug: "backup"
weight: 4
---

`tamarackdb-backup` keeps a copy of an instance's events. `tamarackdb-init --import` builds a new database from the
history of another event store. To copy a TamarackDB instance as is, copy its database file while the server is
stopped.

## Backup

`tamarackdb-backup` does one catch-up run and exits. Schedule it with cron or a systemd timer. Each run picks up where
the last one stopped, so a missed run is not a problem.

### Settings

The settings live under a `[backup]` section, in their own file or in the server's (see
[Configuration](/docs/operations/configuration/#sources)).

```sh
tamarackdb-backup --default-config > backup-config.toml
tamarackdb-backup --config backup-config.toml
```

| Key | Environment variable | Default | What it sets |
|---|---|---|---|
| `sourceUrl` | `TAMARACKDB_BACKUP_SOURCE_URL` | none | The `http://` or `https://` address of the instance to copy |
| `sourceSocket` | `TAMARACKDB_BACKUP_SOURCE_SOCKET` | none | The unix socket of an instance on the same host |
| `sourceToken` | `TAMARACKDB_BACKUP_SOURCE_TOKEN` | none | The Bearer token of the source, when it has `enableAuth` on |
| `dataDir` | `TAMARACKDB_BACKUP_DATA_DIR` | `backup` | The directory the backup files go to. Never the server's own `dataDir` |
| `pageLimit` | `TAMARACKDB_BACKUP_PAGE_LIMIT` | `1000` | Events read per page. At most the source's `maxEventsPerPage` |

- Set exactly one of `sourceUrl` and `sourceSocket`.
- A file that holds `sourceToken` must be readable by the backup's user only (`chmod 600`).

### The source

- **On the same host:** set `sourceSocket` to the server's socket. The backup's user must be allowed by the server's
  `socketMode` (see [Security](/docs/operations/security/#unix-socket)).
- **On another host:** set `sourceUrl` to the `https://` address of a reverse proxy in front of the server, with
  `enableAuth` on and the token in `sourceToken` (see [Security](/docs/operations/security/#another-host)).

### How a run works

- The backup file is `tamarackdb-backup.sqlite`, in `dataDir`.
- A run copies the events after the last one already in the file, page by page. Each event keeps its `sequence` and
  its `time`.
- Each page is written as soon as it's read. A run that fails keeps the pages already written, and the next run resumes
  from there.
- A run fails, with a non-zero exit code and the error on stderr, when a page is cut short, or when a page takes more
  than 5 minutes.
- A backup file holds events only. Projections are left out: they can be rebuilt from events.
- The backup file is a regular TamarackDB database.

### Scheduling

A cron entry:

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

### Restoring

Serve the backup file as the new instance.

1. Wait for the running backup to finish, and stop scheduling new ones.
2. Copy the file into a new data directory, under the name `tamarackdb.sqlite`, owned by the server's user:

   ```sh
   sudo install -d -o tamarackdb -g tamarackdb -m 700 /path/to/new-data
   sudo install -o tamarackdb -g tamarackdb -m 600 /path/to/backup/tamarackdb-backup.sqlite /path/to/new-data/tamarackdb.sqlite
   ```

3. Start `tamarackdb-server` with `dataDir` set to that directory.
4. With the application stopped, restart every projector from the beginning. Start the application once the
   projections are rebuilt.

## Import

An application that moves from another event store brings its history once, before it goes live:

```sh
sudo -u tamarackdb /usr/local/bin/tamarackdb-init --data-dir /path/to/data --import events.ndjson
```

```text
tamarackdb-init: created /path/to/data/tamarackdb.sqlite, imported 1000000 event(s), sequence 1 to 1000000
```

- The import always creates a new database. It never adds to an existing one.
- Each event keeps its `sequence` and its `time`. The first `sequence` can be 1 or more, and each next one is the one
  before plus 1.
- A line that isn't exactly an event stops the import, with its line number.
- The import is all or nothing: when it stops, no database is left.
- `maxEventSize` doesn't apply.
- Compare the count and the first and last `sequence` with the source before starting the server. Then rebuild every
  projection.

### The dump

One event per line, in NDJSON, in the shape of an event read with `QUERY /events`. Each line holds exactly these six
fields:

- `sequence`: an integer, 1 or more.
- `time`: exactly the form `2024-03-01T09:12:44.000000Z`, in UTC, with six digits after the second.
- `type`: a non-empty string.
- `identifiers` and `metadata`: objects, `{}` when empty, at most 20 entries each (see
  [Concepts](/docs/development/concepts/#events)).
- `payload`: a string, `""` when empty.

```json
{"sequence":1,"time":"2024-03-01T09:12:44.000000Z","type":"order-placed","identifiers":{"orderId":"o-1"},"metadata":{"tenantId":"acme"},"payload":"..."}
```
