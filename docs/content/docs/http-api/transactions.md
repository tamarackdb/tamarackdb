---
title: "Transactions"
description: "The /tx endpoints: begin a transaction, read and write events, read and write projections, commit or abandon it, with each body, response, and error."
slug: "transactions"
weight: 4
---

The endpoints of a transaction. What a transaction is, and the rules it follows, are in
[Concepts: Transactions](/docs/concepts/transactions/).

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## Endpoints

| Endpoint | Body | Success |
|---|---|---|
| [`POST /tx`](#begin) | none | `200 {"txId": "..."}` |
| [`QUERY /tx/{txId}/events`](#reading-events) | `{"query": ...}` | `200`, NDJSON |
| [`POST /tx/{txId}/events`](#writing-events) | `{"events": [...]}` | `200 {"time": "..."}` |
| [`GET /tx/{txId}/projections/{type}/{id}`](#reading-a-projection) | none | `200`, the payload |
| [`POST /tx/{txId}/projections`](#writing-projections) | `{"upsert": [...], "delete": [...]}` | `200 {"time": "..."}` |
| [`POST /tx/{txId}/commit`](#commit) | none | `204` |
| [`DELETE /tx/{txId}`](#abandon) | none | `204` |

- Bodies are decoded strictly (see [Conventions](/docs/http-api/conventions/#request-bodies)).
- No response of these endpoints carries the `X-Tamarackdb-Store` header: the server keeps the store ID with the
  transaction.
- Any error on a transaction ends it, whatever its status, including a `400` for a malformed body. Every later call on
  it gets `404 TransactionNotFound`. `404 ProjectionNotFound` isn't an error in that sense: it's an ordinary answer.

## Begin

```sh
curl -X POST http://127.0.0.1:8085/tx
```

```json
{"txId":"7d1e4b2a-3c5f-4e6d-9a8b-0c1d2e3f4a5b"}
```

The `txId` is a UUID. Every other endpoint of the transaction takes it in its path.

## Reading events

```sh
curl -X QUERY http://127.0.0.1:8085/tx/7d1e4b2a-3c5f-4e6d-9a8b-0c1d2e3f4a5b/events \
  -H "Content-Type: application/json" \
  -d '{ "query": [ { "types": ["seat-reserved"], "identifiers": [ { "name": "showId", "value": "s1" } ] } ] }'
```

- `query` is required, in the [query grammar](/docs/http-api/query-grammar/). A decision that rests on no event reads
  `"none"`. The body takes no other key: there is no `afterSequence` and no `limit`.
- The read opens a condition. The next call on the transaction MUST be [a write of events](#writing-events).
- A read past `maxReadsPerTx` gets `400` (see [Limits](#limits)).

`200 OK`, with an [NDJSON](https://github.com/ndjson/ndjson-spec) body (`Content-Type: application/x-ndjson`):

```
{"sequence":17,"time":"2026-10-03T21:10:58.402113Z","type":"seat-reserved","identifiers":{"showId":"s1","seat":"A4"},"metadata":{},"payload":"{}"}
{"time":"2026-10-03T21:11:05.123456Z","type":"seat-reserved","identifiers":{"showId":"s1","seat":"A5"},"metadata":{},"payload":"{}"}
{"end":true}
```

- First come the committed events that match, in ascending Sequence Position order, in the same shape as
  [`QUERY /events`](/docs/http-api/read-events/#response).
- Then come the transaction's own pending events that match, in the order they were written. A pending event has its
  `time`, and no `sequence`.
- The last line is always the trailer, `{"end": true}`. A client MUST tell it apart from an event by its shape.
- Every event that matches comes back, with no page limit.
- The server gives each line 30 seconds to go out, as for `QUERY /events`.

A read cut short, with no trailer, leaves its condition open: the client can neither read again nor know what it
missed. It MUST abandon the transaction and run the command again.

## Writing events

```sh
curl -X POST http://127.0.0.1:8085/tx/7d1e4b2a-3c5f-4e6d-9a8b-0c1d2e3f4a5b/events \
  -H "Content-Type: application/json" \
  -d '{ "events": [ { "type": "seat-reserved", "identifiers": { "showId": "s1", "seat": "A6" }, "payload": "{}" } ] }'
```

```json
{"time":"2026-10-03T21:11:07.554310Z"}
```

- `events` is required. Each event has a `type`, `identifiers`, `metadata`, and a `payload` (see
  [Events](/docs/concepts/events/#fields)).
- `{"events": []}` is the decision to write nothing. It closes the condition like any other write.
- The write closes the open condition. Without one, it's refused.
- `time` is the time every event of this write carries, read from the server's clock when the write arrived. The
  events get their `sequence` at commit.
- An event over `maxEventSize` gets `413`.
- A write that would take the transaction over `maxEventsPerTx` events gets `400` (see [Limits](#limits)).

## Reading a projection

```sh
curl -i http://127.0.0.1:8085/tx/7d1e4b2a-3c5f-4e6d-9a8b-0c1d2e3f4a5b/projections/show-seats/s1
```

- `200 OK`: the payload, exactly as written, in `text/plain; charset=utf-8`, with no version header.
- `404 ProjectionNotFound`: the projection doesn't exist, or the transaction deleted it.
- The first read of a projection in a transaction reads the store. Every later one returns the projection as the
  transaction left it.
- A read is refused while a condition is open.

## Writing projections

```sh
curl -X POST http://127.0.0.1:8085/tx/7d1e4b2a-3c5f-4e6d-9a8b-0c1d2e3f4a5b/projections \
  -H "Content-Type: application/json" \
  -d '{
    "upsert": [ { "type": "show-seats", "id": "s1", "payload": "{\"free\":37}" } ],
    "delete": [ { "type": "seat-hold", "id": "s1-A6" } ]
  }'
```

```json
{"time":"2026-10-03T21:11:07.601877Z"}
```

| Key | What it holds |
|---|---|
| `upsert` | `type`, `id`, `payload`: the projection's whole new payload |
| `delete` | `type`, `id` |

- At least one of the two lists MUST be non-empty. A key left out is an empty list.
- Every projection MUST have been read in the transaction first. No version is sent: the server knows the one read.
- A `payload` is a string, and an empty string is valid. A missing or `null` payload gets `400`.
- One write names a projection at most once, across the two lists.
- Deleting a projection that doesn't exist does nothing.
- A projection over `maxProjectionSize` gets `413`.
- A write that would take the transaction over `maxProjectionsPerTx` projections gets `400` (see [Limits](#limits)).
- A write is refused while a condition is open.
- `time` is the time of the write, by the server's clock: the response has the same shape as a write of events.

## Commit

```sh
curl -X POST http://127.0.0.1:8085/tx/7d1e4b2a-3c5f-4e6d-9a8b-0c1d2e3f4a5b/commit
```

`204 No Content`, with no body: everything the transaction held is written. The `time` of each event is already known
from its write, and nothing else is returned.

- A commit is refused while a condition is open.
- It waits for its turn behind the requests that arrived before it, and may get `503 WriteQueueFull` or
  `503 ShuttingDown` (see [Waiting for a turn](/docs/http-api/conventions/#waiting-for-a-turn)).
- A transaction with nothing to write gets `204` at once.
- The transaction is over after its commit, whatever the outcome. A client that disconnects while its commit waits
  leaves the queue, and nothing is written. Once its turn comes, the commit goes to the end, even if the client
  leaves (see [The client leaving](/docs/http-api/conventions/#the-client-leaving)).

A conflict gets `409 ConcurrencyException`, and nothing is written. The `message` names the cause:

| Cause | `message` |
|---|---|
| An event that matches the query of the transaction's third read was committed after that read | `conditions[2] no longer holds` |
| A projection no longer has the version read | `projection show-seats/s1 no longer has the version read` |
| A projection read as missing was created since | `projection seat-hold/s1-A6 was created by another write since it was read` |
| The store was reset since the transaction began | `the transaction was begun on another store` |

After a `409`, the client runs the whole command again, in a new transaction.

### A lost response

If the commit's response is lost, the client can't tell whether the write happened. It can't send the commit again:
the transaction no longer exists, and a second commit gets `404 TransactionNotFound`.

The application runs the whole command again, in a new transaction:

- If the first commit didn't go through, the new one writes normally.
- If it did, or is still waiting or running, the new commit comes after it: commits are served in the order they
  arrive. The new transaction's read either finds the first commit's events, or its condition fails with `409` and
  the command runs once more. Either way, the decision ends up made on those events, and doesn't write them twice.

This holds only for a decision whose read matches the events it writes. A decision that reads `"none"`, or whose
query doesn't match its own events, has nothing to check them against: run again, it writes its events a second time.
Such a command MUST find out by other means, for example by reading the events that carry an identifier it gave them.

**Why not read back what the commit wrote.** Finding nothing can mean "not written" or "not written yet": the first
commit may still be waiting or running, and a read never waits for it.

## Abandon

```sh
curl -X DELETE http://127.0.0.1:8085/tx/7d1e4b2a-3c5f-4e6d-9a8b-0c1d2e3f4a5b
```

`204 No Content`, always, even for a transaction that already ended. Nothing it held is written. It's meant as a
precaution in the error handler around a command, often after an error already ended the transaction.

## Limits

Three settings bound a transaction, across all its calls (see
[Configuration](/docs/operations/configuration/#settings)):

| Setting | What it counts |
|---|---|
| `maxEventsPerTx` | The events the transaction writes, across all its writes of events |
| `maxReadsPerTx` | Its reads of events, those followed by an empty write included |
| `maxProjectionsPerTx` | The distinct projections it writes, across all its writes of projections, `upsert` and `delete` together. A projection written twice counts once |

- Each call is checked before it does anything. The call that would go over gets `400 InvalidRequest`, and the
  `message` names the setting, for example `the transaction would read events 101 times, more than maxReadsPerTx
  (100)`.
- Like any error, the refusal ends the transaction.
- The read of events that would go over runs no query.

**Why.** `maxRequestBodySize` bounds one request, not a transaction, which is built over many requests. A commit holds
its turn while it checks the condition of every read and writes every event and projection: without these limits,
every request behind it would wait for as long as the transaction likes. A read followed by an empty write counts,
since its condition is checked at commit like the others. Checking each call tells the client before it has done all
its work.

## Errors

| Status | `error` | When |
|---|---|---|
| `400` | `InvalidRequest` | A malformed body, a call that breaks a rule of transactions, or a call over a [limit](#limits). The `message` names the rule or the setting, for example `events written without a read` |
| `404` | `TransactionNotFound` | The transaction is unknown, expired, or already over |
| `404` | `ProjectionNotFound` | [Reading a projection](#reading-a-projection) that doesn't exist |
| `409` | `ConcurrencyException` | At commit, a [conflict](#commit). On any call, a store reset since the transaction began |
| `413` | `PayloadTooLarge` | An event or a projection over its size limit, or a body over `maxRequestBodySize` |

Every error but `404 ProjectionNotFound` ends the transaction. The full list of codes is in
[Errors](/docs/http-api/errors/).
