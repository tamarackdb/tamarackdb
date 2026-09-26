---
title: "Backup"
slug: "backup"
weight: 3
---

This is for whoever needs a standing backup copy of an instance's events: an
off-site copy, a warm standby, or a database to test against without touching
production. For how to run the server itself, see [Deployment](/docs/guides/deployment/).

`tamarackdb-backup` copies new events from a remote TamarackDB instance into a
local SQLite file. It does one catch-up run and exits: schedule it with cron
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
: Base URL of the instance to copy events from.
: Env: `TAMARACKDB_BACKUP_SOURCE_URL`
: Default: none, required

`sourceToken`
: Bearer token for the source instance, when it has `enableAuth` on.
: Env: `TAMARACKDB_BACKUP_SOURCE_TOKEN`
: Default: none

`databasePath`
: Path to the local SQLite file the backup is written to.
: Env: `TAMARACKDB_BACKUP_DATABASE_PATH`
: Default: `data/tamarackdb-backup.sqlite`

`pageLimit`
: Page size used when reading from the source.
: Env: `TAMARACKDB_BACKUP_PAGE_LIMIT`
: Default: `1000`

The config file is TOML, with these keys under a `[backup]` section. That
section can live in its own file, as shown above, or share one file with the
server's `[server]` section (see [Deployment](/docs/guides/deployment/#configure)); either
way `tamarackdb-backup` reads only `[backup]`.

`sourceUrl` must be an `http://` or `https://` address, so the source instance
needs `bindAddress`/`port` set: `tamarackdb-backup` has no way to reach a
source running only on its default unix socket (see
[Deployment](/docs/guides/deployment/#configure)).

## What the backup holds

The backup file is a regular TamarackDB database. If the source is ever lost,
point `tamarackdb-server` at the backup file and serve it as the new instance.

It holds events only, not documents. Before an application uses a restored
backup, it must rebuild its projections (see
[Integration](/docs/guides/integration/#projection-rebuilds)).

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
