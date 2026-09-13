# Backing up TamarackDB

This is for whoever needs a standing backup copy of an instance's events: an
off-site copy, a warm standby, or a database to test against without touching
production. For how to run the server itself, see [deploy.md](deploy.md).

`tamarackdb-backup` copies new events from a remote TamarackDB instance into a
local SQLite file. It does one catch-up run and exits: schedule it with cron
or a systemd timer, don't run it as a long-running process. A run that's
missed or late isn't a problem: the next one picks up from where the last one
stopped.

Generate a starter config and adjust it as needed:

```sh
./bin/tamarackdb-backup -default-config > backup-config.json
```

```sh
./bin/tamarackdb-backup -config /path/to/backup-config.json
```

| Key | Environment variable | Default | Description |
|---|---|---|---|
| `sourceUrl` | `TAMARACKDB_BACKUP_SOURCE_URL` | none, required | Base URL of the instance to copy events from |
| `sourceToken` | `TAMARACKDB_BACKUP_SOURCE_TOKEN` | none | Bearer token for the source instance, when it has `enableAuth` on |
| `databasePath` | `TAMARACKDB_BACKUP_DATABASE_PATH` | none, required | Path to the local SQLite file the backup is written to |
| `pageLimit` | `TAMARACKDB_BACKUP_PAGE_LIMIT` | `1000` | Page size used when reading from the source |

This is its own config file, separate from the server's `config.json`: the two
never share a field.

## How it works

Each run reads the highest Sequence Position already in the local file, then
pages through the source's `QUERY /read` with `afterSequence` set to that
value, writing every event it gets straight into local storage: not through
`POST /append`, but through the same code path `store.Open` always uses to
build a database file, so the schema comes out identical. See
[design.md](design.md#storage-sqlite) for why that matters: the backup file
can be pointed at directly with `tamarackdb-server -config ...` if the source ever
needs replacing.

There's no retry inside a run: if one fails partway, nothing is retried in
process, and the error goes to stderr with a non-zero exit code. Events
already imported before the failure stay in the local file, so the next
scheduled run resumes from the last successfully imported page.

## Scheduling

A typical cron entry:

```
*/5 * * * * /usr/local/bin/tamarackdb-backup -config /etc/tamarackdb/backup-config.json >> /var/log/tamarackdb-backup.log 2>&1
```

Or a systemd timer:

```ini
# /etc/systemd/system/tamarackdb-backup.service
[Service]
Type=oneshot
ExecStart=/usr/local/bin/tamarackdb-backup -config /etc/tamarackdb/backup-config.json
```

```ini
# /etc/systemd/system/tamarackdb-backup.timer
[Timer]
OnCalendar=*:0/5
Persistent=true

[Install]
WantedBy=timers.target
```
