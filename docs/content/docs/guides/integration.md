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
[Deployment](/docs/guides/deployment/#configure)). Add `-H "Authorization: Bearer <token>"` to
every request when `enableAuth` is on (see [Deployment](/docs/guides/deployment/#configure)).

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
- `503 Paused`: the server is paused for a projection rebuild (see
  [Projection rebuilds](#projection-rebuilds)).

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
- the client closes the connection while a call with its ticket is running;
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

Two more, optional, top-level keys narrow a query further:

- `afterSequence`: only events with a Sequence Position strictly greater than this
  value
- `time: { "from": "...", "before": "..." }`: only events whose `time` falls
  in this range (`from` inclusive, `before` exclusive). Either key, or `time`
  itself, can be left out. A bound may use any RFC 3339 offset: it's converted
  to UTC before comparing.

```json
{
  "query": "*",
  "afterSequence": 12345,
  "time": { "from": "2026-01-01T00:00:00.000000Z" }
}
```

The `time` filter is for search and inspection. Only the Sequence Position
defines the order of events.

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

{"name":"Ada Lovelace"}
```

The response body is the payload exactly as written, not wrapped in a JSON
envelope: its own format (JSON, XML, plain text) is up to the writing
application.

The ticket is optional, as for events:

- **With a ticket**, the read sees projections written earlier in the same
  transaction. A projector uses it to read a projection before changing it.
- **Without a ticket**, the read sees committed projections only. This is how you
  read a projection to display a page.

A projection that doesn't exist gets `404 ProjectionNotFound`. Inside a
transaction, this is an ordinary answer, not an error: the transaction goes on.
A projector that gets a `404` usually creates the projection.

A projection is always read by `type` and `id`. There is no query over projections.

### Writing projections

`POST /projections` writes or deletes several projections at once. The ticket is
required, except during a projection rebuild (see
[Projection rebuilds](#projection-rebuilds)).

```sh
curl -X POST http://127.0.0.1:8085/projections \
  -H "Content-Type: application/json" \
  -H "X-Tamarackdb-Ticket: a045ad63-5d4b-4847-8eb9-fbddb4e2d65b" \
  -d '{
    "projections": [
      { "type": "user-profile", "id": "123", "payload": "{\"name\":\"Ada Lovelace\"}" },
      { "type": "user-list-entry", "id": "456", "payload": null }
    ]
  }'
```

- A projection with a `payload` is created, or replaced if it exists.
- A projection with a `null` payload is deleted. Deleting a projection that
  doesn't exist does nothing.
- The same `type` + `id` can't appear twice in one call.
- A call carries at most `maxProjectionsPerRequest` projections (100 out of
  the box), each payload at most `maxProjectionSize` bytes (64 KiB out of the
  box).
- An empty `projections` array writes nothing. Your application can send its
  usual call even when its event handlers changed no projection. A missing
  `projections` field gets `400`: it's most likely a misspelled key.

It responds `204 No Content`.

There is no version and no concurrency check on projections. None is needed:
your projector reads the projection inside the transaction, changes it, and
writes it back, while the transaction holds the write lock. Nothing else can
change the projection in between.

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

A projection rebuild runs while the server is paused. A pause stops the server
from giving out tickets, so no transaction is active while you rebuild.

1. Pause the server:

   ```sh
   curl -X POST http://127.0.0.1:8085/pause
   ```

   The request waits its turn behind every transaction already queued, then
   responds `204 No Content`. From then on, every `POST /begin` gets
   `503 Paused`.

2. Delete the projections to rebuild, one type at a time, or all of them:

   ```sh
   curl -X DELETE http://127.0.0.1:8085/projections/user-profile
   curl -X DELETE http://127.0.0.1:8085/projections
   ```

3. Page through `QUERY /events` without a ticket, and run each page through
   your projectors.

4. Write the rebuilt projections with `POST /projections` without a ticket. Each
   call commits on its own.

5. Resume:

   ```sh
   curl -X POST http://127.0.0.1:8085/resume
   ```

`DELETE /projections/{type}`, `DELETE /projections`, and `POST /projections` without
a ticket are accepted only while the server is paused. Outside a pause, they
get `409 NotPaused`. Reads without a ticket work at all times.

`POST /pause` and `POST /resume` both respond `204 No Content`, whether the
server was already in that state or not.

A rebuild is not atomic as a whole. If it fails partway, run it again from
step 2. The pause survives a server restart: the server stays paused until
`POST /resume`, so your application can't write on half-rebuilt projections.
Your application is expected to be fully down during a rebuild.

## Resetting between test runs

If the server has `devMode` on, `POST /reset` deletes every event and every
projection. The next event appended gets sequence 1:

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
The pause state stays as it is.

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

Every error uses the same shape:

```json
{ "error": "InvalidRequest", "message": "afterSequence must be a non-negative integer" }
```

`error` is a stable code your code can check. `message` is a human-readable detail,
present when it helps and left out otherwise.

Inside a transaction, every error except `404 ProjectionNotFound` rolls the
transaction back.

| Status | `error` | Meaning |
|---|---|---|
| 400 | `InvalidRequest` | Malformed or invalid request body: bad JSON, invalid query shape, `limit` over the configured maximum, an invalid `time` bound, an event missing `type`, a duplicate identifier or metadata value, a missing `events` field or more than 100 events in one `POST /events`, a missing `projections` field, too many projections, or a repeated projection `type` + `id` in one `POST /projections`, a call that needs a ticket and carries none, and so on |
| 401 | `Unauthorized` | Missing or invalid Bearer token (only when `enableAuth` is on) |
| 404 | `ProjectionNotFound` | `GET /projections/{type}/{id}` only: no projection exists at that `type` + `id`. Doesn't end the transaction |
| 409 | `ConcurrencyException` | The Append Condition of a `POST /events` call failed |
| 409 | `NotPaused` | `DELETE /projections`, `DELETE /projections/{type}`, or `POST /projections` without a ticket, while the server isn't paused |
| 410 | `TicketNotActive` | The ticket isn't the active one: it's unknown, or its transaction has already ended |
| 413 | `PayloadTooLarge` | An event, or a projection's `payload`, is bigger than the configured maximum size |
| 500 | `InternalError` | Unexpected server-side failure |
| 503 | `TransactionQueueFull` | `POST /begin` or `POST /pause`: too many requests are already waiting |
| 503 | `Paused` | `POST /begin` while the server is paused |
| 503 | `Unavailable` | `GET /health` only: storage is unreachable |
