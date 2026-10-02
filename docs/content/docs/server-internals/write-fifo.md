---
title: "The write FIFO"
description: "The write FIFO: one request at a time on the write connection, in strict arrival order, with no priority and a bounded queue, and why the design is that way."
slug: "write-fifo"
weight: 1
---

One request at a time touches the write connection. A queue manager hands out that single turn, strictly in the order
requests arrive.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## Who waits in it

| Kind | Request |
|---|---|
| `write` | [`POST /write`](/docs/http-api/write/) |
| `projections` | A [bulk delete](/docs/http-api/projections/#bulk-delete) of projections |
| `reset` | [`POST /reset`](/docs/http-api/reset/) |
| `optimize` | The hourly `PRAGMA optimize` (see [SQLite](/docs/server-internals/sqlite/#query-planner-statistics)) |

- Reads never wait in it (see [Reads](/docs/server-internals/reads/)).
- The queue manager knows nothing about what a request will read or write. It knows its kind, for
  [observability](/docs/operations/observability/).
- A request is in one of two states: **Active** (at most one at a time, the only one allowed to touch the write
  connection) or **Queued** (waiting its turn).

## A request's path

1. The HTTP handler reads and checks the request body, outside the FIFO.
2. It asks to join the line. If `maxQueuedWrites` requests already wait, it's turned away at once with
   `503 WriteQueueFull`, instead of joining.
3. Otherwise it waits, with its HTTP connection held open, until every request ahead of it is done.
4. If the client disconnects while waiting, the request leaves the line at once, and everyone behind it moves up one
   spot. There is no other way out of the line.
5. At the head of the line, the handler checks once more that the client is still there. Then it runs its work on the
   write connection.
6. From that moment, the work runs with a context the client can't cancel (`context.WithoutCancel`).
7. When the work ends, the request gives the turn to the next one.

**Why the context can't be cancelled.** `database/sql` rolls a SQLite transaction back when its context is cancelled.
If the client left halfway through a write, the write would be undone at a random point. A write that has started
goes to the end instead, and the client deals with not knowing the outcome (see
[A lost response](/docs/http-api/write/#a-lost-response)).

## Rules

- The FIFO MUST serve requests strictly in arrival order, with no priority between kinds.
- It MUST be bounded: there is no "no limit" value for `maxQueuedWrites`.
- Nothing outlives a request: no transaction stays open between two requests, and nothing on the server has to expire.
  A request holds the turn only for its own work, usually a few milliseconds.

**Why no priority.** Letting a bulk delete of projections go first would gain nothing, since the events of the writes
waiting ahead of it must be written anyway. It would also do harm: a waiting write that replaces or deletes a
projection the bulk delete removed would then get `409`, and be refused whole, its events included.

**Why a bound.** A FIFO with no bound would let a burst, or a broken client, pile up an unlimited number of blocked
HTTP connections.

**Why no wait timeout.** Each request ahead holds the turn only for its own work. A client that wants a shorter wait
closes the connection.

## Shutdown

When the server shuts down, the FIFO closes: every request still waiting, and every later one, gets
`503 ShuttingDown`. A request already holding the turn finishes. See [Lifecycle](/docs/server-internals/lifecycle/).
