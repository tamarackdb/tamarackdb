---
title: "Projections"
slug: "projections"
weight: 7
---

A projection is the current state a projector computes from events. Storing projections in TamarackDB is optional:
an application that keeps them elsewhere never touches them.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## What a projection is

- An opaque string payload, identified by `type` + `id`. The `type` is like a class, and each projection is one
  instance of it.
- It has no history: the store only holds its current state. It can be replaced or deleted.
- It's always read by `type` and `id`. There is no query over projections.
- Every projection can be rebuilt from events. That's why backups leave projections out (see
  [Backup](/docs/backup/)).
- Projections live in the same SQLite file as events, and are written by the same
  [`POST /write`](/docs/http-api/write/). Events and projections of one write become durable together, or neither
  does.
- The payload's format (JSON, XML, plain text) is up to the writing application. The store never parses it.

The calls are in [HTTP API: Projections](/docs/http-api/projections/).

## Versions

Every projection has a version, a random UUID (version 4), new on every write.

- A write changes a projection with one of three operations:
  - `create`: the `type` + `id` MUST be free.
  - `replace`: the stored version MUST be the one given. The whole payload is replaced.
  - `delete`: the stored version MUST be the one given.
- An operation that doesn't hold fails the whole write with `409 ConcurrencyException`, events included. A `replace`
  or `delete` of a projection that no longer exists fails the same way: another write deleted it since it was read.
- A projection saved without being read first goes out as a `create`, and fails if the projection exists: read
  before you write.
- A `409` on a projection means another write changed it since it was read. Read it again and redo the work, in a new
  transaction.
- Only the projections a write changes are checked, never the ones it only read (see
  [Append Condition](/docs/concepts/append-condition/#what-a-condition-doesnt-cover)).
- A client MUST treat the version as opaque: compare it only for equality, and never compute it. To change the same
  projection again later, use the version from the last response.

**Why.** The version check is what keeps two transactions from overwriting each other's projections: each one read a
version, and the second write to arrive finds it changed. An opaque UUID can't be guessed from the previous one, and
since it never repeats, a stale copy never matches again, even after the projection is deleted and created anew, or
after a rebuild.

A projection doesn't carry who wrote it, so the server can't keep two projectors from touching the same projections.
Keeping projectors apart is the application's job (see
[Transactions](/docs/concepts/transactions/#where-a-transaction-ends)).

## What a projection may depend on

It depends on where the application ends its transactions (see
[Transactions](/docs/concepts/transactions/#where-a-transaction-ends)):

- **Atomic**: projectors run before the write, while the events are still pending in the client library, so the
  events have no `sequence` or `time` yet. A projection MUST NOT use them. A business date goes in the payload or the
  metadata.
- **Eventually consistent**: projectors read events that are already written, so a projection MAY use `sequence`
  and `time` too.

Either way, a rebuild reads the same events back, so it produces the same projections.

## Rebuilds

A rebuild replays events to write projections again. How it's organized is up to the application: one thread
replaying every event in order, several projectors in parallel, or anything else. It's built from ordinary calls:

1. Delete the projections to rebuild, one type at a time or all at once, with a
   [bulk delete](/docs/http-api/projections/#bulk-delete).
2. Page through [`QUERY /events`](/docs/http-api/read-events/) to read the events to replay.
3. Write the rebuilt projections with [`POST /write`](/docs/http-api/write/), in one write or several. A projection
   is a `create` the first time, then a `replace` with the version the previous write returned.

One write or several is the application's choice:

- **One write** MUST fit under `maxProjectionsPerWrite` and `maxRequestBodySize`. It holds the write turn for as long
  as its inserts take, with every other write waiting behind it.
- **Several writes** each fit the limits. A projector writes its position (store ID and Sequence Position) with each
  one, so a rebuild that stops halfway resumes from there.

Either way, the bulk delete is a separate call: between it and the writes, a read finds the deleted projections gone.

During a rebuild:

- Reads run side by side on the read connection pool.
- Writes from several threads each wait for their turn, one at a time. Each waiting write counts toward
  `maxQueuedWrites`: many writers at once can get `503 WriteQueueFull`, and retry.
- A write that replaces or deletes a projection a bulk delete already removed gets `409`.

**Why page the reads.** One read streaming the whole log would hold one SQLite read transaction open for the whole
rebuild. That pins a snapshot, and keeps the WAL from being checkpointed while the rebuild's own writes pile up in it.
Paging keeps each read short, so the WAL checkpoints between pages.

Giving back the disk space of deleted projections is a separate, manual step (see
[Maintenance](/docs/operations/maintenance/)).
