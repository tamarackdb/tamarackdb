---
title: "Append Condition"
description: "How an Append Condition protects a decision: its shape, its edge cases, several conditions per write, what it doesn't cover, and TamarackDB's DCB compliance."
slug: "append-condition"
weight: 6
---

An Append Condition makes a write fail if something relevant happened since the client read. It's how a decision is
protected against concurrent writes.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## The flow

TamarackDB runs the standard DCB flow, optimistically:

1. Read the relevant events, and keep the Sequence Position read up to (`afterSequence`) and the store ID.
2. Decide on the new events to append.
3. Write them with a condition: fail if an event matching the query exists after `afterSequence`.
4. If another write appended a matching event in between, the write gets `409 ConcurrencyException` and nothing is
   written. Read again, decide again, and send a new write.

A read never locks anything. The decision is made on what was read, and the write carries what the decision depends on.

## Shape

```json
{
  "failIfEventsMatch": [
    { "identifiers": [ { "name": "userId", "value": "123" } ] }
  ],
  "afterSequence": 12345,
  "store": "5b0c7e2a-1f4d-4a9b-8c3e-6d2f1a0b9e47"
}
```

- `failIfEventsMatch`: a query, in the same [grammar](/docs/http-api/query-grammar/) as a read.
- `afterSequence`: the Sequence Position the client read up to. It can be past the last matching event: it's what the
  client saw, not necessarily a real event.
- `store`: the store ID that read returned (see [Store ID](/docs/concepts/store-id/)).

## Rules

- The condition fails if any event matching `failIfEventsMatch` has a Sequence Position greater than `afterSequence`.
- A condition with `afterSequence` MUST carry `store`. A condition without `afterSequence` MUST NOT carry it: it read
  nothing, so it holds on any store. Breaking either rule gets `400`.
- A condition whose `store` isn't the current store ID fails with `409`: its `afterSequence` names a position in another
  history. The server decides this without any SQL.
- Both `failIfEventsMatch` and `afterSequence` are optional:
  - `failIfEventsMatch` alone fails if any matching event exists at all, from Sequence Position 1. It suits a decision
    that rests on no read, for example "fail if a `user-registered` event with this email exists".
  - `afterSequence` alone fails if any event at all exists after it.
  - A condition with neither field always holds: it says nothing, so it protects nothing. A rewrite MUST NOT read an
    empty condition as `afterSequence: 0`, which would fail as soon as the store holds one event.
- A write carries a list of conditions, at most `maxEventsPerWrite` of them, and every one MUST hold. The first one that
  fails ends the write, and the `409` names it by its place: `conditions[1] no longer holds`, or `conditions[0] was read
  on another store`.
- The server checks conditions against committed events only, in the same SQLite transaction as the inserts. Nothing can
  slip in between the checks and the inserts (see [The write FIFO](/docs/server-internals/write-fifo/)).

**Why a list.** A transaction usually has one condition per decision: each decision model or processor adds the
condition its own read supports. Merging them into one condition with OR would refuse a write whenever any of the
queries matched anything after the earliest position. A list is more precise, and still never a partial success.

## What a condition doesn't cover

- **Projections.** A decision rests on events, never on a projection. A write checks the projections it changes, by
  their version, and nothing else. A projection the transaction only read isn't checked: the server never learns what a
  transaction read, and a projection can be stale the moment it's read.
- **Pending events of the same transaction.** A decision can also go stale because of a pending event added after its
  read, by another processor of the same command for example. Only the client library knows the order of its reads and
  pending events, so that check is the library's (see [Client libraries](/docs/integration/client-libraries/)).
- **A condition left out, or too narrow.** It leaves a race that no error reports. Describing what a decision depends on
  is the application's job.

## DCB compliance

TamarackDB follows the [DCB specification](https://dcb.events/specification/):

| Requirement | Level | TamarackDB |
|---|---|---|
| Read events filtered by type and/or tags through a Query | MUST | [`QUERY /events`](/docs/http-api/read-events/) |
| Read from a given Sequence Position | SHOULD | `afterSequence` on `QUERY /events` |
| Append one or more events atomically | MUST | Every [`POST /write`](/docs/http-api/write/) commits atomically |
| Fail the append if an event matches the Append Condition, when one is given | MUST | `conditions` on `POST /write`, optional for the client |

Its guarantee is exactly the one of the specification, no more and no less. Only what an Append Condition or a
projection version expresses is protected. A broader guarantee is a business rule of the application, not something the
event store enforces.
