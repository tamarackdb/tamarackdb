---
title: "Transactions"
description: "How a transaction works: it lives in the client library, writes go out whole, conflicts are found at write time, and the application picks atomic or eventual."
slug: "transactions"
weight: 2
---

A transaction lives in the client library, not on the server. The server only ever receives complete writes.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## How a transaction works

- While a command runs, the client library keeps in memory what the command wants to write: new events, the Append
  Conditions they depend on, and projection changes. These are its **pending writes**.
- Reads made inside the transaction see the pending writes, merged into what the server returns (see
  [Client libraries](/docs/client-libraries/)).
- When the transaction ends, the library sends everything in one [`POST /write`](/docs/http-api/write/).
- The server checks the write and applies it in one SQLite transaction of its own: it checks that every Append
  Condition holds, and that every projection is still at the version read. Then it writes everything, or nothing.
- The server keeps nothing between two requests: no open transaction, no pending write, no timer.

**Why.** Kept on the server, transactions alive at the same time would have to be kept apart while they run, which is
hard to test and to reason about. In the library, a transaction belongs to one thread or request, and the server only
handles complete writes, one at a time.

## Many transactions at once

- Many transactions run at the same time, one per thread or request of the application.
- None of them holds anything on the server while it runs: reads never wait, and a slow client blocks no one.
- Conflicts between transactions are found when each write arrives, one write at a time, by its
  [Append Conditions](/docs/concepts/append-condition/) and projection
  [versions](/docs/concepts/projections/#versions).
- A transaction that became stale learns it then, with `409 ConcurrencyException`. No one is warned earlier.

## No frozen view

A transaction doesn't freeze a view of the store.

- Each read is a separate call, and sees what is committed at the moment it runs.
- Two reads of the same transaction can see different states: another client may write between them.
- What protects a decision is the condition built from each read, with its own `afterSequence`: the write is refused
  if an event that matters arrived after that read.
- Don't combine two reads as if they described the same moment without a condition on each one.

## Where a transaction ends

The application decides where a transaction ends, through its client library. TamarackDB doesn't know, and supports
both ways:

- **Atomic**: one transaction per command. The decision models, the processors, and the projectors all run, then one
  write carries every new event and every changed projection. Everything lands together, or nothing does. After a
  `409`, run the whole command again.
- **Eventually consistent**: the command writes its own events alone. Processors and projectors run later, each in
  its own transaction. Each projector keeps the position it has processed up to (see
  [Store ID](/docs/concepts/store-id/)) in the same write as its projections, and catches up from there. A processor
  that gets a `409` reads again and retries.

In the eventually consistent way, the application MUST give each projector its own projection types, plus its own
position, and run one instance of it. TamarackDB can't check this: a projection doesn't carry who wrote it. A `409` on
a projector's write then has an operational cause, such as two instances running at once, or a bulk delete during a
rebuild.

What a projection may use depends on this choice (see
[Projections](/docs/concepts/projections/#what-a-projection-may-depend-on)).

## The guarantee

The guarantee is exactly the one of the DCB specification (see
[DCB compliance](/docs/concepts/append-condition/#dcb-compliance)). Only what an Append Condition or a projection
version expresses is protected. A broader guarantee is a business rule of the application.
