---
title: "Projections"
description: "What a projection is, how versions keep two writes from overwriting each other, what a projection may use, and how to rebuild projections from events."
slug: "projections"
weight: 7
---

A projection is the current state an application computes from events. Storing projections in TamarackDB is optional:
an application that keeps them elsewhere never touches them.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## What a projection is

- An opaque string payload, identified by `type` + `id`. The `type` is like a class, and each projection is one
  instance of it.
- It has no history: the store only holds its current state. It can be replaced or deleted.
- It's always read by `type` and `id`. There is no query over projections.
- Every projection can be rebuilt from events. That's why backups leave projections out (see
  [Backup](/docs/operations/backup/)).
- Projections live in the same SQLite file as events. A transaction's commit writes both: its events and projections
  become durable together, or neither does. Outside a transaction,
  [`POST /projections`](/docs/http-api/projections/#writing-projections) writes projections only.
- The payload's format (JSON, XML, plain text) is up to the writing application. The store never parses it.

The calls are in [HTTP API: Projections](/docs/http-api/projections/), and in
[HTTP API: Transactions](/docs/http-api/transactions/).

## Versions

Every projection has a version, a random UUID (version 4), new on every write.

- In a transaction, the server keeps the version of each projection read, and the client never sees it (see
  [Transactions](/docs/concepts/transactions/#projections-in-a-transaction)). At commit, the server checks it the same
  way as below.
- With [`POST /projections`](/docs/http-api/projections/#writing-projections), a write changes a projection with one
  of three operations:
  - `create`: the `type` + `id` MUST be free.
  - `replace`: the stored version MUST be the one given. The whole payload is replaced.
  - `delete`: the stored version MUST be the one given.
- An operation that doesn't hold fails the whole write with `409 ConcurrencyException`. A `replace` or `delete` of a
  projection that no longer exists fails the same way: another write deleted it since it was read.
- A `409` on a projection means another write changed it since it was read. Read it again and redo the work, in a new
  transaction or a new write.
- Only the projections a write changes are checked, never the ones it only read (see
  [Append Condition](/docs/concepts/append-condition/#what-a-condition-doesnt-cover)).
- A client MUST treat a version it receives as opaque: compare it only for equality, and never compute it. To change
  the same projection again with `POST /projections`, use the version from the last response.

**Why.** The version check is what keeps two writes from overwriting each other's projections: each one read a
version, and the second write to arrive finds it changed. An opaque UUID can't be guessed from the previous one, and
since it never repeats, a stale copy never matches again, even after the projection is deleted and created anew, or
after a rebuild.

A projection doesn't carry who wrote it, so the server can't keep two writers from touching the same projections.
Keeping them apart is the application's job.

## What a projection may use

A rebuild reads the same events back, so it must produce the same projections. What a projection may use depends on
when it's computed:

- **In a transaction**, from events of the same transaction: a projection MAY use their `time`, which is stored as is
  at commit. It MUST NOT use their `sequence`: a pending event has none yet.
- **From committed events**, read with [`QUERY /events`](/docs/http-api/read-events/): a projection MAY use both
  `sequence` and `time`.

## Rebuilds

A rebuild replays events to write projections again. How it's organized is up to the application: one thread
replaying every event in order, several threads in parallel, or anything else. It's built from ordinary calls:

1. Delete the projections to rebuild, one type at a time or all at once, with a
   [bulk delete](/docs/http-api/projections/#bulk-delete).
2. Page through [`QUERY /events`](/docs/http-api/read-events/) to read the events to replay.
3. Write the rebuilt projections with [`POST /projections`](/docs/http-api/projections/#writing-projections), in one
   write or several. A projection is a `create` the first time, then a `replace` with the version the previous write
   returned.

One write or several is the application's choice:

- **One write** MUST fit under `maxProjectionsPerWrite` and `maxRequestBodySize`. It holds the write turn for as long
  as its inserts take, with every other write waiting behind it.
- **Several writes** each fit the limits. The application writes its position (store ID and Sequence Position) with each
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
