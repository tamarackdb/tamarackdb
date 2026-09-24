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

## How it works

Each run reads the highest Sequence Position already in the local file, then
pages through the source's `QUERY /events` with `afterSequence` set to that
value, writing every event it gets straight into local storage: not through
`POST /write`, but through the same code path `store.Open` always uses to
build a database file, so the schema comes out identical. See
[Architecture](/docs/architecture/#storage-sqlite) for why that matters: the backup file
can be pointed at directly with `tamarackdb-server --config ...` if the source ever
needs replacing.

Each event is copied as is, both dates included: `clientTime` and `writeTime`
are the ones from the source. `writeTime` is when the source wrote the event,
not when the backup copied it.

`tamarackdb-backup` only ever copies events, into the events database file.
It has no notion of documents at all, and never touches (or even needs to
know the path of) a source's `tamarackdb-documents.sqlite`. This is
deliberate, not a gap: a document's payload is reproducible from events (see
[Architecture](/docs/architecture/#documents)). If you ever point `tamarackdb-server` at a
restored backup file, the documents database starts empty: any document an
application writes again shows up as usual, but a full catch-up for the rest
means rebuilding, the normal `DELETE /documents/{type}` plus a fresh write of
every projection of that type, not something that happens on its own.

There's no retry inside a run: if one fails partway, nothing is retried in
process, and the error goes to stderr with a non-zero exit code. Events
already imported before the failure stay in the local file, so the next
scheduled run resumes from the last successfully imported page.

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
