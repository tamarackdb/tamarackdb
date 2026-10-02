---
title: "Reads"
description: "How reads run on their own connection pool, see a steady SQLite snapshot with the store ID in it, never wait for a write, and can't stall the pool."
slug: "reads"
weight: 3
---

Reads never wait for a write, and never take the write connection.

## How a read runs

- A read (`QUERY /events`, `GET /projections/{type}/{id}`) runs on the read connection pool, sized by
  `readPoolSize` (see [Configuration](/docs/operations/configuration/)). It doesn't go through the
  [write FIFO](/docs/server-internals/write-fifo/).
- Consistency comes from SQLite's WAL mode (MVCC): a read sees a steady snapshot of committed data from the moment it
  starts. It never sees a write in progress, and a write never blocks it.
- A read that starts just before a commit doesn't see the new events. That's fine: a client that then writes uses the
  Sequence Position it actually read as `afterSequence`.

## Store ID in the same snapshot

- Each read runs in its own read transaction: it reads the store ID first, then the page or the projection.
- SQLite takes the snapshot at the first statement, so both come from the same one. A response never pairs the data
  of one store ID with another, even if a reset commits in between (see
  [Store ID header](/docs/http-api/conventions/#store-id-header)).
- For a `QUERY /events` page, the transaction ends once the page is fully sent.

## A stalled page

- A `QUERY /events` page holds its read connection, and pins its snapshot, until the page is fully sent.
- Each line of the page gets 30 seconds to go out, renewed on every line. A page that keeps moving is never cut, however
  slow the client. A client that stops reading loses its connection after 30 seconds, and the read connection goes
  back to the pool.
- The client sees a page with no trailer, and resumes like after any dropped connection (see
  [A page cut short](/docs/http-api/read-events/#a-page-cut-short)).

**Why.** Without this limit, `readPoolSize` stalled clients would block every read, `/health` included, and keep the
WAL from being checkpointed.
