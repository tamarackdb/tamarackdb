---
title: "Backup"
description: "tamarackdb-backup keeps a standing copy of an instance's events: its settings, how a run works, local or remote sources, restoring, and scheduling."
slug: "backup"
weight: 7
---

`tamarackdb-backup` keeps a standing copy of an instance's events: an off-site copy, a warm standby, or a database to
test against without touching production. It does one catch-up run and exits. Schedule it with cron or a systemd timer;
don't run it as a long-running process. A missed or late run isn't a problem: the next one picks up where the last one
stopped.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## Configuration

Generate a starter config and adjust it:

```sh
./bin/tamarackdb-backup --default-config > backup-config.toml
./bin/tamarackdb-backup --config /path/to/backup-config.toml
```

`sourceUrl`
: Base URL of the instance to copy events from: an `http://` or `https://` address.
: Env: `TAMARACKDB_BACKUP_SOURCE_URL`
: Default: none

`sourceSocket`
: Path of the unix socket the instance listens on, for an instance on the same host (see [A source on the same
  host](#a-source-on-the-same-host)).
: Env: `TAMARACKDB_BACKUP_SOURCE_SOCKET`
: Default: none

`sourceToken`
: Bearer token for the source instance, when it has `enableAuth` on.
: Env: `TAMARACKDB_BACKUP_SOURCE_TOKEN`
: Default: none

`dataDir`
: Directory the backup files are written to. Each file is named after the store ID of the source, `<store ID>.sqlite`,
  so a reset of the source starts a new file and leaves the old one as it is. A file holds every event: its permissions
  are in [Security](/docs/operations/security/#files). Don't point it at the server's own `dataDir`.
: Env: `TAMARACKDB_BACKUP_DATA_DIR`
: Default: `backup`

`pageLimit`
: Page size used when reading from the source. It MUST NOT exceed the source's `maxEventsPerPage`, or every run fails
  with `400 Bad Request`.
: Env: `TAMARACKDB_BACKUP_PAGE_LIMIT`
: Default: `1000`

The config file is TOML, with these keys under a `[backup]` section. An unknown key in it stops the run with an error
naming the key. If it holds `sourceToken`, make it readable by the backup's user only (`chmod 600`). That section can
live in its own file, as shown above, or share one file with the server's `[server]` section (see
[Configuration](/docs/operations/configuration/#sources)); either way `tamarackdb-backup` reads only `[backup]`.

Set exactly one of `sourceUrl` and `sourceSocket`: a run with both, or neither, stops with an error. When the file sets
one of them, the `TAMARACKDB_BACKUP_SOURCE_*` variables are ignored, so a variable left in the environment can't clash
with the file's choice.

## How a run works

Every instance names the history it holds with a store ID (see [Store ID](/docs/concepts/store-id/)). A backup file is
named after it, `<store ID>.sqlite`, in `dataDir`. Each run:

1. Asks the source for its first event (`QUERY /events` with `afterSequence: 0` and `limit: 1`), only for the store ID
   in the response's header. The event itself is dropped, and the page is empty for an empty source.
2. Checks that the store ID is a UUID in its canonical form, since it comes from the network and becomes a file name.
   Nothing else can point outside the backup directory.
3. Creates `dataDir` if it's missing, then opens `<store ID>.sqlite` in it, or creates it with the server's schema. The
   run holds the file's `.lock` (see [SQLite](/docs/server-internals/sqlite/#one-process-per-file)): a backup file can't
   be updated while a server serves it.
4. Reads the highest Sequence Position already in the file.
5. Pages through the source's `QUERY /events` from there, with `limit` set to `pageLimit`. The run checks that every
   page carries the same store ID as the first response. If it changes, the source was reset during the run: the run
   stops with an error, without importing that page, and the next run starts the new file.
6. Writes each page in one SQLite transaction, with `Store.Import`, a variant of `Store.Append`. Each event keeps the
   `sequence` and `time` the source gave it: the import skips sequence reservation and the Append Condition check, then
   moves the local counter past the highest sequence imported.
7. Stops once a page's trailer reads `hasMore: false`.

**Why the file name.** It's what keeps the events of one store out of the file of another, with nothing else to store or
check. After a reset of the source, the next run sees a new store ID, creates a new file, and starts from zero; the old
file stays as it is. On a development instance, files pile up: delete the ones you no longer need.

## Failures

- A page cut short (no trailer, see [A page cut short](/docs/http-api/read-events/#a-page-cut-short)) fails the run,
  instead of importing a partial page.
- A page request that takes more than 5 minutes fails the run, so a source that stops answering never holds the backup
  file's lock past that.
- There's no retry inside a run: the error goes to stderr, with a non-zero exit code.
- Every page imported before the failure is already committed, so the next run resumes right after it.

## A source on the same host

For an instance on the same host, listening on its unix socket, read straight from the socket:

```toml
[backup]
sourceSocket = "/run/tamarackdb/tamarackdb.sock"
```

Nothing is exposed over the network. The socket's permissions decide who may connect, as for the application: the
backup's user MUST be allowed by the server's `socketMode`. Run the backup as the server's own user, or set `socketMode
= "0660"` and add the backup's user to the server's group (see [Security](/docs/operations/security/#unix-socket)).

## A source on another host

`tamarackdb-backup` reaches an instance on another host over HTTPS. The server itself only speaks plain HTTP, so put a
reverse proxy in front of its socket, responsible for TLS, and point `sourceUrl` at the proxy's `https://` address.
nginx and Caddy both relay the `QUERY` method.

Two things to get right:

- **Who can reach the proxy.** Over the socket, the file's permissions are what keep other users out, and `enableAuth`
  is often off. The proxy gives full access to the API, writes included, to anyone who reaches its port. Turn
  `enableAuth` on in the server, with the token in `sourceToken`: the proxy passes the `Authorization` header through.
- **The proxy's access to the socket.** The proxy's user MUST be allowed by the server's `socketMode`: set it to
  `"0660"`, and add that user to the server's group (see [Security](/docs/operations/security/#unix-socket)).

## What a backup file holds

- Events only. Projections are left out on purpose: every projection can be rebuilt from events, so the file's
  `projections` table stays empty.
- It's a regular TamarackDB database file. Unlike a raw copy of the source's file, which can miss commits still in the
  WAL, `tamarackdb-server` can serve it as a live instance.
- It has a store ID of its own, drawn when the run created it, not the source's. Served as an instance, it's seen as
  another store, which is right, since it can be behind the source.

## Restoring

If the source is ever lost, serve the file of its current store ID as the new instance. That's the file the last run
wrote to: each run logs its path.

1. Wait for any `tamarackdb-backup` run to finish, and stop scheduling new ones.
2. Create a new data directory, owned by the user the server runs as (here `tamarackdb`) and readable by that user only,
   and copy the backup file into it under the name `tamarackdb.sqlite`, the only name the server opens inside its
   `dataDir`:

   ```sh
   sudo install -d -o tamarackdb -g tamarackdb -m 700 /path/to/new-data
   sudo install -o tamarackdb -g tamarackdb -m 600 /path/to/backup/<store ID>.sqlite /path/to/new-data/tamarackdb.sqlite
   ```

   A directory made with a plain `mkdir`, or a file copied with a plain `cp`, gets the umask's permissions, which
   usually let every user read the events. A file copied by another user, such as root, is one the server can't write,
   and it refuses to start.
3. Start `tamarackdb-server` with `dataDir` set to that directory.

Before an application uses a restored backup, it MUST rebuild its projections (see
[Rebuilds](/docs/concepts/projections/#rebuilds)). Anything that kept a position on the source starts over, since the
backup has its own store ID.

Don't serve the backup file while `tamarackdb-backup` still writes to it: the two can't hold the file at the same time.

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
