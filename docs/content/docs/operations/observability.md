---
title: "Observability"
description: "GET /stats: counters of writes, conflicts, transactions, and errors since the server started, and what each one tells about the instance and its clients."
slug: "observability"
weight: 5
---

`GET /stats` returns counters since the server started: the writes, their conflicts, the life of transactions, and the
internal errors. Whether the server is up is [`GET /health`](/docs/operations/health-check/)'s job.

```sh
curl http://127.0.0.1:8085/stats
```

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
    "paused": 0
  },
  "pause": {
    "state": "normal",
    "since": "2026-10-03T14:00:00.000000Z",
    "openTransactions": 0
  },
  "errors": { "internal": 0 }
}
```

- The counters only add up: a rate is the difference between two calls.
- They start from zero when the server starts. `startedAt` says when that was.
- When `enableAuth` is on, it needs the token like every other route (see
  [Security](/docs/operations/security/#bearer-token)).
- Its format is not part of the API: it may change from one version to the next.

## Counters

| Counter | What it counts |
|---|---|
| `lastOptimizeAt` | Not a counter: when the query statistics were last refreshed, at startup or by [`POST /optimize`](/docs/http-api/optimize/). `null` until the first success |
| `writes.committed` | Writes committed: transaction commits that wrote something, and `POST /projections` calls |
| `writes.conflicts.condition` | Transaction commits refused with `409` because a condition no longer held |
| `writes.conflicts.projection` | Writes refused with `409` because a projection wasn't at the version read: transaction commits and `POST /projections` calls |
| `writes.writeQueueFull` | Requests turned away with `503 WriteQueueFull` |
| `transactions.begun` | Transactions begun |
| `transactions.committed` | Transactions committed, including those with nothing to write |
| `transactions.abandoned` | Transactions ended with `DELETE /tx/{txId}` |
| `transactions.expired` | Transactions ended after `txIdleTimeout` without a call |
| `transactions.designErrors` | Transactions ended by a call that broke a rule, or by a malformed request. An event or a projection over its size limit doesn't count |
| `transactions.paused` | `POST /tx` calls refused with `503 Paused` |
| `pause.state` | Where the [pause](/docs/http-api/pause/) stands: `normal`, `pauseRequested`, or `paused` |
| `pause.since` | When that state began |
| `pause.openTransactions` | Transactions still open, and commits still writing: what a requested pause waits for |
| `errors.internal` | Responses `500 InternalError` |

## Reading them

- **Condition conflicts** are business contention: two decisions raced on the same events. A steady rate is normal
  under load. A rising one points at decisions that read more than they need.
- **Projection conflicts** are often operational: two instances of one projector running at once, or a bulk delete
  during a rebuild.
- **`writeQueueFull`** above zero means `maxQueuedWrites` is too low for the traffic, or writes hold the turn too long
  (see [Sizing the write queue](/docs/operations/configuration/#sizing-the-write-queue)).
- **`expired`** rising means the application begins transactions and forgets them: it doesn't commit or abandon them
  on every path.
- **`designErrors`** SHOULD stay at zero in production. A rising count means a client library sends calls in an order
  transactions never allow: it has a bug.
- **`pauseRequested`** that lasts means a transaction is still open, or the coordinator stopped calling `POST /pause`:
  `openTransactions` says how many are open. `POST /resume` withdraws it.
- **`lastOptimizeAt`** more than a day old means the timer that calls `POST /optimize` doesn't run (see
  [Maintenance](/docs/operations/maintenance/#refreshing-query-statistics)).
- **`internal`** above zero is a failure on the server: the matching log lines are at level `error` (see
  [Logs](/docs/operations/logs/)).
