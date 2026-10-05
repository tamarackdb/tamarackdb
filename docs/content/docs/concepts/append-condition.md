---
title: "Append Condition"
description: "How an Append Condition protects a decision: what a transaction builds from each read, its edge cases, what it doesn't cover, and DCB compliance."
slug: "append-condition"
weight: 6
---

An Append Condition makes a write fail if something relevant happened since the client read. It's how a decision is
protected against concurrent writes.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## The flow

TamarackDB runs the standard DCB flow, optimistically, in a [transaction](/docs/concepts/transactions/):

1. Read the relevant events.
2. Decide on the new events to append.
3. Write them. The server checks at commit that no event matching the read was appended since.
4. If another write appended a matching event in between, the commit gets `409 ConcurrencyException` and nothing is
   written. Run the command again: read again, decide again.

A read never locks anything. The decision is made on what was read, and the commit carries what the decision depends
on. The client never builds or sends a condition: the server builds one from each read.

## Rules

- Each read of events in a transaction becomes a condition: its query, the Sequence Position the read reached, and
  the store ID.
- The condition fails if any committed event matching the query has a Sequence Position greater than the one the read
  reached. The read reaches the last event in the store, matching or not.
- A read that finds nothing still protects its decision. A command that checks "no `user-registered` event with this
  email" reads with that query, finds nothing, and its commit fails if a matching event is appended after the read.
- A condition on `"none"` always holds: no event can match it.
- A commit carries one condition per read, and every one MUST hold. The first one that fails ends the commit, and the
  `409` names it by the rank of its read: `conditions[1] no longer holds` for the second read.
- The server checks conditions against committed events only, in the same SQLite transaction as the inserts. Nothing
  can slip in between the checks and the inserts: writes are served one at a time, in the order they arrive.
- A Sequence Position only means something next to its store ID. The store ID never changes while a transaction
  lives (see [Store ID](/docs/concepts/store-id/)).

**Why a list.** Each read supports its own decision. Merging the conditions into one, with OR, would refuse a commit
whenever any of the queries matched anything after the earliest position. A list is more precise, and still never a
partial success.

## What a condition doesn't cover

- **Projections.** A decision rests on events, never on a projection. A write checks the projections it changes, by
  their version, and nothing else. A projection that was only read isn't checked, even in a transaction: a projection
  can be stale the moment it's read.
- **A condition left out, or too narrow.** It leaves a race that no error reports. Describing what a decision depends on
  is the application's job.

## DCB compliance

TamarackDB follows the [DCB specification](https://dcb.events/specification/):

| Requirement | Level | TamarackDB |
|---|---|---|
| Read events filtered by type and/or tags through a Query | MUST | [`QUERY /events`](/docs/http-api/read-events/) |
| Read from a given Sequence Position | SHOULD | `afterSequence` on `QUERY /events` |
| Append one or more events atomically | MUST | Every [commit](/docs/http-api/transactions/#commit) writes atomically |
| Fail the append if an event matches the Append Condition, when one is given | MUST | Every read in a transaction |

Its guarantee is exactly the one of the specification, no more and no less. Only what an Append Condition or a
projection version expresses is protected. A broader guarantee is a business rule of the application, not something the
event store enforces.
