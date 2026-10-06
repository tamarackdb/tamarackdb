---
title: "Optimize"
description: "POST /optimize: refreshes the statistics SQLite plans queries with, in its turn, for an operator's daily timer. The server also runs it at every start."
slug: "optimize"
weight: 8
---

`POST /optimize` refreshes the statistics SQLite uses to plan queries. The log only grows, so statistics gathered once
get further from the data: queries slow down, without ever giving wrong results. An operator's timer calls it once a
day (see [Maintenance](/docs/operations/maintenance/#refreshing-query-statistics)).

```sh
curl -X POST http://127.0.0.1:8085/optimize
```

## What it does

- It runs SQLite's `PRAGMA optimize`, which analyzes again only the tables that changed enough to matter.
- It waits for its turn like a write (see [Waiting for a turn](/docs/http-api/conventions/#waiting-for-a-turn)), and
  can get `503 WriteQueueFull` or `503 ShuttingDown`.
- It responds `204 No Content` once done.
- It runs whatever the state of a [pause](/docs/http-api/pause/): it touches neither events nor transactions.
- `GET /stats` shows when it last succeeded, in `lastOptimizeAt` (see
  [Observability](/docs/operations/observability/)).
- The server also runs it at every start. If it fails there, the server logs the error and starts anyway.

**Why a timer outside the server.** The server runs nothing on a timer of its own. The operator picks the time, for
example at night, as for backups. `lastOptimizeAt` shows a timer that was forgotten.
