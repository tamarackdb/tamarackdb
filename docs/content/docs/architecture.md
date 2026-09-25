---
title: "Architecture"
slug: "architecture"
weight: 2
---

## Context

TamarackDB is an event store in Go. It follows the
[DCB (Dynamic Consistency Boundaries) specification](https://dcb.events/specification/), is reachable over HTTP, and
uses SQLite as its storage engine. The service runs as a single instance ("single brain"), not a multi-instance
cluster.

It also stores documents: projections an application reads and updates in the same transaction as the events that
changed them (see Documents). An application that keeps its projections elsewhere never has to touch this mechanism.

Applications can share a single TamarackDB instance when they share events. TamarackDB does not track which
application produced an event.

### Name

TamarackDB takes its name from the tamarack (*Larix laricina*), a conifer native to Quebec's boreal forest. It's one
of the few conifers used in dendrochronology, because its growth rings are unusually clear and easy to read. Each ring
records one season, laid down once and never changed. You can read the tree's whole history by reading the rings from
the center out. This event store works the same way: an ordered, append-only list of facts that never change, from
which you rebuild current state by replaying them.

### Scope

TamarackDB serves applications with modest throughput and few concurrent writers: a handful of users at a time, where
two changes in the same second are rare. Every design choice here follows from that scope: one SQLite file, one
transaction at a time, no clustering.

It trades speed for simplicity, on purpose. The short version is "maybe slow, but dead simple". A system that needs
high write throughput, or that is built around eventual consistency, is not a good fit for TamarackDB.

### Transactional model

TamarackDB is built for applications that process a command synchronously and atomically. A typical command, inside
one request of the application:

1. A Decision Model reads events, decides, and appends new events.
2. Event handlers react to those events in the same request. Projectors update documents. Processors read events,
   including the ones just appended, and may append follow-up events, which trigger more handlers.
3. Everything lands together, or nothing does. If any handler fails, no event and no document is persisted.

This needs a transaction that spans several HTTP calls: the handlers must read what the command just appended, before
anything is committed. TamarackDB provides exactly that:

- A client opens a transaction and gets a ticket. Every call that carries the ticket runs inside one SQLite
  transaction: reading events, appending events, reading documents, writing documents.
- Only one transaction exists at a time. It holds SQLite's write lock from the moment its ticket is given out until it
  ends. Other clients wait their turn in a FIFO.
- The client ends the transaction explicitly, with a commit or a rollback. A deadline rolls it back if the client
  never does.
- A transaction fits inside one request of the application, on the server side. The speed of the end user's browser
  connection has no effect on how long it lasts.

The consistency boundary is the whole store, with a pessimistic lock. That's the widest boundary possible: the
guarantee is stronger than DCB requires, not weaker (see DCB compliance).

Every transaction costs several HTTP round trips. TamarackDB is meant to run on the same host as the application, and
listens on a unix socket by default, which keeps each round trip short (see Security).

## Data model

### Identifiers

An **Identifier** is stored internally as a structured pair:

```json
{"name": "courseId", "value": "123"}
```

This avoids the escaping problems of a delimited string like `"courseId:123"`, and allows direct indexing on a
`name + value` btree index.

**JSON contract on the API side (appending an event)**: an object whose values are strings, or arrays of strings, to
cover the multi-value case directly:

```json
{
  "courseId": ["foo", "bar"],
  "otherId": "baz"
}
```

Each key becomes a `name`. Each value, or array element, becomes its own `{name, value}` row. An event carrying
`courseId: ["foo", "bar"]` has both `courseId:foo` **and** `courseId:bar` at the same time.

### Metadata

**Metadata** is stored the same structured way as Identifiers, a `{name, value}` pair, and follows the same JSON
contract on the API side (an object whose values are strings or arrays of strings).

Identifiers and metadata are two separate namespaces on an event. The same name can be used in both without clashing.
Each also carries its own meaning: business identifiers name domain concepts, everything else (who wrote it,
correlation, tenant, and so on) is Metadata. Both are stored and indexed the same way, and both can appear in a
`QueryItem`.

**Tag** is the general term for a `{name, value}` pair, covering both Identifiers and Metadata. The store treats both
the same way internally, and "Tag" avoids saying "identifier or metadata" every time. Application code, though, always
treats them separately: knowing whether a `{name, value}` pair is an Identifier or a piece of Metadata tells the
application how to read the event back correctly.

Splitting the spec's single Tag concept into Identifiers and Metadata still follows the DCB specification. The spec
allows implementations to use different terms and field names, as long as they work the same way. Both Identifiers and
Metadata behave exactly like Tags for matching. The split is just a naming choice on top of that, not a deviation from
the spec.

An event can't carry the same `{name, value}` pair twice in its identifiers, or twice in its metadata. This matches the
DCB specification's own rule that a set of Tags should not contain duplicates. A `POST /events` call that breaks this
rule gets `400 Bad Request`, instead of being silently deduplicated, like every other invalid request (see Error
responses).

An event can't carry more than **20 identifiers**, or more than **20 metadata** entries. These are fixed limits, not
configuration, for the same reason as the cap on events per `POST /events` call (see Appending events): an event
should stay a short, meaningful statement, not a container for a large list of values. A `POST /events` call that
breaks this rule gets `400 Bad Request`.

### Two categories of data associated with an event

| Category | Role | Examples |
|---|---|---|
| **Identifiers** | Business identifiers, the main way to filter in DCB | `courseId`, `userId` as a subject |
| **Metadata** | Everything else about the event, also queryable | `tenantId`, `userId` (author), `correlation_id` |

### Volume context

Volume stays well within SQLite's normal limits with btree indexing: several million rows across the identifiers and
metadata tables, at 1 to 2 identifiers per event, based on real production numbers.

## Query grammar (per the DCB spec)

A `Query` is an **array of `QueryItem`**, combined with **OR**:

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
  }
]
```

For each `QueryItem`:
- **OR** across `types` (the event must match one of the listed types)
- **AND** across `identifiers` (the event must carry **all** of the listed identifiers)
- **AND** across `metadata` (the event must carry **all** of the listed metadata)
- The three are combined with **AND**: an event must satisfy its type, its identifiers, and its metadata together

Negation (`<>`, and so on) is not allowed. It's left out on purpose: a negation describes an unlimited set of matching
events ("anything that isn't X"), so there's no way to guarantee that no future event could break the condition.

Two special cases from the spec, worth calling out:
- **`Query.all()`**: a Query can mean "all events", with no keys to list. Used both to read the whole store, and as
  `failIfEventsMatch` for an Append Condition that must fail if *any* event exists past `afterSequence`.
- **`afterSequence` can be past the last matching event**: this number is just what the client had seen, not
  necessarily a real event. The concurrency check compares `sequence > afterSequence` against matching events.

Every array in this grammar (the top-level `query`, or a `QueryItem`'s `types`, `identifiers`, or `metadata`) must be
non-empty when present. An empty array gets `400 Bad Request`, rather than meaning something special. To leave an axis
open within a `QueryItem`, leave that key out entirely, instead of sending `[]`. To match every event, use `"*"` in
place of `query`, instead of an empty array.

A `QueryItem` must specify at least one of `types`, `identifiers`, or `metadata`. An empty item (`{}`) is invalid and
gets `400 Bad Request`: it poses no constraint, and isn't the documented way to say "all events" either, that's what
`"*"` is for at the whole-query level, not something an item can express on its own.

If two `QueryItem` in the same array are exact duplicates (same types, identifiers, and metadata, in any order),
TamarackDB silently keeps one and drops the rest: a repeated item adds nothing to the OR beyond a wasted clause. This
applies to `query` on `QUERY /events` and to `condition.failIfEventsMatch` on `POST /events` alike, since both use
this same grammar. It's useful when a client builds one Query by merging several sources (several models reacting to
the same identifier, for example) and doesn't want to bother deduplicating them itself first.

## HTTP API

Every endpoint is named by the resource it acts on (`/events`, `/documents`) or by the transaction step it performs
(`/begin`, `/commit`, `/rollback`). A call that belongs to a transaction carries the ticket in the
`X-Tamarackdb-Ticket` header.

| Endpoint | Ticket | Purpose |
|---|---|---|
| `POST /begin` | none | Wait for a turn in the FIFO, open a transaction, get a ticket |
| `QUERY /events` | optional | Read events, inside the transaction or from committed data |
| `POST /events` | required | Append events, with an optional Append Condition |
| `GET /documents/{type}/{id}` | optional | Read one document, inside the transaction or from committed data |
| `POST /documents` | required, or none while paused | Write or delete documents |
| `POST /commit` | required | Commit the transaction |
| `POST /rollback` | required | Roll the transaction back |
| `DELETE /documents/{type}` | none, paused only | Delete every document of one type |
| `DELETE /documents` | none, paused only | Delete every document |
| `POST /pause` | none | Stop giving out tickets, once earlier transactions are done |
| `POST /resume` | none | Give out tickets again |
| `POST /reset` | none, dev mode only | Delete all events and documents |

`GET /health`, `GET /metrics`, and `GET /debug` are covered in Management / observability.

### Transactions

#### Opening a transaction

`POST /begin` opens a transaction and responds with its ticket:

```json
{ "ticket": "a045ad63-5d4b-4847-8eb9-fbddb4e2d65b" }
```

It takes no request body. How long a transaction may last is set by the operator, not the client (see Deadline and
ceiling).

If another transaction is active, the request waits in a FIFO, with its HTTP connection held open, until its turn
comes. The SQLite transaction starts with `BEGIN IMMEDIATE` at the moment the ticket is given out: the write lock is
held from the first operation, reads included, until the transaction ends.

A client that disconnects while waiting leaves the FIFO. A request that waits longer than the configured maximum wait
gets `503 TransactionWaitTimeout`, with a `Retry-After` header. A request that arrives when the FIFO is already at its
configured depth gets `503 TransactionQueueFull` right away, instead of joining (see Configuration). While the server
is paused, a request that reaches the head of the FIFO gets `503 Paused` (see Pause).

#### Deadline and ceiling

A transaction must end before its deadline, or it's rolled back automatically. Two limits set the deadline, both
configuration, neither chosen by the client:

- **An idle timeout**, 5 seconds by default. The deadline starts at the moment the ticket is given out plus the idle
  timeout. Each call made with the ticket renews it: when the call ends, the deadline moves to that moment plus the
  idle timeout. A client that stops making calls, because it crashed or hangs, loses its transaction after that long.
- **A total ceiling**, 15 seconds by default, counted from the moment the ticket is given out. Renewals never push the
  deadline past it. It keeps a buggy client that keeps making calls from holding the store indefinitely.

The deadline is a safety net for a client that fails, not a time budget for a command. A healthy client always ends
its transaction itself, with a commit or a rollback, long before either limit. The store holds a single global lock,
so how long one client may hold it is the operator's decision: it's what every other client waits for.

#### Calls inside a transaction

Every call inside a transaction carries the ticket in the `X-Tamarackdb-Ticket` header. A call with a ticket that
isn't active (unknown, already committed, rolled back, or expired) gets `410 TransactionNotActive`.

A call that fails inside a transaction ends it: the transaction is rolled back, and the ticket stops being active.
This covers every error response: a malformed request, a failed Append Condition, a payload over its size limit, an
internal error. A `404 DocumentNotFound` from `GET /documents/{type}/{id}` is not an error in this sense: it's an
ordinary answer, and the transaction goes on.

#### Ending a transaction

A transaction ends, and gives its turn to the next one in the FIFO, in one of these cases:

- The client calls `POST /commit`.
- The client calls `POST /rollback`. The transaction is rolled back.
- A call inside the transaction fails. The transaction is rolled back.
- The deadline passes before `POST /commit`. The transaction is rolled back.

`POST /commit` runs in this order:

1. The ticket stops being active.
2. The SQLite transaction commits. Events and documents become durable together.
3. The next transaction in the FIFO gets its ticket.

A commit never fails with `409`: every Append Condition was already checked when its events were appended. `POST
/commit` and `POST /rollback` both respond `204 No Content`.

If the connection drops before the `POST /commit` response reaches the client, the ticket is already inactive and the
next transaction can take its turn. The client can't tell whether its data was committed. This is the same
lost-acknowledgment problem as any request/response protocol. It's rare enough that TamarackDB leaves it to the
application's user experience: reloading the page shows whether the change was applied.

### Reading events

Events are read with `QUERY /events`, using the [HTTP QUERY method](https://www.rfc-editor.org/info/rfc10008/)
(RFC 10008): safe, idempotent, and cacheable like GET, but carrying a JSON body like POST. This is needed since a Query
can be too large or nested to fit in a query string.

The request body is a JSON object with a `query` key. That key holds either the array of `QueryItem` shown above, or
the literal string `"*"` for `Query.all()`, plus optional `afterSequence` / `time` keys:

```json
{
  "query": [
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
  ],
  "afterSequence": 12345,
  "time": {
    "from": "2026-01-01T00:00:00.000000Z",
    "before": "2026-02-01T00:00:00.000000Z"
  }
}
```

`Query.all()` is the literal string `"*"` in place of the `query` array: no `QueryItem` to filter by means every event
matches.

`afterSequence` limits the read to events with a Sequence Position strictly greater than the given value, the same
`sequence > afterSequence` rule used by the Append Condition's concurrency check. It's optional: leaving it out reads
from the start of the store.

`time` limits the read to events whose `time` falls in the given range: `time.from` is inclusive (`>=`),
`time.before` is exclusive (`<`). Both `time` itself, and its `from`/`before` keys, can be left out independently, so
a query can filter from a point on, up to a point, or between two points. A bound may carry any RFC 3339 offset: it's
converted to UTC before the comparison. The filter exists for search and inspection (for example "events from last
January"), and plays no part in the Append Condition: only Sequence Position defines the order of events.
`afterSequence` and `time` can be combined; an event must match both when both are given.

**With a ticket**, the read runs inside the transaction, on the write connection. It sees every event appended
earlier in the same transaction, even though nothing is committed yet. This is how an event handler reads what the
command just appended.

**Without a ticket**, the read runs on the read connection pool, outside any transaction. It sees committed events
only, and is never blocked by the active transaction (see Reads).

### Pagination

Events always come back in ascending Sequence Position order, the causal order of the log, and the only order a
Decision Model ever needs when replaying history. No `order` option exists.

An optional `limit` key caps how many events come back in one response:

```json
{ "query": [...], "afterSequence": 12345, "limit": 500 }
```

Pagination uses a cursor, not an offset. An offset would be unstable on a log that keeps growing: events appended
between two page requests would shift it, causing skipped or repeated results. The cursor is `afterSequence` itself:
a Sequence Position never changes and only ever increases, so it stays a valid resume point no matter what else gets
written meanwhile. To fetch the next page, the client repeats the same `query` with `afterSequence` set to the
Sequence Position of the last event it received.

The response carries a `hasMore` boolean, so the client never has to guess whether it reached the end. The server
fetches `limit + 1` rows. If it gets that many, it trims the result back to `limit` and returns `hasMore: true`.
Otherwise it returns everything it got and `hasMore: false`.

Both the default `limit` (used when a request leaves it out) and the server-enforced maximum (the highest `limit` a
request may ask for) are configuration, not fixed constants (see Configuration). How fast an application's
projections can process a batch of events (see Projection rebuilds) varies enough between applications, and even
between projections in the same application, that one fixed page size wouldn't fit all of them.

Left unset, `limit` falls back to a default of **1,000**, with a server-enforced maximum of **10,000**. That's sized
so a default page is easy to buffer client-side, and a page at the maximum still finishes in a matter of seconds even
for a fast projection. Without a ticket, this also keeps each SQLite read transaction short (see Projection
rebuilds). Asking for more than the configured maximum gets `400 Bad Request` (see Error responses).

### Response format

The response body is [NDJSON](https://github.com/ndjson/ndjson-spec) (`Content-Type: application/x-ndjson`): one JSON
value per line, separated by `\n`, instead of a single JSON array wrapping the whole page. Each line is a
self-contained JSON object carrying its own Sequence Position, so a response cut off mid-transfer (a dropped
connection, a timeout, or a failure on the server's side) still leaves every fully-received line usable: the client
resumes with `afterSequence` set to the last `sequence` it fully read, without losing events it already has. A single
JSON array offers no such recovery: a response cut short mid-array is invalid JSON, and the whole page is lost. NDJSON
also lets the server write each row straight to the connection as it comes out of SQLite, without holding the whole
page in memory first.

Every line but the last is one matching event, in ascending Sequence Position order. The last line is always a
trailer carrying `hasMore`:

```
{"sequence":12346,"time":"2026-09-01T14:23:05.123456Z","type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}
{"sequence":12347,"time":"2026-09-01T14:23:07.981234Z","type":"user-updated","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}
{"hasMore":true}
```

The trailer comes last, not first, because writing it means fetching every row of the page first: putting it last is
what lets the server stream each event as it's scanned instead of buffering the whole page to learn `hasMore` before
sending anything. The cost is that a failure partway through a page can no longer turn into a clean error response:
the status code and the first event lines are already on the wire before the failure happens. A client tells the
trailer apart from an event line by shape (a `hasMore` key, not a `sequence` key), not by position, since it's only
known to be last once the stream ends. A response that ends without one was cut short partway through: a client
treats that exactly like a dropped connection, resuming with `afterSequence` set to the last event line it fully read.

`identifiers` and `metadata` come back in the same compact object shape used when appending (`{"courseId": ["foo",
"bar"]}`), grouping multiple values for the same name under one key.

`time` is when TamarackDB appended the event, read from the server's clock during the `POST /events` call, in ATOM
format (RFC 3339) with exactly 6 fractional digits, always in UTC (`Z`). Every event of one `POST /events` call shares
the same `time`: order within a call comes from Sequence Position. The store has no timezone setting: `time` is a
reference value, not something meant for display. Converting to local time is left to the application.

`payload` is an opaque string: the store never parses or checks it. Its real format (JSON, XML, or anything else) is
a convention owned by the writing application, based on the event's `type`. The store has no notion of it.

Response compression (gzip, negotiated the normal way through `Accept-Encoding`, above a minimum body size) is a
nice-to-have, not built yet. Request compression isn't planned at all: appending a large batch of events at once isn't
a `POST /events` use case.

### Appending events

`POST /events` appends events inside the transaction. The request body carries the events and an optional Append
Condition:

```json
{
  "events": [
    {
      "type": "user-created",
      "identifiers": { "userId": "123" },
      "metadata": { "tenantId": "acme" },
      "payload": "..."
    }
  ],
  "condition": {
    "failIfEventsMatch": [ ... ],
    "afterSequence": 12345
  }
}
```

`condition.failIfEventsMatch` follows the same grammar as `query` on `QUERY /events` (an array of `QueryItem`, or
`"*"`). `condition` itself is optional: an event with nothing to protect can be appended with no concurrency check at
all. The condition is checked inside the transaction, against every event visible to it, including the ones appended
earlier in the same transaction (see Append Condition and concurrency).

A call needs at least one event, and may carry at most **100 events**. This is a fixed limit, not configuration, since
it marks an architectural boundary, not a performance trade-off: a Decision Model appends the handful of events from
one business decision, not a batch. A call over this limit gets `400 Bad Request`. A transaction can make several
`POST /events` calls: the limit applies to each call.

On success, the server responds `200 OK`. The body gives the Sequence Position and `time` of each event, in the order
they were sent:

```json
{
  "events": [
    {"sequence": 12348, "time": "2026-09-01T14:25:00.000000Z"}
  ]
}
```

These values are final as soon as the call returns, even though nothing is committed yet: the transaction either
commits them as they are, or rolls them back entirely. Event handlers can use them right away. A projection can store
an event's `sequence` or `time` in a document, and a rebuild reads the same values back from `QUERY /events` (see
Documents).

If the Append Condition fails, the server responds `409 Conflict`, and the transaction is rolled back:

```json
{ "error": "ConcurrencyException" }
```

A malformed request gets `400 Bad Request`. An event over its size limit gets `413 Payload Too Large` (see Event size
limit and Error responses). Both roll the transaction back too (see Calls inside a transaction).

### Documents

A document is a projection: an opaque payload identified by `type` + `id`, with no history. Unlike an event, a
document can be overwritten or removed; the store only ever holds its current state. The document mechanism is
optional: an application that keeps its projections elsewhere never has to touch it.

Documents live in the same SQLite file as events (see Storage: SQLite), and are written in the same transaction. A
commit makes events and documents durable together; a rollback discards both.

**Reading a document**: `GET /documents/{type}/{id}` returns `404 DocumentNotFound` if no document exists for that
`type` + `id`, and `200` otherwise, with the payload as the response body, exactly as written. There's no JSON
envelope around it, since the payload's own format (JSON, XML, plain text) is up to the writing application, not
something the store imposes a wrapper on top of.

With a ticket, the read runs inside the transaction: it sees documents written earlier in the same transaction. A
projector uses it to read a document before changing it. Without a ticket, the read runs on the read connection pool
and sees committed documents only: this is how an application reads a projection to display a page.

A document is always read by `type` and `id`. There is no query over documents.

**Writing documents**: `POST /documents` writes or deletes several documents at once:

```json
{
  "documents": [
    { "type": "user-profile", "id": "123", "payload": "..." },
    { "type": "user-list-entry", "id": "456", "payload": null }
  ]
}
```

A document with a `payload` is created, or replaced if it exists. A document with a `null` payload is deleted;
deleting a document that doesn't exist does nothing. The same `type` + `id` can't appear twice in one call: that's a
`400 Bad Request`. A call may carry at most `maxDocumentsPerWrite` documents, and each payload at most
`maxDocumentSize` bytes (see Configuration). On success, it responds `204 No Content`.

There's no version and no concurrency check on documents. None is needed: a projector reads a document inside the
transaction, changes it, and writes it back, while the transaction holds the write lock. Nothing else can change the
document in between.

The recommended use is one `POST /documents` call per transaction, right before `POST /commit`, carrying every
document the event handlers changed (typically about ten). The client collects the changes while the handlers run,
instead of sending each small change as it happens. The server doesn't enforce this: several calls in one transaction
remain valid.

`POST /documents` without a ticket is accepted only while the server is paused, for projection rebuilds. Each such
call commits on its own. Outside a pause, it gets `409 NotPaused` (see Pause).

**Deleting documents in bulk**: `DELETE /documents/{type}` deletes every document of one type, and `DELETE /documents`
deletes every document. Both take no ticket, are accepted only while the server is paused, and respond `204 No
Content`. Outside a pause, they get `409 NotPaused`.

**What a document may depend on.** Events are appended before the event handlers run, and `POST /events` returns each
event's `sequence` and `time`. A document can depend on anything in an event, those two values included. A rebuild
reads the same events back from `QUERY /events`, with the same values, so it produces the same documents.

### Pause

A pause stops the server from giving out tickets. It guarantees that no transaction is active while a projection
rebuild runs.

**`POST /pause`** joins the FIFO like a `POST /begin` request: it's subject to the same maximum wait, and a
client that disconnects while waiting leaves the FIFO, with no pause. When its turn comes, it opens no SQLite
transaction. It writes the pause file, switches the server to paused, gives its turn to the next request in the FIFO,
and responds `204 No Content`. The response therefore arrives only once every transaction ahead of it has ended.
Calling `POST /pause` while already paused also responds `204 No Content`.

**Effect on the FIFO.** The FIFO keeps moving while the server is paused. When a request reaches the head of the FIFO
while paused, it gets `503 Paused` instead of a ticket, and the next request moves up. This single check covers both
requests queued behind `POST /pause` and requests that arrive during the pause: nothing needs to empty the FIFO.

**Calls accepted only while paused**: `POST /documents` without a ticket, `DELETE /documents/{type}`, and `DELETE
/documents`. Outside a pause, they get `409 NotPaused`: the server isn't in the state the call requires. A `503` would
suggest a temporary outage, and invite the client to retry for nothing. Reads without a ticket (`QUERY /events`, `GET
/documents/{type}/{id}`) are accepted at all times.

**`POST /resume`** deletes the pause file, then switches the server back to giving out tickets. It doesn't join the
FIFO: while paused, the FIFO empties itself as each waiting request gets its `503`. It responds `204 No Content`,
whether the server was paused or not.

**The pause survives a restart.** The pause file, `tamarackdb.paused` in `dataDir`, exists exactly while the server is
paused. On startup, TamarackDB starts paused if the file is there. A crash or restart during a rebuild leaves the
server paused, so the application can't write on half-rebuilt projections: it stays blocked until the rebuild is run
again and `POST /resume` is called. The file is written before `POST /pause` responds, and deleted before `POST
/resume` switches the server back, so a crash between the two steps never leaves the server running unpaused when it
should be paused. A forgotten pause file blocks the application: that shows at once (every `POST /begin` gets
`503 Paused`), and `/health` and `/debug` report it (see Management / observability).

### Projection rebuilds

A projection rebuild runs while the server is paused, outside any transaction:

1. `POST /pause`.
2. `DELETE /documents/{type}` for each type to rebuild, or `DELETE /documents` for all of them.
3. Page through `QUERY /events` without a ticket, replaying each page through the projections' own logic.
4. Write the rebuilt documents with `POST /documents` without a ticket.
5. `POST /resume`.

The application is fully down during a rebuild: nothing else writes. A rebuild has no atomicity as a whole: each
`POST /documents` call commits on its own. A rebuild that fails, or that a TamarackDB crash interrupts, leaves partial
projections behind, and is simply run again. The server stays paused in the meantime.

Reads during a rebuild page through `limit`-sized requests instead of one response streaming the whole result set
over one long connection. Holding one SQLite read transaction open for a whole large rebuild would pin one MVCC
snapshot in place for as long as the rebuild runs, blocking WAL checkpointing that whole time while the rebuild's own
document writes keep piling up in the WAL file. Paging keeps each read transaction short, so the WAL checkpoints
normally between pages.

Fetch time isn't always small next to processing time: some projections process events fast enough that the per-page
round trip becomes a real, if still secondary, share of total rebuild time. This is exactly why the page size and its
ceiling are configuration, not a fixed constant: a slow projection can use a small `limit` and pay almost nothing for
it, while a fast one benefits from a larger `limit` that spreads the round-trip cost over more events per page.

A `VACUUM`, to reclaim the space of deleted documents, also runs while the server is paused (see Storage: SQLite).

### Reset (dev mode)

`POST /reset` deletes every event and every document, and sets the Sequence Position counter back to zero: the next
event appended gets sequence 1. It exists only when `devMode` is on (see Dev mode).

It's meant for local development, where only the developer uses the application, and it doesn't wait for anyone. It
doesn't join the FIFO. If a transaction is active, its ticket stops being active and its SQLite transaction is rolled
back, then all data is deleted, and the FIFO moves on. The only thing `POST /reset` waits for is a call already
running on the write connection, so that the two never use the connection at the same time. The client whose
transaction was cut off gets `410 TransactionNotActive` on its next call. Requests waiting in the FIFO stay there, and
get their ticket on an empty store. `POST /reset` leaves the pause state as it is. It responds `204 No Content`.

### Event size limit

A single event may not be bigger than **64 KiB**, measured as the combined UTF-8 byte length of its `type`,
`identifiers`, `metadata`, and `payload`, not a character count. Multi-byte characters (accented text, for instance)
count for more than one byte each. A `POST /events` call carrying an event over this limit gets `413 Payload Too
Large`.

The limit is deliberate, not a technical ceiling to raise later: it keeps an event a short, meaningful statement about
the world, rather than a data transport container, and keeps Decision Model replay fast (which can reload hundreds of
thousands of events). Larger content (files, documents) belongs in external storage, referenced from the event
instead of embedded in it.

Real-world event sizes stay well under the limit, with room to spare over real usage, rather than the limit being a
ceiling anything currently pushes against.

The default of 64 KiB is configurable (see Configuration).

### Error responses

Every error response uses the same JSON envelope:

```json
{ "error": "InvalidRequest", "message": "afterSequence must be a non-negative integer" }
```

`error` is a stable code a client can check. `message` is a human-readable detail, included when it helps figure out
the problem, left out when it wouldn't add anything (as with `ConcurrencyException`).

| Status | `error` | When |
|---|---|---|
| `400` | `InvalidRequest` | Malformed or invalid body, see below |
| `404` | `DocumentNotFound` | `GET /documents/{type}/{id}` for a document that doesn't exist |
| `409` | `ConcurrencyException` | The Append Condition of a `POST /events` call failed |
| `409` | `NotPaused` | A call accepted only while paused, made outside a pause |
| `410` | `TransactionNotActive` | The ticket is unknown, or its transaction has already ended |
| `413` | `PayloadTooLarge` | An event or a document payload over its size limit |
| `500` | `InternalError` | An unexpected server-side failure |
| `503` | `TransactionQueueFull` | `POST /begin` or `POST /pause` while the FIFO is at its configured depth |
| `503` | `TransactionWaitTimeout` | A request waited in the FIFO longer than the configured maximum, with `Retry-After` |
| `503` | `Paused` | A request reached the head of the FIFO while the server is paused |

Inside a transaction, every error except `404 DocumentNotFound` rolls the transaction back (see Calls inside a
transaction).

`QUERY /events`, `POST /events`, and `POST /documents` respond `400 Bad Request` for any malformed or invalid body:
invalid JSON, a `query` / `condition.failIfEventsMatch` that isn't an array of `QueryItem` or `"*"`, an empty array
anywhere the Query grammar needs a non-empty one (see Query grammar), a non-integer `afterSequence` or `limit`, a
`limit` above the configured maximum (see Pagination), an invalid `time.from` / `time.before` timestamp, an event
missing its `type`, an event carrying a duplicate identifier or metadata value, more than 20 identifiers/metadata
entries (see Metadata), a `POST /events` call with no event or more than 100 events (see Appending events), a
`POST /documents` call with a duplicate `type` + `id` or more than `maxDocumentsPerWrite` documents, and so on.

Validation is hand-written in Go, not driven by a JSON Schema: the request surface is small, several rules are about
meaning rather than pure structure (a valid ATOM timestamp, a consistent `time.from`/`time.before` range, the full DCB
`QueryItem` grammar), and a generic schema validator's error messages don't map cleanly onto the `{error, message}`
shape above.

## Append Condition and concurrency

Standard DCB flow:
1. `read(query)`: read the relevant events, keep the last `sequence` read (`afterSequence`)
2. Decide on the new events to append (Decision Model)
3. `append(events, condition: {failIfEventsMatch: query, afterSequence})`
4. The operation fails if an event matching `query` exists after `afterSequence`

TamarackDB supports two ways to run this flow.

**Inside a transaction.** Steps 1 to 3 all carry the same ticket. The transaction holds the write lock from the start,
so no other client can append between the read and the append. The Append Condition can only fail because of events
appended earlier in the same transaction: by another model, or by an event handler reacting to them. Each model can
make its own `POST /events` call with its own condition: the transaction, not a merged condition, is what makes the
whole command atomic.

**Optimistic.** Step 1 runs without a ticket, outside any transaction, on committed data. The client decides, then
opens a transaction only for step 3, with `afterSequence` set to the last Sequence Position it read. The write lock is
held only for the append itself. If another transaction appended a matching event in between, the condition fails
with `409 ConcurrencyException`, and the client can read again and retry.

The Append Condition is checked in the same SQLite transaction as the insert, against every event visible to it.
Only one transaction is ever active (see Concurrency handling in Go), so nothing can slip in between the check and
the insert.

### DCB compliance

TamarackDB follows the [DCB specification](https://dcb.events/specification/):

| Requirement | Level | TamarackDB |
|---|---|---|
| Read events filtered by type and/or tags through a Query | MUST | `QUERY /events` |
| Read from a given Sequence Position | SHOULD | `afterSequence` on `QUERY /events` |
| Append one or more events atomically | MUST | Every `POST /events` of a transaction commits atomically |
| Fail the append if an event matches the Append Condition, when one is given | MUST | `condition` on `POST /events`, optional for the client |

DCB relies on a dynamic consistency boundary, defined by a Query, with optimistic concurrency control. Inside a
transaction, TamarackDB uses the widest boundary possible, the whole store, with a pessimistic lock. The guarantee is
stronger, not weaker. The Append Condition stays fully supported, for the optimistic flow above and for any client
that relies on it.

The Append Condition doesn't guard against another connection to the SQLite file. It doesn't need to:
`BEGIN IMMEDIATE` blocks any outside write while a transaction is active, and outside writes aren't supported anyway,
since the Sequence Position counter lives in TamarackDB's memory (see Concurrency handling in Go).

## Concurrency handling in Go

### Principle: the transaction FIFO

A queue manager gives out the single active turn, strictly in the order requests arrive. Two kinds of requests join
its FIFO: `POST /begin` and `POST /pause`. It knows nothing about what a transaction will read or append. Only
two states exist: **Active** (at most one transaction at a time, the only one allowed to touch the write connection)
and **Queued** (every other request, waiting its turn in line).

**Flow for a request in the FIFO:**
1. The HTTP handler asks the queue manager to join the line.
2. If the FIFO is already at its configured depth (see Configuration), the request is turned away right away with
   `503 TransactionQueueFull`, instead of joining.
3. Otherwise it waits, with its HTTP connection held open, until every request ahead of it is done. If the client
   disconnects, the request leaves the line right away, and everyone behind it moves up one spot. If it waits longer
   than the configured maximum, it leaves the line with `503 TransactionWaitTimeout`.
4. When it reaches the head of the line:
   - If the server is paused, it gets `503 Paused`, and the next request moves up.
   - If it's a `POST /pause`, the server writes the pause file, switches to paused, and the next request moves up.
   - If it's a `POST /begin`, the handler runs `BEGIN IMMEDIATE` on the write connection, creates a ticket,
     sets the transaction's deadline and ceiling, and responds with the ticket.
5. The transaction stays active after the `POST /begin` response: the queue manager holds it in memory, keyed
   by its ticket, until it ends (see Ending a transaction). Then the next request moves up.

**The active transaction outlives any single request.** Every call with a ticket looks up the active transaction.
A ticket that doesn't match it gets `410 TransactionNotActive`. Calls with the same ticket run one at a time: a mutex
guards the write connection, and a second call made in parallel waits for the first to finish. The mutex is also what
the deadline timer and `POST /reset` wait on, so nothing ever uses the write connection at the same time as a call.

**Deadline.** A timer tracks the active transaction's deadline and ceiling. When it fires, it takes the mutex, which
waits for a call already running to finish, then rolls the transaction back and gives the turn to the next request.
A call that was already running when the deadline passed finishes normally; if it was `POST /commit`, the commit
wins. The next call with that ticket gets `410 TransactionNotActive`. Every call with the ticket resets the timer
when it ends, up to the ceiling.

**A failed call ends the transaction.** A call that returns an error (other than `404 DocumentNotFound`), panics, or
whose client disconnects before it finishes, rolls the transaction back and gives the turn to the next request. The
rollback runs in a deferred call, set up before the handler touches the write connection, so it runs no matter how
the handler exits. A transaction never stays active because of a failure.

**No conflict checking.** The queue manager never looks at a transaction's queries, conditions, or events. Every
request joins the line purely by arrival order: there's no conflict matrix, and no split between conflicting and
non-conflicting transactions. Two transactions touching unrelated events still run one after the other. This is the
scope's trade-off (see Scope): a simpler model, at the cost of throughput.

### Application-controlled Sequence Position

Since only one transaction ever touches the write connection, TamarackDB assigns the Sequence Position itself, in
memory, instead of leaving it to SQLite's `AUTOINCREMENT`.

`events.sequence` is a plain `INTEGER PRIMARY KEY`, with the value set by the application on insert (see Schema).
Before giving out any ticket (reads without a ticket are unaffected, and can start right away), the process reads the
current highest `sequence` in the `events` table, and keeps it in memory as the next-sequence counter. An empty table
starts the counter the same way `AUTOINCREMENT` would: the first event gets sequence 1.

**The counter follows the transaction.** When a transaction starts, the queue manager saves the counter's value. Each
`POST /events` call moves the counter forward only once its Append Condition has been checked and holds, never
before. On commit, the counter keeps its new value. On rollback, for any reason, it goes back to the saved value.
Events appended inside a transaction get their final Sequence Position right away, which is what lets a `QUERY
/events` with the same ticket see them, and lets `POST /events` return them. A rolled-back transaction leaves no gap
in the sequence, since the counter goes back to where it was. `POST /reset` sets the counter back to zero.

Knowing every event's sequence up front means a call's `events` rows can be written as one multi-row `INSERT`,
followed by one multi-row `INSERT` into `identifiers` and one into `metadata`, instead of a per-event round trip to
fetch an ID between each event and its tags. `PRAGMA foreign_keys = ON` is still checked right away, not deferred to
commit, so a row in `identifiers` or `metadata` still can't point to an `event_sequence` that doesn't exist yet in
`events`, within the same transaction.

**Skipping the conflict check when nothing was appended since the read.** If a condition's `afterSequence` equals the
counter's last-assigned value, no event exists past that point at all, committed or appended earlier in the same
transaction. `failIfEventsMatch` can't match anything, whatever it is, so the SELECT that would otherwise check it can
be skipped entirely. A bare `afterSequence` condition (no `failIfEventsMatch`) never needs a SELECT at all: "does any
event exist after `afterSequence`" can be answered directly from the counter. This is purely an internal shortcut: it
changes how the decision is reached, never the decision itself, or anything in the HTTP contract.

### Reads

Reads take one of two paths, depending on the ticket.

**Without a ticket**, a read doesn't go through the queue manager. It runs on the read connection pool. Consistency
comes from SQLite's **MVCC** mode (WAL): a read sees a steady snapshot of committed data from the moment it starts,
and never sees an active transaction's changes. It's never blocked by the active transaction, however long that one
runs. A read that starts just before a commit simply won't see the new events, which is fine: a client that then
appends with an Append Condition uses the Sequence Position it actually read as `afterSequence`.

**With a ticket**, a read is a call inside the transaction, like any other: it runs on the write connection, inside
the SQLite transaction, under the mutex. It sees everything the transaction has appended or written so far, plus
everything committed before the transaction started.

**Write atomicity:** appending an event touches several tables (the `events` row, one or more identifier rows, one or
more metadata rows). All of it happens inside the transaction's single SQLite transaction, so readers without a ticket
see an event, its identifiers, and its metadata together, and only once committed.

### Startup, shutdown, and crash behavior

On startup, before opening the store, the process prints a banner and its resolved configuration to stdout: bind
address, port, the TLS and auth flags and file paths (`authToken` itself is never printed), data directory, dev mode,
the transaction timeouts, and the pagination/event-size/queue-depth limits. This is a plain operational aid, for
checking at a glance what a given instance is actually set up to do, not a machine-readable format meant for parsing.

Opening the store checks `PRAGMA user_version` against the schema version built into the binary, then reads the
current highest `sequence` in the `events` table into the in-memory Sequence Position counter (see
Application-controlled Sequence Position above). The process then checks for the pause file in `dataDir`, and
starts paused if it
exists (see Pause). All of this finishes before the process gives out any ticket; reads without a ticket can be
served as soon as the store is open.

On `SIGINT` or `SIGTERM`, the process shuts down in order: the HTTP server stops taking new connections and finishes
requests already in flight (`http.Server.Shutdown`, capped at 10 seconds), requests still waiting in the FIFO are
turned away, the active transaction, if any, is rolled back, the queue manager closes, then the SQLite store closes,
releasing its connections and the `.lock` file. A transaction is never committed on shutdown: only its client can
decide to commit. A fatal storage error found mid-flight (see Fatal storage errors below) drives this same ordered
shutdown, instead of an abrupt exit. The HTTP server also sets `ReadHeaderTimeout` to 10 seconds, closing a connection
that never finishes sending its request headers, instead of holding it open forever.

The queue manager's state (the active transaction, its ticket and deadline, the requests waiting) is purely
transient, held only in memory for the life of the process. Nothing is saved, and nothing needs to be rebuilt on
startup: a freshly started process begins with an empty FIFO and no active transaction, which is correct, since every
client that existed before a crash lost its connection or its ticket too. Two pieces of state are rebuilt on startup,
because they have to match what's on disk: the Sequence Position counter, read from the database, and the pause
state, read from the pause file.

A panic in one request handler is caught by the HTTP server, without crashing the process. The handler's deferred
rollback and release still run during the panic's unwind (see A failed call ends the transaction), so no transaction
stays active forever. A full process crash (an unrecovered panic, SIGKILL, an out-of-memory kill) takes the whole
in-memory state down with it, so there's nothing left to leak either way.

SQLite's own atomicity guarantees the store itself: a crash during an active transaction leaves an uncommitted WAL
transaction, discarded the next time a connection opens. Neither the transaction's events nor its documents ever
become visible, in whole or in part. The counter, read back from the database, matches.

None of this tells a client whether its commit actually happened, when the crash (or any dropped connection) lands
after SQLite commits but before the `POST /commit` response reaches it (see Ending a transaction). A client that wants
to retry such a command safely can append with an Append Condition, even one with no `failIfEventsMatch`: an
`afterSequence` on its own is enough. If the original commit went through, the Sequence Position has already moved
past it, so the retry fails with `409 ConcurrencyException` instead of appending a duplicate event.

**Fatal storage errors:** a SQLite error that suggests the file itself may be damaged (an I/O error, detected
corruption, failure to open the database file) is treated as fatal: the process logs it and exits, instead of trying
to keep serving requests against a store it can no longer trust. This is deliberately simple: no per-error recovery
logic, just a clean restart, which is cheap and safe given the transient state described above, and which `/health`
and a process supervisor are already set up to catch and act on. A temporary, non-fatal SQLite error (a busy lock
during a WAL checkpoint, say) doesn't count as fatal: it fails that one call, which rolls its transaction back (see A
failed call ends the transaction), instead of taking the whole process down for every other client.

## Storage: SQLite

SQLite is used as the storage engine, for these reasons:
- Volume (several million rows across the identifiers and metadata tables) fits comfortably within SQLite's limits,
  with `name + value` indexes
- No external network or process to depend on, in line with the goal of keeping state centralized in memory on the Go
  side
- Single-writer behavior, in line with the single-process model ("single brain"): the process is the only writer for
  as long as it runs
- WAL mode allows reads without a ticket to happen at the same time as the active transaction, without blocking
- A plain, inspectable file format: the database can be opened and queried with ordinary SQLite tools, not some closed
  format, and backed up the same way, through SQLite's own backup tools (for example `.backup`, `VACUUM INTO`) instead
  of a raw copy of the file, which can miss commits still sitting in the WAL

Events and documents live in one file, `tamarackdb.sqlite`, so one SQLite transaction covers both. Documents are
larger than events and are rewritten in place, so they make the WAL grow faster than events alone would. This stays
small in practice: only one transaction writes at a time anyway, and a command writes about ten documents, once,
right before its commit (see Documents). The one heavy case is a projection rebuild, which writes every rebuilt
document, but it runs while the server is paused, with no other writer (see Projection rebuilds). A `VACUUM` to
reclaim the space of deleted documents also covers the events, and runs while the server is paused too.

`tamarackdb-backup` only ever copies events (see [Backup](/docs/guides/backup/)): a document is reproducible from
events, so it doesn't need its own backup copy. A raw copy of the database file, by contrast, includes the documents.

A file-level copy is not the only way to back up an instance. `tamarackdb-backup` reads events over `QUERY /events`
from a live source and writes them into a local file through `Store.Import`, a variant of `Append` that skips sequence
reservation and the append-condition check, since the sequences it receives are already assigned by the source. The
result is a plain SQLite file built through the same `store.Open` schema path used everywhere else, so unlike a raw
file copy, it can be opened and served as a live instance in its own right.

**Enforcing single-writer at the OS level:** `writeDB.SetMaxOpenConns(1)`, WAL, and `_busy_timeout` only keep writes
in order *inside* one process: nothing stops a second `tamarackdb-server` process from opening the same database file
and racing the first. `store.Open` closes that gap directly: before touching the SQLite file, it takes an exclusive,
non-blocking `flock(2)` on a sibling `<path>.lock` file, and holds it for the life of the process. It's held open, not
just briefly grabbed, so the OS releases it automatically on exit or crash: no leftover lock file can ever block a
later start. A second process finds the lock already held, and fails fast at startup with `ErrDatabaseLocked`,
instead of silently corrupting state or fighting the first process over `SQLITE_BUSY`. This relies on `flock(2)`;
TamarackDB only targets Linux. Docker covers every other platform.

### Schema

```sql
PRAGMA user_version = 1;

CREATE TABLE events (
    sequence    INTEGER PRIMARY KEY,
    time        TEXT NOT NULL,
    type        TEXT NOT NULL,
    payload     TEXT NOT NULL,
    identifiers TEXT NOT NULL,
    metadata    TEXT NOT NULL
);

CREATE INDEX idx_events_time ON events(time);
CREATE INDEX idx_events_type ON events(type);

CREATE TABLE identifiers (
    event_sequence INTEGER NOT NULL REFERENCES events(sequence),
    name           TEXT NOT NULL,
    value          TEXT NOT NULL,
    PRIMARY KEY (event_sequence, name, value)
) WITHOUT ROWID;

CREATE INDEX idx_identifiers_name_value ON identifiers(name, value, event_sequence);

CREATE TABLE metadata (
    event_sequence INTEGER NOT NULL REFERENCES events(sequence),
    name           TEXT NOT NULL,
    value          TEXT NOT NULL,
    PRIMARY KEY (event_sequence, name, value)
) WITHOUT ROWID;

CREATE INDEX idx_metadata_name_value ON metadata(name, value, event_sequence);

CREATE TABLE documents (
    type    TEXT NOT NULL,
    id      TEXT NOT NULL,
    payload TEXT NOT NULL,
    PRIMARY KEY (type, id)
) WITHOUT ROWID;
```

`documents` is `WITHOUT ROWID`, keyed by `(type, id)`: a document has no history, so its natural key is also its only
key, with no separate rowid needed. The same key serves `DELETE /documents/{type}`, as a prefix of the primary key.

`time` is stored as `TEXT`, not as an integer timestamp. Its fixed-width UTC format sorts the same way alphabetically
as it does chronologically, so the `time` index serves the range filter directly, and nothing needs to be converted
between what's stored and what's returned: a read passes the stored text straight through. This only holds because
the format is strict: an offset, or a different number of fractional digits, would break the ordering (`05.123Z`
sorts after `05.123456Z`, since `Z` comes after every digit). The server always writes `time` itself, in that exact
format.

`identifiers` and `metadata` are `WITHOUT ROWID` tables, keyed by their natural combined primary key
`(event_sequence, name, value)`: these are pure link rows, so a separate rowid would just be an extra, unneeded
btree. The secondary index `(name, value, event_sequence)` on each table is what serves the DCB matching check
directly, with `event_sequence` included so the index alone can answer the scan.

`events.identifiers` and `events.metadata` hold the same data again, in the compact object shape the HTTP API returns
(see Response format): a ready-made copy a read can hand back directly, without joining out to the
`identifiers`/`metadata` tables. A read hands these two columns to the client exactly as stored: it never decodes them
into Go values and re-encodes them, since the stored bytes already are the response bytes. Those tables stay what the
DCB matching check and a read's own filtering use, keyed for lookup by `name`/`value`; the columns on `events` are
keyed by nothing but the event itself, meant for handing the whole set back at once. A column and a table can share a
name in SQLite without conflict: `events.identifiers` names the column, a bare `identifiers` in a `FROM` clause names
the table.

The `events(sequence)` foreign key on both tables is enforced by turning on `PRAGMA foreign_keys = ON` on every
connection at startup: SQLite reads foreign key declarations, but doesn't enforce them by default. Turning this on
catches implementation bugs (say, an identifier or metadata row written with an `event_sequence` that doesn't match a
real event), rather than serving any real functional need, since events are append-only with a single writer.

Two more pragmas are set on every connection at startup, next to `foreign_keys`: `PRAGMA journal_mode = WAL` (the
mode this design assumes throughout, for MVCC reads and checkpoint behavior) and `PRAGMA synchronous = FULL`. `FULL`
costs one extra fsync per commit compared to the `NORMAL` mode WAL usually pairs with, but at this scope's write
volume that cost doesn't matter, and it buys the strongest durability SQLite offers, for what is, for each
application, its single source of truth with no backup copy running behind it.

The write connection opens every transaction with `BEGIN IMMEDIATE` (`_txlock=immediate` in the DSN), taking SQLite's
write lock at the start of the transaction, rather than waiting until the first write statement runs. For a
transaction opened by `POST /begin`, that's the moment the ticket is given out: reads, checks, and inserts all
run under the lock, with no window where another connection could slip in between them. A call accepted only while
paused (`POST /documents` without a ticket, `DELETE /documents`) runs its own short `BEGIN IMMEDIATE` ... `COMMIT`,
under the same mutex as any call (see Principle: the transaction FIFO). The write connection also sets
`_busy_timeout = 5000` (five seconds). Since `writeDB.SetMaxOpenConns(1)` already forces every write onto one
connection, and the FIFO already lets only one transaction run at a time, the busy timeout only guards against
something else briefly holding the file (a passive checkpoint, an external `sqlite3` shell), not against another
transaction.

Checkpointing relies on SQLite's own automatic passive checkpoint (triggered on its own once the WAL crosses its
default size, without blocking any reader or writer), instead of a separate checkpoint goroutine or schedule. Two
things can hold it back: a long read without a ticket, which pins an MVCC snapshot (bounded by pagination, see
Projection rebuilds), and a long transaction, whose changes can't be checkpointed before it commits (bounded by the
transaction ceiling, see Deadline and ceiling). Nothing about the checkpoint itself needs to be triggered by hand.

Query planner statistics are kept up to date the same hands-off way: once an hour, the process runs `PRAGMA optimize`
on the write connection. It joins the FIFO like a transaction, so it never runs inside a client's transaction. `events`
only grows, for the life of a deployment that's never restarted, so statistics gathered once at some point in the past
drift further from reality the longer the process stays up. `PRAGMA optimize` is SQLite's own answer to this: cheap
enough to run often, since it only re-analyzes tables it judges to have changed enough to matter (or that have no
statistics at all yet), rather than scanning everything the way a plain `ANALYZE` does. A full `ANALYZE` is never run
automatically; it's still the right tool right after a one-off bulk import, run by hand while the server is stopped.

On startup, the process reads `PRAGMA user_version` and checks it against the schema version built into the binary.
A database file that doesn't exist yet is created fresh, with the schema above setting it at the current version. An
existing file whose version doesn't match (older, from a schema that's since changed, or newer, from a downgraded
binary) is fatal: the process logs it and refuses to start, the same treatment as any other storage integrity failure
(see Startup, shutdown, and crash behavior).

Moving an existing database from one schema version to the next is the job of a separate binary, not the TamarackDB
process itself: a dedicated migration tool, run once, on purpose, between a schema change and the next deployment of
the main binary. The TamarackDB server never changes its own schema.

## Configuration

TamarackDB's startup configuration (socket path or bind address/port, TLS settings, auth token, data directory,
transaction timeouts, pagination/size limits, and the FIFO depth) comes from three sources, in this order:

1. A TOML configuration file, passed via `--config` (defaults to `config.toml` in the working directory). Keys live
   under a `[server]` section, so the same file can also hold `tamarackdb-backup`'s `[backup]` section (see
   [Backup](/docs/guides/backup/)); each binary reads only its own section.
2. `TAMARACKDB_*` environment variables, one per configuration key.
3. Built-in defaults, for the keys that have one (`socketPath`, `dataDir`, `logLevel`, `defaultLimit`, `maxLimit`,
   `maxEventSize`, `maxDocumentSize`, `maxDocumentsPerWrite`, `transactionTimeout`, `transactionCeiling`,
   `maxTransactionWait`, `maxQueuedTransactions`, `readPoolSize`).

A value set in the configuration file always wins over the matching environment variable. The configuration file
itself is optional: an application deployed as one instance per environment, each with its own file, uses it as the
single source of truth. A container deployment with no file at all is set up entirely through the environment
instead. Both paths produce the same `Config`, and every field is checked the same way regardless of where it came
from.

| Key | Environment variable | Default |
|---|---|---|
| `socketPath` | `TAMARACKDB_SOCKET_PATH` | `/var/run/tamarackdb-server.sock` |
| `bindAddress` | `TAMARACKDB_BIND_ADDRESS` | `127.0.0.1` |
| `port` | `TAMARACKDB_PORT` | `8085` |
| `enableTls` | `TAMARACKDB_ENABLE_TLS` | `false` |
| `tlsCertFile` | `TAMARACKDB_TLS_CERT_FILE` | |
| `tlsKeyFile` | `TAMARACKDB_TLS_KEY_FILE` | |
| `enableAuth` | `TAMARACKDB_ENABLE_AUTH` | `false` |
| `authToken` | `TAMARACKDB_AUTH_TOKEN` | |
| `dataDir` | `TAMARACKDB_DATA_DIR` | `data` |
| `logLevel` | `TAMARACKDB_LOG_LEVEL` | `warning` |
| `devMode` | `TAMARACKDB_DEV_MODE` | `false` |
| `defaultLimit` | `TAMARACKDB_DEFAULT_LIMIT` | `1000` |
| `maxLimit` | `TAMARACKDB_MAX_LIMIT` | `10000` |
| `maxEventSize` | `TAMARACKDB_MAX_EVENT_SIZE` | `65536` (64 KiB) |
| `maxDocumentSize` | `TAMARACKDB_MAX_DOCUMENT_SIZE` | `65536` (64 KiB) |
| `maxDocumentsPerWrite` | `TAMARACKDB_MAX_DOCUMENTS_PER_WRITE` | `100` |
| `transactionTimeout` | `TAMARACKDB_TRANSACTION_TIMEOUT` | `5` (seconds) |
| `transactionCeiling` | `TAMARACKDB_TRANSACTION_CEILING` | `15` (seconds) |
| `maxTransactionWait` | `TAMARACKDB_MAX_TRANSACTION_WAIT` | `30` (seconds) |
| `maxQueuedTransactions` | `TAMARACKDB_MAX_QUEUED_TRANSACTIONS` | `100` |
| `readPoolSize` | `TAMARACKDB_READ_POOL_SIZE` | `8` |

`dataDir` is the one directory holding the database file, `tamarackdb.sqlite`, and the pause file,
`tamarackdb.paused` (see Pause). Only the directory is configurable, the same convention MySQL's own `datadir` uses:
the filenames within it are fixed.

`maxDocumentSize` bounds one document's payload the same way `maxEventSize` bounds one event. `maxDocumentsPerWrite`
caps how many documents one `POST /documents` call may carry. Unlike the fixed 100-events-per-call limit, it's
configuration, not an architectural boundary: document volume needs vary more between applications, especially for a
rebuild's calls.

`transactionTimeout` is a transaction's idle timeout: how long it may go without a call before it's rolled back,
counted from the moment its ticket is given out, then from the end of each call made with the ticket.
`transactionCeiling` is the total time no transaction can exceed, however many calls it makes (see Deadline and
ceiling). `transactionTimeout` can't be greater than `transactionCeiling`: `Load` rejects that configuration.

`maxTransactionWait` caps how long a request may wait in the FIFO before getting `503 TransactionWaitTimeout`.
`maxQueuedTransactions` caps how many requests may wait in the FIFO at once. A request that arrives when the FIFO is
already at that depth gets `503 TransactionQueueFull` instead of joining. Neither is "0 means no limit": a FIFO with no
bound would let a burst, or a broken client, pile up an unlimited number of blocked HTTP connections, so every
deployment gets a bound whether it sets one or not. Beyond the defaults, there's no single right value: size them
against how many concurrent users the owning application expects, and how long its commands take. A request waiting
behind `N` transactions may wait up to `N` times `transactionCeiling` in the worst case.

## Security

TLS and Bearer-token checks are each controlled by their own flag, `enableTls` and `enableAuth`, so a deployment can
match its own network's trust level instead of the store forcing one fixed stance. Both default to off, for a
deployment where the network is already isolated further out (a private segment, a VPN, a firewall), which would make
TLS and per-request auth extra weight on top of a trust boundary already enforced elsewhere.

TamarackDB listens on a unix socket by default (`socketPath`), and switches to TCP once `bindAddress` or `port` is set
(see Configuration); `socketPath` wins whenever it's set, even alongside `bindAddress`/`port`. The unix socket is the
recommended setup: TamarackDB runs on the same host as the application, and every transaction costs several round
trips, which a unix socket keeps short. `enableTls` only applies to the TCP path: the Go process handles TLS itself,
via `ServeTLS`, with no reverse proxy in front, and `enableTls` is ignored entirely when `socketPath` is in effect,
since a unix socket never leaves the host. When `enableTls` is off, the process serves plain HTTP on the configured
bind address and port.

When `enableAuth` is on, every registered route needs a Bearer token in the `Authorization` header
(`Authorization: Bearer <token>`): every endpoint of the HTTP API, `/health`, the observability endpoints (`/metrics`,
`/debug`), and, in dev mode, `POST /reset` and the profiling endpoints too. The token is a single fixed value, set as
`authToken`. A request with no valid token gets `401 Unauthorized` before it reaches any handler logic. Rotating the
token means changing the configuration file or environment variable and restarting the process: there's no in-memory
rotation, or window where two tokens both work. When `enableAuth` is off, the API serves every request with no auth
check at all.

One token, with no per-client scope, is enough because a TamarackDB instance has exactly one trusted caller: the
owning application. If that application itself serves many tenants, keeping them apart is its own job, done with the
`tenantId` metadata already carried on events. It's not something TamarackDB's auth layer needs to handle.

A ticket is not a credential. It identifies the active transaction, not the caller: the Bearer token, when on, is what
authenticates each call. A ticket is a random UUID, so a caller can't guess the ticket of a transaction it didn't
open, but it's never exposed anywhere else either, including `/debug` (see Queue and connection pool observability).

### Dev mode

`devMode` (see Configuration) turns on two things, neither reachable otherwise, both meant only for local development
and test environments, never a production instance: `POST /reset`, and Go's standard profiling endpoints under
`/debug/pprof/` (CPU, heap, goroutine, and so on). Neither exists at all unless `devMode` is `true`. Left at its
default of `false`, a request to either gets the stdlib's plain `404`, like any other unregistered path. That keeps
them out of reach in a normal deployment, instead of reachable-but-guarded, matching the "fail loud, keep it simple"
stance used throughout: there's no separate permission or confirmation step once `devMode` is on.

`POST /reset` deletes every event and every document, sets the Sequence Position counter back to zero, and cuts off
the active transaction, if any (see Reset). The schema itself stays in place.

The profiling endpoints are read-only and outside the FIFO: they inspect the running process (CPU samples, memory
allocations, goroutine stacks), not the database, so they carry none of `POST /reset`'s data-loss risk. They're still
dev-mode-only because a CPU or heap profile can reveal details about the data flowing through a live request that a
production deployment shouldn't expose to whoever can reach the port.

## Management / observability features

### Health check

A lightweight `GET /health` endpoint confirms the process is responding and SQLite is reachable (with a trivial
`SELECT 1` on the read pool), for a process supervisor or load balancer to check. Even for a single-instance service, a
health check still helps restart and alerting logic. On success it responds `200 OK` with a small JSON body:

```json
{"status": "ok", "version": "1.2.3", "paused": false}
```

On failure to reach SQLite, it responds `503 Service Unavailable` rather than `500`, the usual signal a supervisor or
load balancer already expects for "not ready right now," different from the `500` an ordinary request failure returns
elsewhere in the API.

A paused server is healthy: `/health` still responds `200 OK`, with `"paused": true`. A supervisor that restarted the
process on a `503` would only restart it paused again (see Pause), in a loop, for the whole length of a rebuild.

### Versioning

The running build's version is a single value, derived from the closest Git tag with
`git describe --tags --always --dirty` and baked into every binary at build time, via
`-ldflags "-X github.com/tamarackdb/tamarackdb/internal/buildinfo.Version=..."`. It's not something a process reads
or reloads while running. Every TamarackDB binary accepts `--version`, printing that value and exiting right away,
before doing anything else (loading a configuration file, opening the store, reaching out over the network). That same
value is what `GET /health` reports in its `version` field above.

### Request logging

Every request logs one line to stdout once its handler finishes: HTTP method, path, resulting status code, response
size in bytes, and how long it took, e.g. `tamarackdb-server: POST /events 200 42B 1.23ms`. This wraps the whole
routed handler, including authentication, so a request turned away with `401 Unauthorized` gets logged just like any
other. `logLevel` sets the minimum severity a line is written at (see Configuration).

A transaction rolled back because it reached its idle timeout or its ceiling has no request of its own to log. The
deadline timer logs one line for it instead, at `warning`: which limit was reached, and how long the transaction
lasted. It's the sign of a client that crashed, hung, or ran a command far longer than it should.

### Queue and connection pool observability

Event and document counts, per-type breakdowns, and database file size (anything you can work out from the store's
own content) are a query away, straight against the SQLite file, so the store doesn't need to expose them itself. What
the file can't answer is live, in-memory state that only exists for the life of the process: the active transaction,
the FIFO, the pause state, and how busy the read and write SQLite connection pools are. Two endpoints cover that, kept
separate since they serve different needs:

**`GET /metrics`**: Prometheus exposition format, for scraping into existing monitoring:
- `tamarackdb_paused` (gauge): whether the server is paused (`1`) or not (`0`)
- `tamarackdb_transaction_active` (gauge): whether a transaction is currently active (`1`) or not (`0`)
- `tamarackdb_requests_queued` (gauge): number of requests (`POST /begin` or `POST /pause`) currently waiting in
  the FIFO
- `tamarackdb_queue_longest_wait_seconds` (gauge): longest current wait, in seconds, among queued requests; `0` when
  the FIFO is empty
- `tamarackdb_transactions_started_total` (counter): total transactions given a ticket since startup
- `tamarackdb_transactions_committed_total` (counter): total transactions committed since startup
- `tamarackdb_transactions_rolled_back_total` (counter, label `reason`): total transactions rolled back since startup,
  by reason: `client` (`POST /rollback`), `error` (a failed call), `expired` (deadline or ceiling), `shutdown`, `reset`
- `tamarackdb_transaction_duration_seconds` (histogram): time from ticket to end, for every transaction, however it
  ended
- `tamarackdb_appends_failed_total` (counter): total `POST /events` calls that failed on their Append Condition
  (`409 ConcurrencyException`) since startup

**`GET /debug`**: a JSON snapshot for digging into one specific stuck or slow transaction, or a read pool that looks
saturated, too detailed to fit a metric:

```json
{
  "time": "2026-09-01T14:23:05.123456Z",
  "paused": null,
  "write": {
    "active": {
      "since": "2026-09-01T14:23:04.900000Z",
      "ageSeconds": 0.223,
      "deadline": "2026-09-01T14:23:09.900000Z",
      "ceiling": "2026-09-01T14:23:19.900000Z",
      "calls": 7
    },
    "queued": [
      {
        "kind": "transaction",
        "queuedAt": "2026-09-01T14:23:05.000000Z",
        "waitSeconds": 0.1
      }
    ],
    "httpOpen": 2,
    "sqliteInUse": 1,
    "sqliteMax": 1
  },
  "read": {
    "httpOpen": 3,
    "sqliteInUse": 3,
    "sqliteMax": 8
  }
}
```

`paused` is `null` when the server isn't paused, and `{"since": "..."}` when it is. After a restart, `since` comes
from the pause file's modification time, so it still reports when the pause actually began.

`write.active` describes the active transaction, if any (`null` otherwise): when its ticket was given out, its current
deadline and fixed ceiling, and how many calls it has made so far. It never carries the ticket itself (see Security),
nor the transaction's queries, conditions, or events: the queue manager never knows them. `write.queued` lists every
request still waiting, oldest first, with its `kind` (`transaction` or `pause`), and `waitSeconds` instead of
`ageSeconds`. `write.queued` is always present, never `null`, even when empty.

`httpOpen` is how many requests are currently in flight on each side: on the write side, requests waiting in the FIFO
plus calls with a ticket; on the read side, reads without a ticket. `sqliteInUse` and `sqliteMax` are the underlying
SQLite connection pool's usage against its configured ceiling (`database/sql`'s own
`DBStats.InUse`/`MaxOpenConnections`, read straight off the read and write `*sql.DB` pools). `write.sqliteMax` is
always `1`: the write pool is deliberately capped at one connection, so SQLite's own driver enforces the same
single-writer guarantee the FIFO already provides at the HTTP layer. `read.httpOpen` can run higher than
`read.sqliteMax` when the read pool is saturated and the extra requests are waiting inside `database/sql` for a free
connection. A sustained gap between the two is a sign that `readPoolSize` (see
[Deployment](/docs/guides/deployment/)) is too small for the traffic.

Since the queue manager serializes access to its own state behind a mutex, separate from the write connection's
mutex, and the SQLite pool stats come straight from `database/sql`'s own counters, answering a `GET /debug` request is
always a quick, non-blocking read: never stuck behind a queued request or a running call. The endpoint is read-only:
nothing about it lets you end a transaction, cancel a read, or otherwise change either pool's state.

## Implementation

The concrete Go code behind the queue manager, the Query-to-SQL translation, the schema migration tool, and the backup
tool live in `internal/queue`, `internal/store`, `cmd/tamarackdb-migrate`, and `cmd/tamarackdb-backup`. The document
wire shape and its validation rules live in `internal/document`, independent of `internal/dcb`.
