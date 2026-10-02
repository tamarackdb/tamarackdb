---
title: "Sequence Position counter"
description: "How the server assigns Sequence Positions from a counter in memory: reserved only once a write holds, given back on failure, and used to skip checks."
slug: "sequence-counter"
weight: 2
---

TamarackDB assigns Sequence Positions itself, from a counter in memory, instead of leaving it to SQLite's
`AUTOINCREMENT`. This works because only one request ever touches the write connection (see
[The write FIFO](/docs/server-internals/write-fifo/)).

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## The counter

- `events.sequence` is a plain `INTEGER PRIMARY KEY`, set by the server on insert (see
  [Schema](/docs/server-internals/schema/)).
- When the store opens, the server reads the highest `sequence` in `events` and starts the counter after it. An empty
  table starts at 1, as `AUTOINCREMENT` would.
- [`POST /reset`](/docs/http-api/reset/) sets the counter back so the next event gets 1.

## Reserving positions

- A write MUST reserve its Sequence Positions only after every condition holds and every projection is written, never
  before.
- It inserts its events with them in the same call.
- Unless the commit succeeds (the insert fails, the commit fails, or the code panics), the write MUST give its
  positions back before it ends.

**Why.** A failed write then leaves no gap in the sequence. Writing projections before reserving means a projection
conflict ends the write before any position is taken.

## Batched inserts

Knowing every event's sequence up front lets a write insert its `events` rows in one multi-row `INSERT`, then one into
`identifiers` and one into `metadata`, instead of a round trip per event to fetch an ID before writing its tags.

`PRAGMA foreign_keys = ON` is still checked right away, not at commit: a row in `identifiers` or `metadata` can't
point to an `event_sequence` that doesn't exist yet in `events`, even within the same transaction.

## Skipping the condition check

The counter often answers an Append Condition without any SQL:

- If a condition's `afterSequence` equals the last assigned position, no event exists after it, so
  `failIfEventsMatch` can't match anything: the condition holds.
- A condition with `afterSequence` and no `failIfEventsMatch` asks only "does any event exist after it?": the counter
  answers it directly.

The shortcut applies to each condition of a write on its own. It changes how the decision is reached, never the
decision itself.
