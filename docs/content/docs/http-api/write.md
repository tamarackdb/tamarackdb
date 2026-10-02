---
title: "Writing"
description: "POST /write: one write with events, Append Conditions, and projection changes, applied all or nothing. Its limits, response, conflicts, and safe retries."
slug: "write"
weight: 4
---

`POST /write` sends one write: the events to append, the Append Conditions they depend on, and the projection
changes. It's checked and applied all or nothing, in its turn.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## Request

```sh
curl -X POST http://127.0.0.1:8085/write \
  -H "Content-Type: application/json" \
  -d '{
    "events": [
      {
        "type": "user-renamed",
        "identifiers": { "userId": "123" },
        "metadata": { "tenantId": "acme" },
        "payload": "{\"name\":\"Ada Lovelace\"}"
      }
    ],
    "conditions": [
      {
        "failIfEventsMatch": [ { "identifiers": [ { "name": "userId", "value": "123" } ] } ],
        "afterSequence": 12345,
        "store": "5b0c7e2a-1f4d-4a9b-8c3e-6d2f1a0b9e47"
      }
    ],
    "projections": {
      "create":  [ { "type": "user-list-entry", "id": "789", "payload": "..." } ],
      "replace": [ { "type": "user-profile", "id": "123", "version": "9f3c2a1e-7b4d-4c8e-a5f6-0d1e2f3a4b5c", "payload": "..." } ],
      "delete":  [ { "type": "user-list-entry", "id": "456", "version": "1b2c3d4e-5f60-4718-9a0b-c1d2e3f4a5b6" } ]
    }
  }'
```

| Key | What it holds |
|---|---|
| `events` | Events to append: `type`, `identifiers`, `metadata`, `payload` (see [Events](/docs/concepts/events/)) |
| `conditions` | Append Conditions (see [Append Condition](/docs/concepts/append-condition/)) |
| `projections.create` | `type`, `id`, `payload` |
| `projections.replace` | `type`, `id`, `version`, `payload` |
| `projections.delete` | `type`, `id`, `version` |

