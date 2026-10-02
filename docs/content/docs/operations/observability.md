---
title: "Observability"
slug: "observability"
weight: 5
---

Two endpoints show the server's live, in-memory state: the request holding the write turn, the requests waiting for
theirs, the writes so far, and the SQLite connection pools.

Everything the database file can answer (event and projection counts, per-type breakdowns, file size) is a query away,
straight against the SQLite file, so the server doesn't expose it. These endpoints cover what only exists in memory,
for the life of the process.

## `GET /metrics`

Prometheus text format, for scraping into existing monitoring:

- `tamarackdb_write_active` (gauge): whether a request holds the write turn (`1`) or not (`0`): a write, a bulk delete,
  a reset, or `PRAGMA optimize`
- `tamarackdb_requests_queued` (gauge): number of requests currently waiting in the FIFO
- `tamarackdb_queue_longest_wait_seconds` (gauge): longest current wait, in seconds, among queued requests; `0` when
  the FIFO is empty
- `tamarackdb_writes_committed_total` (counter): total `POST /write` calls committed since startup
- `tamarackdb_writes_rejected_total` (counter, label `reason`): total `POST /write` calls refused with `409
  ConcurrencyException` since startup, by reason: `condition` (an Append Condition didn't hold, or was read on another
  store) or `projection` (a projection wasn't at the version given)
- `tamarackdb_write_duration_seconds` (histogram): how long each `POST /write` held the turn, from its turn to its end,
  however it ended

The two reasons for a refused write point at different things. A refused condition is real business contention: two
decisions raced on the same events. A refused projection is often operational: two instances of one projector running
at once, or a bulk delete during a rebuild.

## `GET /debug`

A JSON snapshot for digging into one specific slow write, a queue that keeps growing, or a read pool
that looks saturated, too detailed to fit a metric:

```json
{
  "time": "2026-09-01T14:23:05.123456Z",
  "write": {
    "active": {
      "kind": "write",
      "since": "2026-09-01T14:23:05.120000Z",
      "ageSeconds": 0.003
    },
    "queued": [
      {
        "kind": "projections",
        "queuedAt": "2026-09-01T14:23:05.121000Z",
        "waitSeconds": 0.002
      }
    ],
    "httpOpen": 2,
    "sqliteInUse": 1,
    "sqliteMax": 1
  },
  "read": {
    "httpOpen": 3,
    "sqliteInUse": 3,
    "sqliteMax": 8
  }
}
```

- `write.active`: the request holding the turn, with its `kind` and since when, or `null`. It never carries what the
  request reads or writes: the queue manager never knows it.
- `write.queued`: every request still waiting, oldest first, with its `kind` and `waitSeconds`. Always a list, never
  `null`.
- A `kind` is `write` (`POST /write`), `projections` (a bulk delete), `reset`, or `optimize`.
- `httpOpen`: requests in flight on each side. On the write side, every request that waits for the turn or holds it;
  on the read side, every read.
- `sqliteInUse` and `sqliteMax`: each SQLite connection pool's use against its size, straight from `database/sql`.
  `write.sqliteMax` is always `1` (see [SQLite](/docs/server-internals/sqlite/#connections)).
- `read.httpOpen` above `read.sqliteMax` means reads wait for a free connection. A sustained gap means `readPoolSize`
  is too small for the traffic.
- Answering `GET /debug` never waits behind a queued request or a running write: the queue's state has its own lock,
  and the pool counts come from `database/sql`.

A queue that keeps growing means writes take longer than they should, or arrive faster than they end: look at how
long writes hold the turn.
