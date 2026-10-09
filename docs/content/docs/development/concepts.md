---
title: "Concepts"
description: "The ideas the TamarackDB API builds on: events and tags, the Append Condition, transactions, and projections, on a single page."
slug: "concepts"
weight: 2
---

The ideas the API builds on. TamarackDB follows the [DCB specification](https://dcb.events/specification/): a command
reads the events it needs, decides, and writes new events, and the write fails if the events it read have changed.

## Events

An event is a fact the application recorded. Once written, it never changes and is never removed.

| Field | Set by | What it is |
|---|---|---|
| `type` | the client | A non-empty string naming what happened, such as `user-renamed` |
| `identifiers` | the client | Tags that name things in the domain: `userId`, `courseId` |
| `metadata` | the client | Tags for everything else: author, correlation, tenant |
| `payload` | the client | A string. The server stores it and never interprets it |
| `sequence` | the server | The event's Sequence Position: its place in the log |
| `time` | the server | When the server received the write that carries it |

- A tag is a name and a value. On the wire, each set of tags is an object whose values are a string or an array of
  strings: `{ "courseId": ["foo", "bar"], "otherId": "baz" }` carries three tags.
- `identifiers` and `metadata` are separate: a query for a `userId` identifier doesn't match a `userId` metadata entry.
- An event carries at most 20 identifiers and 20 metadata entries, with no duplicate. An empty array as a value is
  refused: leave the key out instead.
- `payload` can be empty (`""`), but never missing or `null`. Its format is up to the application.
- `sequence` starts at 1 and grows by one with each event, with no gap. It's the only order to rely on.
- `time` is in UTC, with six digits after the second: `2026-09-01T14:23:05.123456Z`. Every event of one write shares
  it. Don't order events by `time`, and don't base a decision on it: the clock can go back.
- An event is at most `maxEventSize` bytes, counting its `type`, its tags, and its `payload`. Store large content
  elsewhere and reference it.

## Queries

A query selects events by type and tags. It's `"all"`, `"none"`, or a list of items:

```json
[
  {
    "types": ["user-created", "user-updated"],
    "identifiers": [
      {"name": "userId", "value": "123"}
    ]
  },
  {
    "types": ["some-other-event"]
  }
]
```

- An event matches the query if it matches any item.
- Within an item, the event's type is one of `types`, and the event carries every listed identifier and metadata entry.
- Values are compared exactly, byte for byte.
- The full grammar is in the [HTTP API](/docs/development/http-api/#queries).

## Append Condition

An Append Condition makes a write fail if an event relevant to the decision was appended since the read.

1. A command reads the events its decision needs.
2. It decides which events to append.
3. It writes them. At commit, the server checks that no event matching the read was appended since.
4. If one was, the commit gets `409 ConcurrencyException` and nothing is written. The command runs again: it reads
   again, and decides again.

- A read never locks anything.
- The client never builds a condition: in a transaction, the server makes one from each read.
- A read that finds nothing still protects its decision. A check that "no user has this email" fails at commit if a
  matching event was appended in between.
- Only what a condition covers is protected. A condition too narrow leaves a race that no error reports.

## Transactions

A transaction groups what one command reads and writes. It lives in the server's memory, locks nothing, and writes
everything at commit, or nothing.

1. **Begin.** The server returns a transaction ID.
2. **Decide.** Each decision reads events, then writes its events, or an empty list.
3. **Projections.** Between two decisions, the command reads and writes projections.
4. **Commit.** The server checks every condition and every projection, then writes everything at once.

- A read of events opens a condition. The next call must be the write of events that closes it, even with an empty
  list. The decision to write nothing is checked at commit too.
- A decision that rests on no event reads `"none"` first.
- To read events without making a decision, use `QUERY /events`, outside the transaction.
- A read in a transaction sees committed events, then the transaction's own pending events. A pending event has no
  `sequence` until the commit.
- After a `409`, run the whole command again, in a new transaction.
- A transaction ends at its commit, when it's abandoned, at its first error, after `txIdleTimeout` without a call, or
  when the server stops. Then every call on it gets `404 TransactionNotFound`.
- One call at a time on a transaction. A second call sent at the same time gets `409 TransactionBusy`.

## Projections

A projection is state the application computes from events, stored next to them. Using projections is optional.

- A projection is a string payload, identified by `type` and `id`. It has no history: only its current state is kept.
- It's read by `type` and `id`. There is no query over projections.
- Every write gives a projection a new version. A write changes a projection with `create` (it must not exist),
  `replace`, or `delete` (it must still be at the version read). Otherwise the write gets `409`.
- In a transaction, the server tracks the versions. Outside one, with `POST /projections`, the client sends them.
- A version is opaque: compare it for equality only.
- A projection computed in a transaction may use the `time` of its events, never their `sequence`.

### Rebuilds

Every projection can be rebuilt from events. Backups leave projections out for that reason.

1. Optionally, [pause](/docs/development/http-api/#pause-and-resume) transactions to rebuild up to a fixed point.
2. Delete the projections with a bulk delete.
3. Read the events with `QUERY /events`, page by page.
4. Write the projections with `POST /projections`, in one write or several.
5. Resume.