- Every key is optional, and a missing one is an empty list. The body is decoded strictly (see
  [Conventions](/docs/http-api/conventions/#request-bodies)).
- A projection `payload` is a string, and an empty string is valid. A missing or `null` payload gets `400`, so a key
  dropped on the client (JavaScript's `JSON.stringify` drops `undefined` values) never writes an empty payload.
- The same `type` + `id` MUST NOT appear twice in one write, across the three lists. So the order in which the lists
  are applied doesn't matter.

**Why three lists.** One list with an operation flag would leave combinations of keys to forbid. Three lists with a
fixed shape each leave none.

## Limits

Each limit is a setting, with its default in [Configuration](/docs/operations/configuration/#settings).

| Limit | Over it |
|---|---|
| `maxEventsPerWrite` events | `400` |
| `maxEventsPerWrite` conditions | `400` |
| `maxProjectionsPerWrite` projections, across the three lists | `400` |
| `maxEventSize` bytes per event (see [Events](/docs/concepts/events/#size)) | `413` |
| `maxProjectionSize` bytes per projection: its `type`, `id`, and `payload` together | `413` |
| `maxRequestBodySize` bytes for the whole body | `413` |

- These limits count everything one transaction of the application writes, since it all goes out in one write.
- Each error from a limit names its setting, for example `request carries 612 projections, more than
  maxProjectionsPerWrite (500)`, so a developer who hits one in development knows which setting to raise.
- The defaults are a cautious starting point: find the real limits in development, with the application's data, and
  set them for production (see [Configuration](/docs/operations/configuration/)).

## What the server does

1. It reads and checks the whole body, before the write waits for its turn. An invalid body gets `400` or `413` right
   away.
2. It waits for its turn in the write FIFO (see [Waiting for a turn](#waiting-for-a-turn)).
3. In its turn, it runs one SQLite transaction:
   1. Every Append Condition is checked, in order, against the events committed before this write. The first that
      doesn't hold ends the write.
   2. The projections are written, each one conditional on its version. The first that doesn't hold ends the write.
   3. The events get their Sequence Positions and their `time`, and are inserted.
   4. The transaction commits.

- Events and projections become durable together, or nothing is written.
- Conditions are checked even when the write carries nothing else.
- A write with nothing at all (no event, no condition, no projection) responds `200` right away, without waiting for a
  turn.

**Why check the body first.** A client sending its body slowly would otherwise hold the turn, and every write behind
it, for as long as it likes.

## Response

`200 OK`, with the store ID in the `X-Tamarackdb-Store` header:

```json
{
  "events": [
    { "sequence": 12348, "time": "2026-09-01T14:25:00.000000Z" }
  ],
  "projections": {
    "create": [ { "version": "5a6b7c8d-9e0f-4a1b-8c2d-3e4f5a6b7c8d" } ],
    "replace": [ { "version": "d4c3b2a1-0f9e-4d8c-b7a6-5f4e3d2c1b0a" } ]
  }
}
```

- `events`: the Sequence Position and `time` of each event, in the order they were sent (see
  [Events](/docs/concepts/events/#time)).
- `projections`: the new version of each created and replaced projection, in the order they were sent.
- Every list is always present, empty if need be.

## Conflicts

A condition that doesn't hold, or a projection that isn't at the version given, gets `409 ConcurrencyException`.
Nothing is written. The `message` names the item by its place in the body:

| Cause | `message` |
|---|---|
| An event matching a condition exists after its `afterSequence`, or at all for a condition without one | `conditions[1] no longer holds` |
| A condition was read on another store ID | `conditions[0] was read on another store` |
| A `replace` or `delete` whose version isn't the stored one, or whose projection no longer exists | `projections.replace[0] no longer has the given version` |
| A `create` whose `type` + `id` already exists | `projections.create[0] already exists` |

What to do next is in [Append Condition](/docs/concepts/append-condition/#the-flow) and
[Projections](/docs/concepts/projections/#versions).

## Waiting for a turn

- Writes go through one at a time, in the order they arrive. A write waits for its turn, with its connection held
  open, behind the requests that arrived before it.
- Each holds the turn only for its own SQLite transaction, usually a few milliseconds.
- When too many requests already wait, a new one gets `503 WriteQueueFull` and never joins the queue. Nothing is
  written.
- The server puts no limit on how long a write waits. A client sets its own: when it no longer wants to wait, it
  closes the connection.

## The client leaving

- A client that disconnects while its write waits leaves the queue, and nothing is written.
- The server checks once more that the client is still there just as the turn comes.
- From then on, the write goes to the end, conditions and all, even if the client leaves. The client then can't tell
  whether the write happened.

## A lost response

If the connection drops before the response arrives, the client can't tell whether the write happened. Reading the
events again doesn't settle it either: finding nothing can mean "not written" or "not written yet".

To retry, a client sends the same write again, with the same Append Conditions, `afterSequence` included. Writes are
served in the order they arrive, so the retry comes after the first attempt.

- If the first attempt didn't go through, the retry is written normally.
- If it did, the retry fails with `409` instead of writing the same events twice, but only if the first attempt left
  something the retry checks:
  - an event that matches one of its conditions (a condition only sees the events that match its
    `failIfEventsMatch`);
  - or a projection change, whose version no longer matches.
- A write with no condition and no projection change, or whose events match none of its conditions, would be written
  twice.

To make any write safe to retry, give it a unique `writeId` in the metadata of each of its events, the same for every
attempt, and add a condition on it with no `afterSequence`:

```json
{
  "failIfEventsMatch": [
    { "metadata": [ { "name": "writeId", "value": "0f8e2d4c-9a1b-4c3d-8e7f-6a5b4c3d2e1f" } ] }
  ]
}
```

The name is up to the client: TamarackDB gives it no meaning. A `409` on the retry is settled by reading the events
with that `writeId`. If they're there, the first attempt went through. If not, another write broke one of the
conditions, and since the first attempt carries the same ones, it can't go through either.
