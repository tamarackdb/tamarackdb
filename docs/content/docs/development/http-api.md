---
title: "HTTP API"
description: "The TamarackDB HTTP API reference: conventions, queries, reading events, transactions, projections, administration endpoints, and errors."
slug: "http-api"
weight: 3
---

The HTTP API reference. The ideas behind it are in [Concepts](/docs/development/concepts/). The examples assume a
server on `127.0.0.1:8085`, with authentication off.

## Endpoints

| Endpoint | Purpose |
|---|---|
| [`QUERY /events`](#reading-events) | Read committed events |
| [`POST /tx`](#transactions) and `/tx/{txId}/...` | Begin a transaction, read and write in it, commit or abandon it |
| [`POST /projections`](#writing-projections) | Create, replace, and delete projections outside a transaction |
| [`GET /projections/{type}/{id}`](#reading-a-projection) | Read one projection |
| [`DELETE /projections/{type}`, `DELETE /projections`](#bulk-delete) | Delete every projection of a type, or every projection |
| [`POST /pause`, `POST /resume`](#pause-and-resume) | Stop transactions from beginning, and let them begin again |
| [`POST /optimize`](#optimize) | Refresh SQLite's query statistics |
| [`DELETE /events`](#delete-events) | Delete every event (development mode only) |
| `GET /health`, `GET /stats` | See [Monitoring](/docs/operations/monitoring/) |

## Conventions

- By default the server listens on a unix socket. With curl: `curl --unix-socket <path> http://localhost/...`.
- With `enableAuth` on, every request carries `Authorization: Bearer <token>`, or gets `401`.
- A request body is JSON, decoded strictly: an unknown key, at any level, gets `400`.
- A body is at most `maxRequestBodySize` bytes, or gets `413`.
- Writes are served one at a time, in the order they arrive: commits, `POST /projections`, bulk deletes, `/pause`,
  `/resume`, and `/optimize`. They can get `503 WriteQueueFull` when too many wait, or `503 ShuttingDown`.
  Nothing is written then.
- A write that joined the queue runs, even if its client leaves. Its client then handles a lost response.

## Queries

A query is `"all"`, `"none"`, or an array of items:

```json
[
  {
    "types": ["user-created", "user-updated"],
    "identifiers": [
      {"name": "userId", "value": "123"}
    ],
    "metadata": [
      {"name": "tenantId", "value": "acme"}
    ]
  },
  {
    "types": ["some-other-event"]
  }
]
```

- Items are combined with OR. Within an item, the keys are combined with AND.
- `types`: the event's type is one of them. `identifiers` and `metadata`: the event carries every one listed.
- A key left out doesn't filter. An item names at least one key.
- An array, when present, is never empty. To match every event, use `"all"`.
- `query` is always required.
- Values are compared exactly, byte for byte.
- A query has at most 100 items, and an item at most 100 values in total.
- There is no negation.

## Reading events

`QUERY /events` reads committed events, a page at a time, outside any transaction. It uses the HTTP `QUERY` method:
like `GET`, with a body.

```sh
curl -X QUERY http://127.0.0.1:8085/events \
  -H "Content-Type: application/json" \
  -d '{
    "query": [
      {
        "identifiers": [
          {"name": "userId", "value": "123"}
        ]
      }
    ],
    "afterSequence": 12345,
    "limit": 500
  }'
```

| Key | Required | What it does |
|---|---|---|
| `query` | yes | A [query](#queries) |
| `afterSequence` | no | Only events after this Sequence Position. Left out, the read starts at the beginning |
| `limit` | no | The most events in the page, from 1 to `maxEventsPerPage`. Left out, `defaultEventsPerPage` |

`200 OK`, with an NDJSON body (`application/x-ndjson`), one JSON value per line:

```
{"sequence":12346,"time":"2026-09-01T14:23:05.123456Z","type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}
{"sequence":12347,"time":"2026-09-01T14:23:07.981234Z","type":"user-updated","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}
{"hasMore":true}
```

- Events come in Sequence Position order. There is no filter on `time`.
- The last line is the trailer, `{"hasMore": true}` or `{"hasMore": false}`. Tell it apart from an event by its
  `hasMore` key.
- For the next page, send the same query with `afterSequence` set to the last event's `sequence`.
- To follow new events, keep polling the same way once `hasMore` is `false`.
- A page with no trailer was cut short. Resume after the last event fully received.
- The server gives each line 30 seconds to go out. Read the page as it arrives.

## Transactions

| Endpoint | Body | Success |
|---|---|---|
| `POST /tx` | none | `200 {"txId": "..."}` |
| `QUERY /tx/{txId}/events` | `{"query": ...}` | `200`, NDJSON |
| `POST /tx/{txId}/events` | `{"events": [...]}` | `200 {"time": "..."}` |
| `GET /tx/{txId}/projections/{type}/{id}` | none | `200`, the payload |
| `POST /tx/{txId}/projections` | `{"create": [...], "replace": [...], "delete": [...]}` | `200 {"time": "..."}` |
| `POST /tx/{txId}/commit` | none | `204` |
| `DELETE /tx/{txId}` | none | `204` |

Any error ends the transaction, except `404 ProjectionNotFound` and `409 TransactionBusy`. Every later call on it gets
`404 TransactionNotFound`.

### Begin

```sh
curl -X POST http://127.0.0.1:8085/tx
```

```json
{"txId":"7d1e4b2a-3c5f-4e6d-9a8b-0c1d2e3f4a5b"}
```

During a pause, `POST /tx` gets `503 Paused`. While `maxOpenTx` transactions are open, it gets
`503 TooManyTransactions`: try again later.

### Read events

```sh
curl -X QUERY http://127.0.0.1:8085/tx/<txId>/events \
  -H "Content-Type: application/json" \
  -d '{
    "query": [
      {
        "types": ["seat-reserved"],
        "identifiers": [
          {"name": "showId", "value": "s1"}
        ]
      }
    ]
  }'
```

```
{"sequence":17,"time":"2026-10-03T21:10:58.402113Z","type":"seat-reserved","identifiers":{"showId":"s1","seat":"A4"},"metadata":{},"payload":"..."}
{"time":"2026-10-03T21:11:05.123456Z","type":"seat-reserved","identifiers":{"showId":"s1","seat":"A5"},"metadata":{},"payload":"..."}
{"end":true}
```

- The body takes `query` only: no `afterSequence`, no `limit`. Every matching event comes back.
- Committed events come first, then the transaction's own pending events, which have no `sequence`.
- The last line is the trailer, `{"end": true}`.
- The read opens a condition. The next call must be a write of events.
- A read cut short leaves the condition open: abandon the transaction and run the command again.

### Write events

```sh
curl -X POST http://127.0.0.1:8085/tx/<txId>/events \
  -H "Content-Type: application/json" \
  -d '{
    "events": [
      {
        "type": "seat-reserved",
        "identifiers": {"showId": "s1", "seat": "A6"},
        "payload": "..."
      }
    ]
  }'
```

```json
{"time":"2026-10-03T21:11:07.554310Z"}
```

- Each event has a `type`, `identifiers`, `metadata`, and a `payload`. `identifiers` and `metadata` can be left out.
- `{"events": []}` is the decision to write nothing.
- The write closes the open condition. Without one, it gets `400`.
- `time` is the time every event of this write will carry. The events get their `sequence` at commit.

### Read a projection

```sh
curl http://127.0.0.1:8085/tx/<txId>/projections/show-seats/s1
```

- `200 OK`: the payload, as written.
- `404 ProjectionNotFound`: it doesn't exist, or the transaction deleted it. The transaction goes on.
- A later read returns the projection as the transaction left it.
- Allowed while a condition is open, for example to add metadata to the events of a decision before writing them.
- A projection only read is not checked at commit.

### Write projections

```sh
curl -X POST http://127.0.0.1:8085/tx/<txId>/projections \
  -H "Content-Type: application/json" \
  -d '{
    "create": [
      {
        "type": "seat-hold",
        "id": "s1-B2",
        "payload": "..."
      }
    ],
    "replace": [
      {
        "type": "show-seats",
        "id": "s1",
        "payload": "..."
      }
    ],
    "delete": [
      {
        "type": "seat-hold",
        "id": "s1-A6"
      }
    ]
  }'
```

- No version is sent: the server knows the one read.
- A `create` needs no read. The server checks at commit that the projection still doesn't exist.
- A `replace` or `delete` needs the projection to be read, or created, in the transaction first.
- Deleting a projection that doesn't exist does nothing.
- One write names a projection once, across the three lists.
- Refused while a condition is open.

### Commit

```sh
curl -X POST http://127.0.0.1:8085/tx/<txId>/commit
```

`204 No Content`: everything is written. The transaction is over, whatever the outcome.

A conflict gets `409 ConcurrencyException`, and nothing is written. Run the whole command again, in a new
transaction. The `message` names the cause:

| Cause | `message` |
|---|---|
| An event matching the third read was committed since | `conditions[2] no longer holds` |
| A projection changed since it was read | `projection show-seats/s1 no longer has the version read` |
| A projection read as missing was created since | `projection seat-hold/s1-A6 was created by another write since it was read` |
| A projection created without a read already exists | `projection seat-hold/s1-B2 already exists` |

**A lost response.** A commit can't be sent again: the transaction no longer exists. Run the command again in a new
transaction. A decision whose read matches its own events finds them, or gets `409`, so it never writes them twice. A
decision that reads `"none"`, or whose query doesn't match its events, could write them twice: give the entity an ID
before the first call, and have the decision read it.

### Abandon

```sh
curl -X DELETE http://127.0.0.1:8085/tx/<txId>
```

`204 No Content`, even for a transaction already over. Nothing it held is written. Call it in the error handler
around a command, and ignore the response.

### Limits

| Setting | What it counts |
|---|---|
| `maxEventsPerTx` | The events the transaction writes |
| `maxReadsPerTx` | Its reads of events |
| `maxProjectionsPerTx` | The distinct projections it writes |

The call that would go over gets `400`, with the setting named in the `message`. An event over `maxEventSize`, or a
projection over `maxProjectionSize`, gets `413`.

## Projections

Outside a transaction: for a projector that catches up on its own, or a rebuild. A command writes its projections in
its transaction instead.

### Writing projections

```sh
curl -X POST http://127.0.0.1:8085/projections \
  -H "Content-Type: application/json" \
  -d '{
    "create": [
      {
        "type": "daily-sales",
        "id": "2026-10-03",
        "payload": "..."
      }
    ],
    "replace": [
      {
        "type": "projector-position",
        "id": "daily-sales",
        "version": "1b2c3d4e-5f60-4718-9a0b-c1d2e3f4a5b6",
        "payload": "..."
      }
    ],
    "delete": [
      {
        "type": "daily-sales",
        "id": "2026-10-02",
        "version": "9f3c2a1e-7b4d-4c8e-a5f6-0d1e2f3a4b5c"
      }
    ]
  }'
```

```json
{
  "create":  [ { "version": "5a6b7c8d-9e0f-4a1b-8c2d-3e4f5a6b7c8d" } ],
  "replace": [ { "version": "d4c3b2a1-0f9e-4d8c-b7a6-5f4e3d2c1b0a" } ]
}
```

- `create`: the projection must not exist. `replace` and `delete`: it must still be at `version`.
- Everything is written, or nothing is.
- The response gives the new version of each created and replaced projection, in the order sent.
- At most `maxProjectionsPerWrite` projections per write. A larger rebuild writes in several calls.
- A conflict gets `409 ConcurrencyException`, and the `message` names the item: `create[0] already exists`, or
  `replace[0] no longer has the given version`. Read the projection again and decide again.
- A write whose response is lost can be sent again as is: it's never applied twice. A `409` on the retry means the
  first attempt went through, or another write came first: read again.

### Reading a projection

```sh
curl -i http://127.0.0.1:8085/projections/user-profile/123
```

```
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8
X-Tamarackdb-Version: 9f3c2a1e-7b4d-4c8e-a5f6-0d1e2f3a4b5c

{"name":"Ada Lovelace"}
```

- The body is the payload as written. The version is in `X-Tamarackdb-Version`.
- `404 ProjectionNotFound` when it doesn't exist.
- `type` and `id` are percent-encoded in the path: an `id` of `a/b` is `a%2Fb`.

### Bulk delete

```sh
curl -X DELETE http://127.0.0.1:8085/projections/user-profile
curl -X DELETE http://127.0.0.1:8085/projections
```

- Deletes every projection of one type, or every projection. `204 No Content`.
- No version, no conflict. A later `replace` or `delete` of a deleted projection gets `409`.

## Pause and resume

A pause stops transactions from beginning. No event is appended until `POST /resume`. Reads, projections, and
`/optimize` go on. One process of the application drives the pause: the first `/resume` ends it for everyone.

```sh
curl -X POST http://127.0.0.1:8085/pause
```

- While transactions are still open: `202 Accepted`, `{"openTransactions":3}`. The pause is requested: `POST /tx`
  gets `503 Paused`, and open transactions go on to their commit.
- Once none is open: `200 OK`, `{"lastSequence":5042}`. No event comes after that position until `/resume`.
- Call `/pause` again, after a short delay, until it answers `200`:

  ```sh
  pause() { curl -s -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:8085/pause; }
  until [ "$(pause)" = 200 ]; do sleep 1; done
  ```

```sh
curl -X POST http://127.0.0.1:8085/resume
```

- `204 No Content`. It ends the pause, or withdraws a requested one.
- A pause in place survives a restart. A requested pause doesn't.

## Optimize

```sh
curl -X POST http://127.0.0.1:8085/optimize
```

Refreshes the statistics SQLite plans queries with. `204 No Content`. Call it once a day (see
[Maintenance](/docs/operations/maintenance/#query-statistics)).

## Delete events

Deletes every event, for tests. It exists only in development mode. To empty the store between two tests, delete the
events, then the projections:

```sh
curl -X DELETE http://127.0.0.1:8085/events
curl -X DELETE http://127.0.0.1:8085/projections
```

- `204 No Content`.
- Projections stay. Delete them with [`DELETE /projections`](#bulk-delete).
- Sequence Positions go on: the next event doesn't get sequence 1.
- It doesn't wait for open transactions, and runs during a pause too. An open transaction goes on, and its commit
  writes into the emptied log.
- With development mode off, it gets `405`.

## Errors

Errors share one JSON envelope:

```json
{ "error": "InvalidRequest", "message": "afterSequence must be a non-negative integer" }
```

`error` is a stable code. `message` is a detail for humans, and names the item at fault (`events[3]`) or the setting
behind a limit.

| Status | `error` | When |
|---|---|---|
| `400` | `InvalidRequest` | A malformed body, an invalid query or event, a broken transaction rule, or a limit |
| `401` | `Unauthorized` | A missing or wrong Bearer token |
| `404` | `ProjectionNotFound` | A projection that doesn't exist |
| `404` | `TransactionNotFound` | A transaction unknown, expired, or over |
| `409` | `ConcurrencyException` | A commit or `POST /projections` conflict. Nothing was written |
| `409` | `TransactionBusy` | A call while another one runs on the same transaction. The transaction goes on |
| `413` | `PayloadTooLarge` | An event, a projection, or a body over its size limit |
| `500` | `InternalError` | A failure on the server |
| `503` | `Paused` | `POST /tx` during a pause |
| `503` | `TooManyTransactions` | `POST /tx` while `maxOpenTx` transactions are open |
| `503` | `WriteQueueFull` | A write while too many already wait. Nothing was written |
| `503` | `ShuttingDown` | A write while the server shuts down. Nothing was written |
| `503` | `Unavailable` | `GET /health` when the database can't be reached |
