---
title: "Maintenance"
description: "The maintenance left to you: a daily timer for query statistics, giving back disk space with VACUUM, a full ANALYZE, and pausing transactions."
slug: "maintenance"
weight: 9
---

SQLite checkpoints its WAL on its own, and the server refreshes query statistics at every start. You set up one timer,
which refreshes them every day. Two operations are left to you, both run by hand while the server is stopped. A
fourth, the pause, runs while it serves.

Run both as the server's user (see [Install](/docs/operations/install/#run)).

## Giving back disk space

SQLite reuses the space of deleted projections on its own, so the file doesn't keep growing after a
[rebuild](/docs/concepts/projections/#rebuilds). To give that space back to the operating system, run a `VACUUM`. The
server never runs one itself.

1. Stop `tamarackdb-server`, at a time the application can be down, for example at the end of a rebuild.
2. Run the `VACUUM`:

   ```sh
   sudo -u tamarackdb sqlite3 /path/to/data/tamarackdb.sqlite 'VACUUM;'
   ```

3. Start `tamarackdb-server` again.

A `VACUUM` rewrites the whole file, events included, and needs free disk space about the size of the database while
it runs.

## Refreshing query statistics

Call [`POST /optimize`](/docs/http-api/optimize/) once a day, from a timer, at a quiet time. A cron entry:

```
0 3 * * * curl -fsS -X POST http://127.0.0.1:8085/optimize
```

Or a systemd timer:

```ini
# /etc/systemd/system/tamarackdb-optimize.service
[Service]
Type=oneshot
ExecStart=/usr/bin/curl -fsS -X POST http://127.0.0.1:8085/optimize
```

```ini
# /etc/systemd/system/tamarackdb-optimize.timer
[Timer]
OnCalendar=*-*-* 03:00
Persistent=true

[Install]
WantedBy=timers.target
```

- With `enableAuth` on, the call needs the token: add `-H "Authorization: Bearer <token>"` (see
  [Security](/docs/operations/security/#bearer-token)). On a unix socket, use
  `curl --unix-socket /path/to/tamarackdb.sock -X POST http://localhost/optimize`.
- `lastOptimizeAt` in `GET /stats` shows when it last ran (see [Observability](/docs/operations/observability/)).
- Without the timer, the statistics are only refreshed when the server starts. Queries slow down little by little,
  without giving wrong results.

After a one-off bulk import, run a full `ANALYZE` the same way as a `VACUUM`, while the server is stopped:

```sh
sudo -u tamarackdb sqlite3 /path/to/data/tamarackdb.sqlite 'ANALYZE;'
```

**Why the server is stopped.** Stopping it costs a few seconds, and keeps the server free of a long operation it would
have to coordinate with reads in flight. A WAL file left behind by another user is also one the server can't open.

## Pausing transactions

A [pause](/docs/http-api/pause/) stops transactions from beginning, while reads and writes of projections go on. Use it
for an operation that needs the log to stand still: a rebuild, a deploy.

```sh
curl -X POST http://127.0.0.1:8085/pause    # 202 while transactions are open: call again until 200
curl -X POST http://127.0.0.1:8085/resume
```

- A pause survives a restart of the server. A forgotten one blocks every command, until `POST /resume`.
- `GET /health` shows a pause in place, and `GET /stats` a requested one (see
  [Observability](/docs/operations/observability/)).
