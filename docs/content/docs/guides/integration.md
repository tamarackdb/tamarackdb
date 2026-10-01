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

## Terms

These pieces are code in your application. TamarackDB doesn't run them: it
stores what they read and write.

- **Decision model**: reads the events a command needs, decides, and appends
  new events.
- **Event handler**: code that reacts to events. There are two kinds:
  - **Projector**: computes projections from events and writes them.
  - **Processor**: reads events and may append more events in response.
- **Projection**: the current state a projector computes from events,
  identified by `type` + `id` (see [Projections](#projections)). The `type` is
  like a class, and each projection is one instance of it. Every projection
  can be rebuilt from events.
- **Write**: everything one transaction of your application sends to the
  server at once: events, Append Conditions, and projection changes (see
  [Writing](#writing)).

## Transactions

The server has no transaction that spans several requests. A transaction
lives in your client library: it collects what the command wants to write,
and sends it all in one `POST /write` at the end. The server checks it and
writes it in one SQLite transaction of its own, or writes nothing.

Many transactions can run at the same time, one per thread or request of your
application. None of them holds anything on the server while it runs: reads
never wait, and a slow client blocks no one. Conflicts between transactions
are found when each write arrives, by its Append Conditions and projection
versions (see [Append Condition](#append-condition) and
[Versions](#versions)). A transaction that became stale learns it then, with
`409 ConcurrencyException`.

The guarantee is exactly the one of the
[DCB specification](https://dcb.events/specification/): only what an Append
Condition or a projection version expresses is protected. A broader guarantee
is a business rule of your application, not something the server enforces.

### Where to commit

Your application decides where a transaction ends. TamarackDB doesn't know,
and supports both ways:

- **Atomic**: one transaction per command. The decision models, the
  processors, and the projectors all run, then one write carries every new
  event and every changed projection. Everything lands together, or nothing
  does. After a `409`, run the whole command again.
- **Eventually consistent**: the command writes its own events alone.
  Processors and projectors then run later, each in its own transaction.
  Each projector keeps the position it has processed up to (see
  [Store ID](#store-id)) in the same write as its projections, and catches up
  from there. A processor that gets a `409` reads again and retries.

In the eventually consistent way, give each projector its own projection
types, plus its own position, and run one instance of it. TamarackDB can't
check this: it doesn't know which projector wrote a projection. A `409` on a
projector's write then has an operational cause, such as two instances
running at once.

### What a client library does

A client library keeps the transaction's pending writes in memory, and
makes reads inside the transaction see them:

- **Pending events.** A read in a transaction returns the server's matching
  events, then the transaction's own pending events that match the query, at
  the end. A pending event has no `sequence` or `time` yet. Matching them
  takes a matcher in the library that follows the [query grammar](#query-grammar)
  exactly. The repository publishes shared test cases for it,
  [`testdata/query-cases.json`](https://github.com/tamarackdb/tamarackdb/blob/main/testdata/query-cases.json):
  each one is a query, an event, and whether it matches. The server checks
  them against its own SQL; replay them against your matcher.
- **Conditions against pending events.** The server checks an Append
  Condition against committed events only. Inside one transaction, a
  decision can also go stale because of a pending event added after the read
  it was based on, for example by another processor of the same command.
  Only the library knows the order of reads and pending events: when an
  event is added with a condition, check that no pending event added after
  that read matches the condition. If one does, the command was built on a
  stale view: close the transaction and report a design error, distinct from
  a `409`, since retrying can't fix it.
- **A condition needs the whole read.** A condition built from a read
  carries the read's position: the store ID, and the Sequence Position of the
  last event the server returned. The library only knows it once the read is
  paged to the end. If the application stops iterating early and then asks for
  a condition, read the rest first. If the rest is empty, the application had
  in fact seen everything, and the condition is right. If events remain, the
  application decided without seeing events that match its own query: that's a
  design error. Close the transaction and report it, and don't build the
  condition. Taking the position after the rest would be wrong: an event
  written after the decision, and read in that rest, would escape the check.
  Taking the position of the last event seen would fail every write, because
  of the older events left unread. An application that only needs to know
  whether an event exists narrows its query, or uses a condition with no read.
- **Processors running at once.** To check a condition against pending
  events, a read remembers how many pending events existed when it merged
  them: its marker. A library that runs several processors of one command at
  the same time (fibers, coroutines, threads) takes that marker and the list
  of pending events to merge at the same moment, with no suspension between
  the two: right after the server's last page arrives, just before the merge.
  Checking a new pending event against its condition and adding it to the list
  also happen in one block. Otherwise another processor can add a pending
  event in between, and the check no longer protects anything. When two
  processors of one command depend on each other, the check then fails or
  passes depending on timing. That's still a design error: the fix is in the
  application (run them one after the other, or split them differently), not
  in a retry, which could pass by luck and hide the bug.
- **Projection changes.** Keep the projections touched in the transaction by
  `type` + `id`, with the version read from the server, and send only the net
  effect: a `create`, a `replace`, or a `delete` (see
  [Writing projections](#writing-projections)). A read in the transaction
  returns the pending state of a projection it already touched. Deleting a
  projection the transaction neither read nor created is a design error:
  a `delete` needs the stored version, and the library doesn't know it.
  Close the transaction and report the error right away, without waiting for
  the write. The application reads the projection first.
- **Positions.** A Sequence Position only means something next to the store
  ID it was read on (see [Store ID](#store-id)). Hand them out together.

A transaction with nothing to write doesn't need to call the server at all.

A transaction doesn't freeze a view of the store. Each read is a separate
call, and sees what is committed at the moment it runs. Two reads of the same
transaction can see different states: another client may write between them,
and reading the same query twice can give two different results. What
protects a decision is the condition built from each read, with its own
`afterSequence`: the write is refused if an event that matters arrived after
that read. Don't combine two reads as if they described the same moment
without a condition on each one.

### A lost write response

If the connection drops before the `POST /write` response arrives, you can't
tell whether the write happened: once a write has started, it goes to the
end, even if the client is gone. Reading the events again doesn't settle it
either: finding nothing can mean "not written" or "not written yet".

To retry safely, send the same write again, with the same Append Conditions,
`afterSequence` included. Writes are served in the order they arrive, so the
retry comes after the first attempt. If the first one didn't go through, the
retry is written normally. If it did, the retry fails with `409
ConcurrencyException` instead of writing the same events twice, but only if
the first attempt left something the retry checks:

- an event that matches one of its conditions (a condition only sees the
  events that match its `failIfEventsMatch`);
- or a projection change, whose version no longer matches.

A write with no condition and no projection change, or whose events match
none of its conditions, would be written twice.

To make any write safe to retry, give it a unique `writeId` in the metadata
of each of its events, the same for every attempt, and add a condition on it
with no `afterSequence`:

```json
{
  "failIfEventsMatch": [
    { "metadata": [ { "name": "writeId", "value": "0f8e2d4c-9a1b-4c3d-8e7f-6a5b4c3d2e1f" } ] }
  ]
}
```

The name is up to you: TamarackDB gives it no meaning. A `409` on the retry
is settled by reading the events with that `writeId`. If you find them, the
first attempt went through. If not, another write broke one of your
conditions, and since the first attempt carries the same ones, it can't go
through either.

## Reading events

Read events with the HTTP `QUERY` method:

```sh
curl -X QUERY http://127.0.0.1:8085/events \
  -H "Content-Type: application/json" \
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

A read sees committed events only. It never waits for a write. The response
carries the store ID (see [Store ID](#store-id)).

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

`time` is when TamarackDB wrote the event, in UTC, with exactly 6
fractional digits. Convert it to local time in your application if you need
to display it. It comes from the server's clock, which can jump back: `time`
usually follows `sequence` order, but nothing guarantees it. Order events by
`sequence`, never by `time`.

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

Values are compared exactly: case counts, and no Unicode normalization
happens. An identifier and a metadata entry with the same name are different
things: a `userId` in `metadata` doesn't match a `userId` asked for in
`identifiers`.

A query carries at most 100 query items, and an item at most 100 values across
its `types`, `identifiers`, and `metadata` combined. A larger query gets `400`.
The same limits apply to `failIfEventsMatch`.

One more, optional, top-level key narrows a query further: `afterSequence`,
only events with a Sequence Position strictly greater than this value.

```json
{ "query": "*", "afterSequence": 12345 }
```

There is no filter on `time`. To find events by period, tag them when you
write them (for example a `month` metadata entry) and query that tag.

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

Read each page as it arrives. The server gives each line of a page 30
seconds to go out: a client that stops reading for longer gets its
connection closed, and sees a page with no trailer. Resume it the same way,
from the last full line.

The same loop that pages through history can also follow new events live: keep
polling with `afterSequence` set to the last Sequence Position you saw. Once
`hasMore` reads `false`, you have caught up, and further polling picks up new
events as they are written. `tamarackdb-backup` (see
[Backup](/docs/guides/backup/)) is a real example of this loop: it pages through
`/events` with `afterSequence` set to the last sequence it saved locally, and
stops once `hasMore` reads `false`.

`limit` defaults to whatever the server operator set (`defaultEventsPerPage`,
1000 out of the box) and is capped at `maxEventsPerPage` (10000 out of the
box). Asking for more than that gets you `400 Bad Request`.

### Store ID

Every response that depends on the store carries the store ID in the
`X-Tamarackdb-Store` header: a UUID that names the history you read or wrote.
`QUERY /events` sends it on every page, empty ones included,
`GET /projections/{type}/{id}` on both `200` and `404`, and `POST /write` on
`200`. A read takes it in the same snapshot as the data it returns, and a
write in the same SQLite transaction as what it wrote.

The store ID only changes when the store is emptied with `POST /reset` (dev
mode only). After that, Sequence Positions start over at 1, and a position you
kept from before names a different event. A position is therefore a pair:
the store ID and the Sequence Position. Keep them together. If a later read
returns a different store ID, start over from the beginning.

## Writing

`POST /write` sends one write: events to append, the Append Conditions they
depend on, and projection changes.

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
        "failIfEventsMatch": [
          { "identifiers": [ { "name": "userId", "value": "123" } ] }
        ],
        "afterSequence": 12345,
        "store": "5b0c7e2a-1f4d-4a9b-8c3e-6d2f1a0b9e47"
      }
    ],
    "projections": {
      "replace": [
        {
          "type": "user-profile",
          "id": "123",
          "version": "9f3c2a1e-7b4d-4c8e-a5f6-0d1e2f3a4b5c",
          "payload": "{\"name\":\"Ada Lovelace\"}"
        }
      ]
    }
  }'
```

The server checks every condition first, against the events committed
before this write. If every one holds, it writes the projections and appends
the events, all in one SQLite transaction. Either everything is written, or
nothing is.

Every key is optional, and a missing one is an empty list. The body is
checked strictly: an unknown key gets `400`, so a misspelled key never drops
data without a word. A write with nothing at all responds `200` right away.
Conditions are checked even when the write carries nothing else.

On success (`200 OK`), the response carries the store ID in the
`X-Tamarackdb-Store` header, the Sequence Position and `time` of each event in
the order you sent them, and the new version of each created and replaced
projection:

```json
{
  "events": [
    { "sequence": 12348, "time": "2026-09-01T14:25:00.000000Z" }
  ],
  "projections": {
    "create": [],
    "replace": [ { "version": "d4c3b2a1-0f9e-4d8c-b7a6-5f4e3d2c1b0a" } ]
  }
}
```

Every event of one write shares the same `time`; order within a write comes
from `sequence`.

### Waiting for a turn

Writes go through one at a time. A write waits for its turn, with its
connection held open, behind the writes that arrived before it. Each one
holds the turn only for the time of its own SQLite transaction, usually a few
milliseconds. The body is read and checked before the write joins the queue,
so an invalid one gets `400` right away, and a client sending its body slowly
holds up no one.

A waiting write can fail with `503 WriteQueueFull`: too many requests are
already waiting, and this one never joined the queue. The server puts no
limit on how long a write waits. Your client sets its own: when it no longer
wants to wait, it closes the connection, and the write leaves the queue with
nothing written. Once a write has started, it goes to the end, even if the
client is gone (see [A lost write response](#a-lost-write-response)).

### Events

`identifiers` and `metadata` are objects whose values are a string, or an array of
strings: an array gives one tag per value, all on the same event. An event
carries at most 20 identifiers and 20 metadata values, and never the same
`{name, value}` pair twice. `payload` is an opaque string. TamarackDB never
parses it, so its format (JSON, XML, or anything else) is entirely up to the
calling application.

A write carries at most `maxEventsPerWrite` events (100 out of the box), each
at most `maxEventSize` bytes (64 KiB out of the box): the combined size of its
`type`, `identifiers`, `metadata`, and `payload`. The limit counts every event
of the transaction, since they all go out in one write. Put larger content
(files, images) in external storage, and reference it from the event instead
of embedding it.

### Append Condition

An Append Condition makes the write fail if something relevant happened
since you last read:

```json
{
  "failIfEventsMatch": [
    { "identifiers": [ { "name": "userId", "value": "123" } ] }
  ],
  "afterSequence": 12345,
  "store": "5b0c7e2a-1f4d-4a9b-8c3e-6d2f1a0b9e47"
}
```

`afterSequence` is the Sequence Position you read up to (see Pagination
above), and `store` the store ID that read returned. `failIfEventsMatch` is a
query, using the same grammar as a read. The condition fails if any event
matching it exists after `afterSequence`, or if the store ID is no longer
`store`.

A condition with `afterSequence` must carry `store`, and a condition without
it must not: it read nothing, so it holds on any store.

Both `failIfEventsMatch` and `afterSequence` are optional:

- `failIfEventsMatch` alone fails if any matching event exists at all. It
  suits a decision that rests on no read, for example "fail if a
  `user-registered` event with this email exists".
- `afterSequence` alone fails if any event at all exists after it.
- A condition with neither field always holds: it says nothing, so it
  protects nothing.

A write carries a list of conditions, at most `maxEventsPerWrite` of them,
and every one must hold. A transaction usually has one per decision: each
decision model or processor adds the condition its own read supports. This
is more precise than one merged condition, and never a partial success.

The flow is optimistic: read, decide, then write with the condition that
describes what the decision depends on. If another client wrote a matching
event in between, the write gets `409 ConcurrencyException`, with a
`message` naming the condition, for example `conditions[1] no longer holds`
or `conditions[0] was read on another store`. Nothing is written: read
again, decide again, and send a new write.

### Writing projections

`projections` creates, replaces, and deletes projections in the same write as
the events:

```json
{
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
}
```

- `create` takes `type`, `id`, and `payload`. The projection must not exist
  yet.
- `replace` takes `type`, `id`, `version`, and `payload`. The whole payload is
  replaced; there is no partial update.
- `delete` takes `type`, `id`, and `version`.
- A payload is a string; an empty string is a valid payload. A missing or
  `null` payload gets `400`.
- Each list is optional.
- The same `type` + `id` can't appear twice in one write, across all three
  lists.
- A write carries at most `maxProjectionsPerWrite` projections in total (500
  out of the box), each at most `maxProjectionSize` bytes (64 KiB out of the
  box), counting its `type`, `id`, and `payload` together.

### Limits

Every request body is capped at `maxRequestBodySize` (8 MiB out of the box).
That cap isn't checked against the other limits: a write can reach it before
each of its items reaches its own. Every error from a limit names the setting
to raise, for example `request carries 612 projections, more than
maxProjectionsPerWrite (500)`. The defaults are a cautious starting point:
find your real limits in development, with your application's data, and
have the operator set them for production (see
[Deployment](/docs/guides/deployment/#configure)).

## Projections

A projection is an opaque payload identified by `type` + `id`, with no history
(see [Terms](#terms)). It can be overwritten or deleted; the store only holds
its current state. Since every projection can be rebuilt from events (see
[Projection rebuilds](#projection-rebuilds)), backups leave projections out.
Projections are written in the same write as events, so both become durable
together, or neither does.

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
[Versions](#versions)). The read sees committed projections only, and the
response carries the store ID (see [Store ID](#store-id)).

A projection that doesn't exist gets `404 ProjectionNotFound`. This is an
ordinary answer, not a failure: a projector that gets a `404` usually creates
the projection.

A projection is always read by `type` and `id`. There is no query over projections.

`type` and `id` go in the URL as path segments, so percent-encode them: an
`id` of `a/b` is `/projections/user-profile/a%2Fb`, and a space is `%20`. The
same goes for `DELETE /projections/{type}`.

### Versions

Every projection has a version, a random UUID that changes on every write.
A `replace` or a `delete` carries the version you read. If the stored
projection has a different version, or no longer exists, the write gets `409
ConcurrencyException`, and nothing is written. A `create` gets the same `409`
if the projection already exists. The `message` names the entry by its place
in the body, for example `projections.replace[0] no longer has the given
version`.

A `409` on a projection means another write changed it since you read it.
Read it again and redo the work, in a new transaction. A projection created
without reading it first goes out as a `create`, and fails if the projection
already exists: read before you write.

Only the projections a write changes are checked. A projection the
transaction only read is not: the server never learns what a transaction
read. So base a decision on events, never on a projection. A projection can
be stale the moment you read it, and nothing protects a decision made on it.
A decision that must hold is protected by an Append Condition on the events
it rests on (see [Append Condition](#append-condition)).

The version is opaque. Compare it only for equality, and never compute it: a
stale copy never matches again, even after the projection is deleted and
created anew. To write the same projection again later, use the version from
the last response.

### What a projection may depend on

A projection can depend on anything in an event. What it may use depends on
where your application commits (see [Where to commit](#where-to-commit)):

- **Atomic**: projectors run before the write, so the events have no
  `sequence` or `time` yet. A projection can't use them. Put a business date
  in the payload or the metadata instead.
- **Eventually consistent**: projectors read events that are already
  written, so a projection may use `sequence` and `time` too.

Either way, a rebuild reads the same events back from `QUERY /events`, so it
produces the same projections.

## Projection rebuilds

A rebuild replays events to write projections again. How you organize it is
up to you: one thread replaying every event in order, several projectors in
parallel, or anything else. These calls are what it's built from:

- Delete the projections to rebuild, one type at a time, or all of them:

  ```sh
  curl -X DELETE http://127.0.0.1:8085/projections/user-profile
  curl -X DELETE http://127.0.0.1:8085/projections
  ```

- Page through `QUERY /events` to read the events to replay.

- Write the rebuilt projections with `POST /write`, in one write or several.
  A projection is a `create` the first time, then a `replace` with the version
  the previous write returned.

- Read a projection back with `GET /projections/{type}/{id}`: the response
  carries its payload and its version.

One write keeps the rebuild atomic, but it must fit under
`maxProjectionsPerWrite` and `maxRequestBodySize`, and it holds the turn for
as long as the insert takes. Several writes each fit the limits; write the
projector's position (see [Store ID](#store-id)) with each one, so a rebuild
that stops halfway resumes from there. The choice is your application's.

`DELETE /projections/{type}` and `DELETE /projections` wait for their turn in
the same queue as `POST /write`, run, then let the next request through. A
write queued before a delete goes through first, and the delete then removes
what it wrote. Like `POST /write`, a delete can get `503 WriteQueueFull`, and
closing the connection while waiting takes it out of the queue, with nothing
deleted. A write that replaces or deletes a projection a bulk delete already
removed gets `409`.

## Resetting between test runs

If the server has `devMode` on, `POST /reset` deletes every event and every
projection, and gives the store a new store ID (see [Store ID](#store-id)).
The next event written gets sequence 1:

```sh
curl -X POST http://127.0.0.1:8085/reset
```

This is useful for a client library's own test suite: start each test, or each
test run, from an empty store instead of tracking what earlier tests left
behind. It responds `204 No Content` and only exists when `devMode` is on; see
[Deployment](/docs/guides/deployment/#developer-mode). Never rely on it against
a production instance.

`POST /reset` waits for its turn in the same queue as writes. The writes
queued before it go through, then the reset deletes them. A write queued
after it goes to the new store: if one of its conditions carries the old
store ID, it gets `409`. So does a `replace` or `delete` of a projection,
since the version it carries no longer exists.

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

| Status | `error` | Meaning |
|---|---|---|
| 400 | `InvalidRequest` | Malformed or invalid request body: bad JSON, trailing text after the JSON value, an unknown key, invalid query shape, more than 100 query items or more than 100 values in one item, `limit` below 1 or over `maxEventsPerPage`, an event missing `type`, a duplicate identifier or metadata value, a condition with `afterSequence` and no `store` or the other way around, a projection missing its `payload` or `version`, a repeated projection `type` + `id`, more events, conditions, or projections than one write allows, and so on. The `message` names the item at fault, for example `events[3]`, and the setting behind a limit |
| 401 | `Unauthorized` | Missing or invalid Bearer token (only when `enableAuth` is on) |
| 404 | `ProjectionNotFound` | `GET /projections/{type}/{id}` only: no projection exists at that `type` + `id` |
| 409 | `ConcurrencyException` | `POST /write` only: an Append Condition doesn't hold, or was read on another store, or a projection doesn't match the stored one (see [Versions](#versions)). The `message` names the item, for example `conditions[1]` or `projections.replace[0]`. Nothing was written |
| 413 | `PayloadTooLarge` | An event, or a projection (its `type`, `id`, and `payload` together), is bigger than its configured maximum size, or the request body is over `maxRequestBodySize`. The `message` names the setting |
| 500 | `InternalError` | Unexpected server-side failure |
| 503 | `WriteQueueFull` | `POST /write`, a bulk delete, or `POST /reset`: too many requests are already waiting. Nothing was written |
| 503 | `ShuttingDown` | `POST /write`, a bulk delete, or `POST /reset`, while the server shuts down. Nothing was written |
| 503 | `Unavailable` | `GET /health` only: storage is unreachable |
