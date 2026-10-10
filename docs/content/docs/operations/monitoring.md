---
title: "Monitoring"
description: "Watching a TamarackDB instance: GET /health for a supervisor or a load balancer, the GET /stats counters and what they mean, and the access log."
slug: "monitoring"
weight: 5
---

Watching an instance: whether it's up, what it does, and what goes wrong. With `enableAuth` on, `/health` and `/stats`
need the token like every other route.

## Health check

`GET /health` tells a supervisor or a load balancer whether the server can serve requests.

```sh
sudo -u tamarackdb curl --unix-socket /run/tamarackdb/tamarackdb.sock http://localhost/health
```

```json
{ "status": "ok", "paused": false, "version": "1.2.3" }
```

- `200 OK` when the server responds and its database can be reached. A paused server still answers `200`.
- `503` when the database can't be reached.
- `paused` is `true` once a [pause](/docs/operations/maintenance/#pausing-transactions) is in place.

## Counters

`GET /stats` returns counters since the server started. A rate is the difference between two calls. The format may
change from one version to the next.

```json
{
  "startedAt": "2026-10-03T14:00:00.000000Z",
  "lastOptimizeAt": "2026-10-04T03:00:00.000000Z",
  "writes": {
    "committed": 18234,
    "conflicts": { "condition": 12, "projection": 3 },
    "writeQueueFull": 0
  },
  "transactions": {
    "begun": 18301,
    "committed": 18234,
    "abandoned": 40,
    "expired": 2,
    "designErrors": 0,
    "paused": 0,
    "busy": 0,
    "tooMany": 0
  },
  "pause": {
    "state": "normal",
    "since": "2026-10-03T14:00:00.000000Z",
    "openTransactions": 0
  },
  "errors": { "internal": 0 }
}
```

What to watch:

- **`writes.conflicts.condition`**: two commands decided on the same events at the same time. A steady rate is normal
  under load.
- **`writes.conflicts.projection`**: often two copies of one projector running at once.
- **`writes.writeQueueFull`** above zero: `maxQueuedWrites` is too low, or writes are too large (see
  [Configuration](/docs/operations/configuration/#write-queue)).
- **`transactions.expired`** rising: the application begins transactions and forgets them.
- **`transactions.designErrors`** or **`transactions.busy`** above zero: the client library has a bug.
- **`transactions.tooMany`** above zero: `maxOpenTx` is too low, or the application begins transactions and never
  ends them (see `expired`).
- **`pause.state`** stuck at `pauseRequested`: transactions are still open (`openTransactions`), or whoever asked for
  the pause stopped calling `POST /pause`.
- **`lastOptimizeAt`** more than a day old: the daily `POST /optimize` doesn't run (see
  [Maintenance](/docs/operations/maintenance/#query-statistics)).
- **`errors.internal`** above zero: a failure on the server. The log has the matching `error` lines.

## Logs

The server logs one line per request to stdout:

```
tamarackdb-server: [WARNING] POST /projections 503 27B 0.07ms
```

- A line holds the level, the method, the path, the status, the response size, and the time taken. Never a body.
- `logLevel` sets the lowest level written. The default, `warning`, shows only problems. Use `debug` to see every
  request.
- `debug`: a success, or an expected refusal such as a conflict.
- `info`: a client's mistake, such as a malformed request or a bad token, or a pause, a resume, an optimize, or a bulk delete.
- `warning`: the write queue is full, or `maxOpenTx` transactions are open.
- `error`: a failure on the server.
