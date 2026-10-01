---
title: "Integration"
slug: "integration"
weight: 2
---

This is for anyone writing an application or client library that talks to a
running TamarackDB instance over HTTP. For how to run that instance, see
[Deployment](/docs/guides/deployment/). For how the server works inside, see [Architecture](/docs/architecture/).

Examples below assume a server running locally on `127.0.0.1:8085`, with
authentication off. TamarackDB listens on a unix socket by default; the
examples apply the same way once you point curl at it with
`--unix-socket <path> http://localhost/...` instead of a host and port (see
[Deployment](/docs/guides/deployment/#configure)). The socket's permissions
decide who may connect: your application's user must be allowed by the
server's `socketMode`, which by default lets in the server's own user only. Add `-H "Authorization: Bearer <token>"` to
every request when `enableAuth` is on (see [Deployment](/docs/guides/deployment/#configure)).

## Client libraries

- PHP: [tamarackdb-php](https://github.com/tamarackdb/tamarackdb-php)

## Terms

These pieces are code in your application. TamarackDB doesn't run them: it
stores what they read and write.

- **Decision model**: reads the events a command needs, decides, and appends
  new events.
- **Event handler**: code that reacts to the events a command just appended.
  It runs inside the same transaction, before the commit. There are two kinds:
  - **Projector**: computes projections from events and writes them.
  - **Processor**: reads events and may append more events in response.
- **Projection**: the current state a projector computes from events,
  identified by `type` + `id` (see [Projections](#projections)). The `type` is
  like a class, and each projection is one instance of it. Every projection
  can be rebuilt from events.

## Transactions

TamarackDB is built for applications that handle a command in one go, inside
one request of the application:

1. Open a transaction.
2. Let your decision models read, decide, and append new events.
3. Let your event handlers react.
4. Write every changed projection.
5. Commit.

Everything lands together, or nothing does. Every call in steps 2 to 5 runs
inside one SQLite transaction on the server, so each call sees what earlier
calls of the same transaction wrote, even though nothing is committed yet.

Only one transaction exists at a time. It holds the store's write lock from
the moment it opens until it ends. Other clients wait their turn. Keep a
transaction inside one request of your application, and keep it short: open
it when the command starts, and end it before you send anything back to the
end user.

### Opening a transaction

```sh
curl -X POST http://127.0.0.1:8085/begin
```

```json
{ "ticket": "a045ad63-5d4b-4847-8eb9-fbddb4e2d65b" }
```

Every call that belongs to the transaction carries this ticket in the
`X-Tamarackdb-Ticket` header.

Log the ticket along with the command it belongs to. If the transaction
expires (see [Deadline](#deadline)), the server logs a warning with that
ticket, and your own log tells you which command it was.

If another transaction is active, the request waits, with its connection held
open, until its turn comes. Requests are served in the order they arrive. A
waiting request can fail with:

- `503 TransactionQueueFull`: too many requests are already waiting. The
  request never joined the queue.

The server puts no limit on how long a request waits. Your client sets its
own: when it no longer wants to wait, it closes the connection, and the
request leaves the queue. Pick that limit from how long your end user can
wait. A request never loses its place in the queue unless its client gives up.

### Deadline

The server rolls a transaction back when either limit is reached:

- **Idle timeout** (5 seconds out of the box): time without any call made
  with the ticket. Each call renews it when it ends.
- **Total ceiling** (15 seconds out of the box): time since the ticket was
  given out, however many calls you make.

Both are set by the operator. A client can't ask for more. They are there to
recover from a client that crashed or hangs, not to time a normal command: a
normal command ends with its own commit or rollback well before either limit.
See [Architecture](/docs/architecture/#deadline-and-ceiling) for the details.

### Ending a transaction

Commit with `POST /commit`:

```sh
curl -X POST http://127.0.0.1:8085/commit \
  -H "X-Tamarackdb-Ticket: a045ad63-5d4b-4847-8eb9-fbddb4e2d65b"
```

Roll back with `POST /rollback` as soon as the command fails, for example when
one of your event handlers throws. The server would roll the transaction back
on its own once the idle timeout is reached (see [Deadline](#deadline)), but
until then it keeps the write lock, and every other client waits. Rolling back
yourself frees it right away:

```sh
curl -X POST http://127.0.0.1:8085/rollback \
  -H "X-Tamarackdb-Ticket: a045ad63-5d4b-4847-8eb9-fbddb4e2d65b"
```

Both respond `204 No Content`. A commit never fails with `409`: every Append
Condition was already checked when its events were appended.

A transaction also ends, rolled back, when:

- any call made with its ticket returns an error, except `404
  ProjectionNotFound` (see [Projections](#projections));
- the client closes the connection while a call with its ticket is running,
  and the call fails because of it (a call that already finished its work
  still succeeds, and a `POST /commit` may still commit);
- the idle timeout or the total ceiling is reached (see [Deadline](#deadline)).

Once a transaction has ended, its ticket is no longer active: any call with it
gets `410 TicketNotActive`. After an error, don't try to continue: open a new
transaction and run the whole command again.

### A lost commit response

If the connection drops before the `POST /commit` response arrives, you can't
tell whether the commit happened. This is rare. Most applications can leave it
to the user: reloading the page shows whether the change was applied.

To retry a command safely instead, append its events with an Append Condition
(see [Append Condition](#append-condition)). The retry is not a second
`POST /commit`: the ticket is no longer active either way, so that call gets
`410 TicketNotActive` whether the commit happened or not. Run the command again
in a new transaction, and append with the same condition as the first attempt,
including its `afterSequence`. If the first commit went through, its events are
now past that position, and the retry fails with `409 ConcurrencyException`
instead of appending the same events twice.

## Reading events

Read events with the HTTP `QUERY` method:

```sh
curl -X QUERY http://127.0.0.1:8085/events \
  -H "Content-Type: application/json" \
  -H "X-Tamarackdb-Ticket: a045ad63-5d4b-4847-8eb9-fbddb4e2d65b" \
  -d '{
    "query": [
      {
        "identifiers": [
          { "name": "userId", "value": "123" }
        ]
      }
    ]
  }'
```

The ticket is optional:

- **With a ticket**, the read runs inside the transaction. It sees every
  committed event, plus the events appended earlier in the same transaction.
  Use this to make a decision, or in an event handler that needs the events
  the command just appended.
- **Without a ticket**, the read sees committed events only. It never waits
  for the active transaction. Use this to display data, for a projection
  rebuild, or for the optimistic flow (see [Append Condition](#append-condition)).
  The response carries the store ID (see [Store ID](#store-id)).

Use the literal string `"*"` in place of `query` to read every event:

```sh
curl -X QUERY http://127.0.0.1:8085/events \
  -H "Content-Type: application/json" \
  -d '{ "query": "*" }'
```

The response is [NDJSON](https://github.com/ndjson/ndjson-spec)
(`Content-Type: application/x-ndjson`): one JSON value per line, streamed as each
matching event is found. Every line but the last is one event, oldest first; the
last line is always a trailer with `hasMore`:

```
{"sequence":12346,"time":"2026-09-01T14:23:05.123456Z","type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}
{"hasMore":false}
```

`time` is when TamarackDB appended the event, in UTC, with exactly 6
fractional digits. Convert it to local time in your application if you need
to display it.

Parse the response line by line, not as one JSON document. That way, a response can safely
resume if the connection drops mid-transfer (see Pagination below). Tell the
trailer apart from an event line by shape, not position: it's the one with a
`hasMore` key. If the response ends without one, the page was cut short; treat
that exactly like a dropped connection and resume with `afterSequence` set to
the last event line you got.

### Query grammar

`query` is a list of query items, combined with **OR**. Within one item:

- **OR** across `types` (matches if the event's type is any of the listed ones;
  leave it out to match every type)
- **AND** across `identifiers` (the event must carry all of the listed ones)
- **AND** across `metadata` (the event must carry all of the listed ones)
- The three are combined with **AND**

```json
{
  "query": [
    {
      "types": ["user-created", "user-updated"],
      "identifiers": [
        { "name": "userId", "value": "123" }
      ]
    },
    { "types": ["some-other-event"] }
  ]
}
```

A query item must specify at least one of `types`, `identifiers`, or `metadata`:
there is no empty item (`{}`). To match every event, use `"*"` for the whole
query instead of an empty item. Every array in this grammar must be non-empty
when present: send `"*"`, or leave the key out, rather than `[]`. There is no
way to say "not X": a query only ever describes a set of matching events,
never an exclusion.

A query carries at most 100 query items, and an item at most 100 values across
its `types`, `identifiers`, and `metadata` combined. A larger query gets `400`.
The same limits apply to `failIfEventsMatch`.

One more, optional, top-level key narrows a query further: `afterSequence`,
only events with a Sequence Position strictly greater than this value.

```json
{ "query": "*", "afterSequence": 12345 }
```

There is no filter on `time`. To find events by period, tag them when you
append them (for example a `month` metadata entry) and query that tag.

The body is checked strictly: an unknown key gets `400`, so a misspelled
`afterSequence` never reads more than you asked for.

### Pagination

Events always come back oldest first. Cap a response with `limit`:

```json
{ "query": "*", "afterSequence": 12345, "limit": 500 }
```

`hasMore: true` in the trailer means more events matched than were returned. To
fetch the next page, repeat the same `query` with `afterSequence` set to the
Sequence Position of the last event you got. Do this on every call, not just the
first one: it also lets you resume a response that was cut off mid-transfer, from
the last full line you received, with no events skipped or repeated.

Read each page as it arrives. Without a ticket, the server gives each line of a
page 30 seconds to go out: a client that stops reading for longer gets its
connection closed, and sees a page with no trailer. Resume it the same way,
from the last full line.

The same loop that pages through history can also follow new events live: keep
polling without a ticket, with `afterSequence` set to the last Sequence
Position you saw. Once `hasMore` reads `false`, you have caught up, and further
polling picks up new events as they are committed. `tamarackdb-backup` (see
[Backup](/docs/guides/backup/)) is a real example of this loop: it pages through
`/events` with `afterSequence` set to the last sequence it saved locally, and
stops once `hasMore` reads `false`.

`limit` defaults to whatever the server operator set (`defaultEventsPerPage`,
1000 out of the box) and is capped at `maxEventsPerPage` (10000 out of the
box). Asking for more than that gets you `400 Bad Request`.

### Store ID

A read without a ticket returns the store ID in the `X-Tamarackdb-Store`
header: a UUID that names the history you read. `QUERY /events` sends it on
every page, empty ones included, and `GET /projections/{type}/{id}` sends it on
both `200` and `404`.

The store ID only changes when the store is emptied with `POST /reset` (dev
mode only). After that, Sequence Positions start over at 1, and a position you
kept from before names a different event. Keep the store ID next to any
Sequence Position you keep: if a later read returns a different store ID,
start over from the beginning.

## Appending events

`POST /events` appends events inside a transaction. The ticket is required.

```sh
curl -X POST http://127.0.0.1:8085/events \
  -H "Content-Type: application/json" \
  -H "X-Tamarackdb-Ticket: a045ad63-5d4b-4847-8eb9-fbddb4e2d65b" \
  -d '{
    "events": [
      {
        "type": "user-created",
        "identifiers": { "userId": "123" },
        "metadata": { "tenantId": "acme" },
        "payload": "{\"name\":\"Ada\"}"
      }
    ]
  }'
```

`identifiers` and `metadata` are objects whose values are a string, or an array of
strings: an array gives one tag per value, all on the same event. An event
carries at most 20 identifiers and 20 metadata values, and never the same
`{name, value}` pair twice. `payload` is an opaque string. TamarackDB never
parses it, so its format (JSON, XML, or anything else) is entirely up to the
calling application.

On success (`200 OK`), the response gives the Sequence Position and `time`
assigned to each event, in the order you sent them:

```json
{
  "events": [
    { "sequence": 12348, "time": "2026-09-01T14:25:00.000000Z" }
  ]
}
```

These values are final as soon as the call returns, even before the commit:
the transaction either commits them as they are, or rolls them back entirely.
Your event handlers can use them right away. Every event of one call shares the
same `time`; order within a call comes from `sequence`.

A single call carries at most 100 events, each up to 64 KiB (the combined size
of its `type`, `identifiers`, `metadata`, and `payload`). A transaction may make
several `POST /events` calls: the limit applies to each call. An empty `events`
array appends nothing, skips the condition, and returns `{ "events": [] }`. A
missing `events` field gets `400`: it's most likely a misspelled key. Put larger
content (files, images) in external storage, and reference it from the event
instead of embedding it.

### Append Condition

Pass a `condition` to make the append fail if something relevant happened since
you last read:

```sh
curl -X POST http://127.0.0.1:8085/events \
  -H "Content-Type: application/json" \
  -H "X-Tamarackdb-Ticket: a045ad63-5d4b-4847-8eb9-fbddb4e2d65b" \
  -d '{
    "events": [
      {
        "type": "user-renamed",
        "identifiers": { "userId": "123" },
        "payload": "..."
      }
    ],
    "condition": {
      "failIfEventsMatch": [
        {
          "identifiers": [
            { "name": "userId", "value": "123" }
          ]
        }
      ],
      "afterSequence": 12345
    }
  }'
```

`afterSequence` is the Sequence Position you last read up to (see Pagination
above). `failIfEventsMatch` is a query, using the same grammar as a read (see
above). The append fails if any event matching it exists after `afterSequence`.
Both are optional and independent: an event with nothing to protect can be
appended with no `condition` at all.

A failed condition gets `409 Conflict`, and rolls the transaction back:

```json
{ "error": "ConcurrencyException" }
```

There are two ways to use it.

**Inside a transaction.** Read with the ticket, decide, append with the ticket.
The transaction holds the write lock the whole time, so no other client can
append in between. The condition can only fail if your own application
appended a matching event in the same transaction after the read the decision
was based on, for example when two models both read before either appends. That
means the decision was made on stale data: read again before deciding. Each
model can send its own `POST /events` with its own condition.

**Optimistic.** Read without a ticket and decide, then open a transaction,
append with `afterSequence` set to the last Sequence Position you read and the
same query as `failIfEventsMatch`, run your event handlers, and commit. Only
the command's own read and decision happen outside the write lock; event
handlers still run inside the transaction and read with the ticket. If another
client appended a matching event in between, you get `409
ConcurrencyException`: read again, decide again, and retry in a new
transaction.

## Projections

A projection is an opaque payload identified by `type` + `id`, with no history
(see [Terms](#terms)). It can be overwritten or deleted; the store only holds
its current state. Since every projection can be rebuilt from events (see
[Projection rebuilds](#projection-rebuilds)), backups leave projections out. Projections are written in the same transaction as events, so
a commit makes both durable together, and a rollback discards both.

Storing projections in TamarackDB is optional. An application that keeps its projections
elsewhere never has to touch it.

### Reading a projection

```sh
curl -i http://127.0.0.1:8085/projections/user-profile/123
```

```
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8
X-Tamarackdb-Version: 9f3c2a1e-7b4d-4c8e-a5f6-0d1e2f3a4b5c
X-Tamarackdb-Store: 5b0c7e2a-1f4d-4a9b-8c3e-6d2f1a0b9e47

{"name":"Ada Lovelace"}
```

The response body is the payload exactly as written, not wrapped in a JSON
envelope: its own format (JSON, XML, plain text) is up to the writing
application. The `X-Tamarackdb-Version` header carries the projection's
version: keep it to replace or delete the projection later (see
[Versions](#versions)).

The ticket is optional, as for events:

- **With a ticket**, the read sees projections written earlier in the same
  transaction. A projector uses it to read a projection before changing it.
- **Without a ticket**, the read sees committed projections only. This is how you
  read a projection to display a page. The response carries the store ID (see
  [Store ID](#store-id)).

A projection that doesn't exist gets `404 ProjectionNotFound`. Inside a
transaction, this is an ordinary answer, not an error: the transaction goes on.
A projector that gets a `404` usually creates the projection.

A projection is always read by `type` and `id`. There is no query over projections.

`type` and `id` go in the URL as path segments, so percent-encode them: an
`id` of `a/b` is `/projections/user-profile/a%2Fb`, and a space is `%20`. The
same goes for `DELETE /projections/{type}`.

### Writing projections

`POST /projections` creates, replaces, and deletes several projections at
once. Send it with the ticket. Without a ticket, the call commits on its own:
that's only meant for projection rebuilds (see
[Projection rebuilds](#projection-rebuilds)).

```sh
curl -X POST http://127.0.0.1:8085/projections \
  -H "Content-Type: application/json" \
  -H "X-Tamarackdb-Ticket: a045ad63-5d4b-4847-8eb9-fbddb4e2d65b" \
  -d '{
    "create": [
      { "type": "user-list-entry", "id": "789", "payload": "{\"name\":\"Grace\"}" }
    ],
    "replace": [
      {
        "type": "user-profile",
        "id": "123",
        "version": "9f3c2a1e-7b4d-4c8e-a5f6-0d1e2f3a4b5c",
        "payload": "{\"name\":\"Ada Lovelace\"}"
      }
    ],
    "delete": [
      { "type": "user-list-entry", "id": "456", "version": "1b2c3d4e-5f60-4718-9a0b-c1d2e3f4a5b6" }
    ]
  }'
```

- `create` takes `type`, `id`, and `payload`. The projection must not exist
  yet.
- `replace` takes `type`, `id`, `version`, and `payload`. The whole payload is
  replaced; there is no partial update.
- `delete` takes `type`, `id`, and `version`.
- A payload is a string; an empty string is a valid payload. A missing or
  `null` payload gets `400`.
- Each key is optional, and an empty list is fine: your application can send
  its usual call even when its event handlers changed nothing. A body with
  none of the three keys gets `400`.
- Unknown keys get `400`, so a misspelled key can't silently drop writes.
- The same `type` + `id` can't appear twice in one call, across all three
  lists.
- A call carries at most `maxProjectionsPerRequest` projections in total (100
  out of the box), each at most `maxProjectionSize` bytes (64 KiB out of the
  box), counting its `type`, `id`, and `payload` together.

On success (`200 OK`), the response gives the new version of each created and
replaced projection, in the order you sent them:

```json
{
  "create": [ { "version": "5a6b7c8d-9e0f-4a1b-8c2d-3e4f5a6b7c8d" } ],
  "replace": [ { "version": "d4c3b2a1-0f9e-4d8c-b7a6-5f4e3d2c1b0a" } ]
}
```

### Versions

Every projection has a version, a random UUID that changes on every write.
A `replace` or a `delete` carries the version you read. If the stored
projection has a different version, or no longer exists, the call gets `409
ConcurrencyException`, and the transaction rolls back. A `create` gets the
same `409` if the projection already exists. The `message` names the entry,
for example `replace[0]`.

In the usual flow, this never happens: your projector reads the projection
with the ticket, inside the transaction, and the transaction holds the write
lock until the commit. A `409` means the version came from somewhere else: a
read without a ticket, a cache, or a copy kept from an earlier request. Read
the projection again with the ticket, or run the whole command again.

The version is opaque. Compare it only for equality, and never compute it: a
stale copy never matches again, even after the projection is deleted and
created anew. To write the same projection again later, in the same
transaction or in a later call of a rebuild, use the version from the last
response.

The recommended use is one `POST /projections` call per transaction, right
before `POST /commit`, carrying every projection your event handlers changed.
Collect the changes in memory while the handlers run, instead of sending each
small change as it happens. Several calls in one transaction still work.

### What a projection may depend on

Events are appended before your event handlers run, and `POST /events` returns
each event's `sequence` and `time`. A projection can use anything in an event,
those two values included. A rebuild reads the same events back from `QUERY
/events`, with the same values, so it produces the same projections.

## Projection rebuilds

A projection rebuild runs outside any transaction. Your application must be
fully down while it runs: no reads, no writes. The server doesn't check this;
keeping other requests away is up to you.

How you organize the rebuild is up to you: one thread replaying every event in
order, several projectors in parallel, or anything else. These calls are what
it's built from:

- Delete the projections to rebuild, one type at a time, or all of them:

  ```sh
  curl -X DELETE http://127.0.0.1:8085/projections/user-profile
  curl -X DELETE http://127.0.0.1:8085/projections
  ```

- Page through `QUERY /events` without a ticket to read the events to replay.

- Write the rebuilt projections with `POST /projections` without a ticket, in
  as many calls as you need. Each call commits on its own. A projection is a
  `create` the first time, then a `replace` with the version the previous call
  returned.

- Read a projection back with `GET /projections/{type}/{id}` without a ticket:
  the response carries its payload and its version.

For example, a single thread can rebuild everything this way:

1. `DELETE /projections`.
2. Page through `QUERY /events`, and apply each event to projections kept in
   memory.
3. Send them all with `POST /projections`, as `create`, in chunks that fit
   the request limits.

`DELETE /projections/{type}`, `DELETE /projections`, and `POST /projections`
without a ticket each wait for their turn in the same queue as `POST /begin`,
run, then let the next request through. Like `POST /begin`, they can get `503
TransactionQueueFull`, and closing the connection while waiting takes them out
of the queue, with nothing written. A `POST /projections` body is checked
before the call joins the queue, so an invalid one gets `400` right away.

Reads without a ticket run side by side. Writes from several threads take
turns, one call at a time. With many writers at once, retry a `503
TransactionQueueFull`.

A rebuild is not atomic as a whole. If it fails partway, run it again from
the start.

## Resetting between test runs

If the server has `devMode` on, `POST /reset` deletes every event and every
projection, and gives the store a new store ID (see [Store ID](#store-id)).
The next event appended gets sequence 1:

```sh
curl -X POST http://127.0.0.1:8085/reset
```

This is useful for a client library's own test suite: start each test, or each
test run, from an empty store instead of tracking what earlier tests left
behind. It responds `204 No Content` and only exists when `devMode` is on; see
[Deployment](/docs/guides/deployment/#developer-mode). Never rely on it against
a production instance.

`POST /reset` doesn't wait for its turn. If a transaction is active, it's
rolled back, and the next call with its ticket gets `410 TicketNotActive`. Requests
waiting for a ticket keep waiting, and get their ticket on the empty store.

## Generating test data

`tamarackdb-demo` fills a data directory with a large set of made-up events and
projections. It is useful for testing a client library against realistic volume
instead of one or two hand-written events:

```sh
./bin/tamarackdb-demo --data-dir /path/to/data --events 100000 --projections 10000 --seed 1
```

Projections have types `ProjectionType1` to `ProjectionType5` and numeric ids from 1
to `--projections`. Each id exists under only one of those types, picked at
random.

It writes straight to the database file, not through the running server, so
run it before starting `tamarackdb-server`, or against a separate data
directory. See [Building from source](/docs/contributing/building-from-source/#demo-dataset) for how to build it and what
its flags do.

## Error responses

Every error from an endpoint uses the same shape:

```json
{ "error": "InvalidRequest", "message": "afterSequence must be a non-negative integer" }
```

`error` is a stable code your code can check. `message` is a human-readable detail,
present when it helps and left out otherwise.

A few responses carry plain text instead, since they come from Go's HTTP server
before any endpoint runs: `404` for an unknown path, `405` for a known path with
the wrong method, and errors for a malformed HTTP request.

Inside a transaction, every error except `404 ProjectionNotFound` rolls the
transaction back.

| Status | `error` | Meaning |
|---|---|---|
| 400 | `InvalidRequest` | Malformed or invalid request body: bad JSON, trailing text after the JSON value, invalid query shape, more than 100 query items or more than 100 values in one item, `limit` below 1 or over the configured maximum, an unknown key in a `QUERY /events` body, an event missing `type`, a duplicate identifier or metadata value, a missing `events` field or more than 100 events in one `POST /events`, a `POST /projections` body with none of `create`, `replace`, `delete`, an unknown key in it, a projection missing its `payload` or `version`, too many projections, or a repeated projection `type` + `id`, a call that needs a ticket and carries none, and so on |
| 401 | `Unauthorized` | Missing or invalid Bearer token (only when `enableAuth` is on) |
| 404 | `ProjectionNotFound` | `GET /projections/{type}/{id}` only: no projection exists at that `type` + `id`. Doesn't end the transaction |
| 409 | `ConcurrencyException` | The Append Condition of a `POST /events` call failed, or a `POST /projections` entry doesn't match the stored projection (see [Versions](#versions)) |
| 410 | `TicketNotActive` | The ticket isn't the active one: it's unknown, or its transaction has already ended |
| 413 | `PayloadTooLarge` | An event, or a projection (its `type`, `id`, and `payload` together), is bigger than the configured maximum size, or the request body is over `maxRequestBodySize` (8 MiB by default) |
| 500 | `InternalError` | Unexpected server-side failure |
| 503 | `TransactionQueueFull` | `POST /begin`, or a projection write without a ticket: too many requests are already waiting |
| 503 | `ShuttingDown` | `POST /begin`, or a projection write without a ticket, while the server shuts down |
| 503 | `Unavailable` | `GET /health` only: storage is unreachable |
