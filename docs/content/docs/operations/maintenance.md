---
title: "Maintenance"
description: "Maintaining a TamarackDB instance: a daily timer for query statistics, VACUUM and ANALYZE, pausing transactions, and the development mode."
slug: "maintenance"
weight: 6
---

What an instance needs over time. SQLite checkpoints its WAL on its own. Run every `sqlite3` command as the server's
user, while the server is stopped.

## Query statistics

Call `POST /optimize` once a day, at a quiet time. Without it, queries slow down little by little. A cron entry:

```
0 3 * * * curl -fsS --unix-socket /run/tamarackdb/tamarackdb.sock -X POST http://localhost/optimize
```

Or a systemd timer:

```ini
# /etc/systemd/system/tamarackdb-optimize.service
[Service]
Type=oneshot
ExecStart=/usr/bin/curl -fsS --unix-socket /run/tamarackdb/tamarackdb.sock -X POST http://localhost/optimize
```

```ini
# /etc/systemd/system/tamarackdb-optimize.timer
[Timer]
OnCalendar=*-*-* 03:00
Persistent=true

[Install]
WantedBy=timers.target
```

- With `enableAuth` on, add `-H "Authorization: Bearer <token>"`.
- `lastOptimizeAt` in `GET /stats` shows when it last ran.
- After a large import, run a full `ANALYZE` once:
  `sudo -u tamarackdb sqlite3 /path/to/data/tamarackdb.sqlite 'ANALYZE;'`.

## Disk space

The file reuses the space of deleted projections, but never gives it back to the system. To shrink it, stop the server
and run a `VACUUM`:

```sh
sudo -u tamarackdb sqlite3 /path/to/data/tamarackdb.sqlite 'VACUUM;'
```

A `VACUUM` rewrites the whole file, and needs free disk space about the size of the database.

## Pausing transactions

A pause stops new transactions while reads and writes of projections go on. Use it when the log must stand still: a
projection rebuild, a deploy.

```sh
curl -X POST http://127.0.0.1:8085/pause    # 202 while transactions are still open: call again until 200
curl -X POST http://127.0.0.1:8085/resume
```

- A pause survives a restart. A forgotten pause blocks every command until `POST /resume`.
- `GET /health` shows a pause in place, and `GET /stats` a requested one.

## Development mode

`devMode` turns on two things, for a local instance or a short troubleshooting session. Leave it off in production.

- `DELETE /events`: deletes every event.
- `/debug/pprof/*`: Go's profiling endpoints. To profile a request, start a CPU profile, then send the request:

  ```sh
  go tool pprof -http=:0 "http://127.0.0.1:8085/debug/pprof/profile?seconds=30"
  ```

With `devMode` off, `DELETE /events` answers `405`, and `/debug/pprof/*` answers `404`.
