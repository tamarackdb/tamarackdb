# TamarackDB Design

## Table of contents

- [Context](#context)
  - [Name](#name)
  - [Scope](#scope)
- [Data model](#data-model)
  - [Identifiers](#identifiers)
  - [Metadata](#metadata)
  - [Two categories of data associated with an event](#two-categories-of-data-associated-with-an-event)
  - [Volume context](#volume-context)
- [Query grammar (per the DCB spec)](#query-grammar-per-the-dcb-spec)
- [HTTP API](#http-api)
  - [Reading events](#reading-events)
  - [Pagination](#pagination)
  - [Projection rebuilds](#projection-rebuilds)
  - [Response format](#response-format)
  - [Appending events](#appending-events)
  - [Event size limit](#event-size-limit)
  - [Error responses](#error-responses)
- [Append Condition and concurrency](#append-condition-and-concurrency)
- [Concurrency handling in Go](#concurrency-handling-in-go)
  - [Principle: the queue manager](#principle-the-queue-manager)
  - [Application-controlled Sequence Position](#application-controlled-sequence-position)
  - [Reads](#reads)
  - [Startup, shutdown, and crash behavior](#startup-shutdown-and-crash-behavior)
- [Storage: SQLite](#storage-sqlite)
  - [Schema](#schema)
- [Configuration](#configuration)
- [Security](#security)
  - [Dev mode](#dev-mode)
- [Management / observability features](#management--observability-features)
  - [Health check](#health-check)
  - [Versioning](#versioning)
  - [Request logging](#request-logging)
  - [Nice to have: queue observability](#nice-to-have-queue-observability)
- [Implementation](#implementation)

## Context

TamarackDB is an event store in Go. It follows the [DCB (Dynamic Consistency Boundaries) specification](https://dcb.events/specification/), is reachable over HTTP, and uses SQLite as its storage engine. The service runs as a single instance ("single brain"), not a multi-instance cluster. Go serializes writes and assigns each event's Sequence Position; the actual DCB matching for the Append Condition runs as a SQL query against SQLite.

Applications can share a single TamarackDB instance when they share events. TamarackDB does not track which application produced an event.

### Name

TamarackDB takes its name from the tamarack (*Larix laricina*), a conifer native to Quebec's boreal forest. It's one of the few conifers used in dendrochronology, because its growth rings are unusually clear and easy to read. Each ring records one season, laid down once and never changed. You can read the tree's whole history by reading the rings from the center out. This event store works the same way: an ordered, append-only list of facts that never change, from which you rebuild current state by replaying them.

### Scope

TamarackDB serves applications with modest throughput and few concurrent writers. Every design choice here, from one SQLite file to one queue manager with no clustering, follows from that scope.

## Data model

### Identifiers

An **Identifier** is stored internally as a structured pair:

```json
{"name": "courseId", "value": "123"}
```

This avoids the escaping problems of a delimited string like `"courseId:123"`, and allows direct indexing on a `name + value` btree index.

**JSON contract on the API side (writing an event)**: an object whose values are strings, or arrays of strings, to cover the multi-value case directly:

```json
{
  "courseId": ["foo", "bar"],
  "otherId": "baz"
}
```

Each key becomes a `name`. Each value, or array element, becomes its own `{name, value}` row. An event carrying `courseId: ["foo", "bar"]` has both `courseId:foo` **and** `courseId:bar` at the same time.

### Metadata

**Metadata** is stored the same structured way as Identifiers, a `{name, value}` pair, and follows the same JSON contract on the API side (an object whose values are strings or arrays of strings).

Identifiers and metadata are two separate namespaces on an event. The same name can be used in both without clashing. Each also carries its own meaning: business identifiers name domain concepts, everything else (who wrote it, correlation, tenant, and so on) is Metadata. Both are stored and indexed the same way, and both can appear in a `QueryItem`.

**Tag** is the general term for a `{name, value}` pair, covering both Identifiers and Metadata. The store treats both the same way internally, and "Tag" avoids saying "identifier or metadata" every time. Application code, though, always treats them separately: knowing whether a `{name, value}` pair is an Identifier or a piece of Metadata tells the application how to read the event back correctly.

Splitting the spec's single Tag concept into Identifiers and Metadata still follows the DCB specification. The spec allows implementations to use different terms and field names, as long as they work the same way. Both Identifiers and Metadata behave exactly like Tags for matching. The split is just a naming choice on top of that, not a deviation from the spec.

An event can't carry the same `{name, value}` pair twice in its identifiers, or twice in its metadata. This matches the DCB specification's own rule that a set of Tags should not contain duplicates. An `append` that breaks this rule gets `400 Bad Request`, instead of being silently deduplicated, like every other invalid request (see Error responses).

An event can't carry more than **20 identifiers**, or more than **20 metadata** entries. These are fixed limits, not configuration, for the same reason as the cap on events per `append` (see Appending events): an event should stay a short, meaningful statement, not a container for a large list of values. An `append` that breaks this rule gets `400 Bad Request`.

### Two categories of data associated with an event

| Category | Role | Examples |
|---|---|---|
| **Identifiers** | Business identifiers, the main way to filter in DCB | `courseId`, `userId` as a subject |
| **Metadata** | Everything else about the event, also queryable | `tenantId`, `userId` (author), `correlation_id` |

### Volume context

Volume stays well within SQLite's normal limits with btree indexing: several million rows across the identifiers and metadata tables, at 1 to 2 identifiers per event, based on real production numbers.

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

Negation (`<>`, and so on) is not allowed. It's left out on purpose: a negation describes an unlimited set of matching events ("anything that isn't X"), so there's no way to guarantee that no future event could break the condition.

Two special cases from the spec, worth calling out:
- **`Query.all()`**: a Query can mean "all events", with no keys to list. Used both to read the whole store, and as `failIfEventsMatch` for an Append Condition that must fail if *any* event exists past `afterSequence`.
- **`afterSequence` can be past the last matching event**: this number is just what the client had seen, not necessarily a real event. The concurrency check compares `sequence > afterSequence` against matching events.

Every array in this grammar (the top-level `query`, or a `QueryItem`'s `types`, `identifiers`, or `metadata`) must be non-empty when present. An empty array gets `400 Bad Request`, rather than meaning something special. To leave an axis open within a `QueryItem`, leave that key out entirely, instead of sending `[]`. To match every event, use `"*"` in place of `query`, instead of an empty array.

A `QueryItem` with no `types`, `identifiers`, or `metadata` at all (`{}`) is valid, and matches every event, by the same rules above: an empty item behaves like `"*"` for that item, though `"*"` is still the normal way to say it for the whole query.

## HTTP API

Routes are named after the DCB spec's own operation names, `read` and `append`, instead of being modeled as a REST resource. DCB is not a CRUD API over a resource. It is two operations, each with its own meaning.

### Reading events

The `read` operation is exposed as `QUERY /read`, using the [HTTP QUERY method](https://www.rfc-editor.org/info/rfc10008/) (RFC 10008): safe, idempotent, and cacheable like GET, but carrying a JSON body like POST. This is needed since a Query can be too large or nested to fit in a query string.

The request body is a JSON object with a `query` key. That key holds either the array of `QueryItem` shown above, or the literal string `"*"` for `Query.all()`, plus optional `afterSequence` / `time` keys:

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
    },
    {
      "identifiers": [
        {"name": "someTag", "value": "someValue"},
        {"name": "otherTag", "value": "someOtherValue"}
      ]
    }
  ],
  "afterSequence": 12345,
  "time": {
    "from": "2026-01-01T00:00:00.000000Z",
    "before": "2026-02-01T00:00:00.000000Z"
  }
}
```

`Query.all()` is the literal string `"*"` in place of the `query` array: no `QueryItem` to filter by means every event matches.

`afterSequence` limits the read to events with a Sequence Position strictly greater than the given value, the same `sequence > afterSequence` rule used by the Append Condition's concurrency check. It's optional: leaving it out reads from the start of the store.

`time` limits the read to events whose `time` falls in the given range: `time.from` is inclusive (`>=`), `time.before` is exclusive (`<`). Both `time` itself, and its `from`/`before` keys, can be left out independently, so a query can filter from a point on, up to a point, or between two points. `time` exists only for search and inspection (for example "events from the last hour"), and plays no part in the Append Condition. Unlike Sequence Position, `time` is not guaranteed to always increase, so it can never be the basis for a concurrency check. `afterSequence` and `time` can be combined; an event must match both when both are given.

### Pagination

Events always come back in ascending Sequence Position order, the causal order of the log, and the only order a Decision Model ever needs when replaying history. No `order` option exists.

An optional `limit` key caps how many events come back in one response:

```json
{ "query": [...], "afterSequence": 12345, "limit": 500 }
```

Pagination uses a cursor, not an offset. An offset would be unstable on a log that keeps growing: events appended between two page requests would shift it, causing skipped or repeated results. The cursor is `afterSequence` itself: a Sequence Position never changes and only ever increases, so it stays a valid resume point no matter what else gets written meanwhile. To fetch the next page, the client repeats the same `query` with `afterSequence` set to the Sequence Position of the last event it received.

The response carries a `hasMore` boolean, so the client never has to guess whether it reached the end. The server fetches `limit + 1` rows. If it gets that many, it trims the result back to `limit` and returns `hasMore: true`. Otherwise it returns everything it got and `hasMore: false`.

Both the default `limit` (used when a request leaves it out) and the server-enforced maximum (the highest `limit` a request may ask for) are configuration, not fixed constants (see Configuration). How fast an application's projections can process a batch of events (see Projection rebuilds) varies enough between applications, and even between projections in the same application, that one fixed page size wouldn't fit all of them.

Left unset, `limit` falls back to a default of **1,000**, with a server-enforced maximum of **10,000**. That's sized so a default page is easy to buffer client-side, and a page at the maximum still finishes in a matter of seconds even for a fast projection, keeping the underlying SQLite read transaction short. Asking for more than the configured maximum gets `400 Bad Request` (see Error responses).

### Projection rebuilds

`QUERY /read` is also how a projection rebuilds itself: read every event matching the projection's types (and maybe identifiers/metadata) from the start of the log, replay them through the projection's own logic, then keep polling forward to stay caught up. A rebuild can mean reading a large share of the store's events.

Rebuilds run while the store keeps accepting writes, not just during a maintenance window. This is why `/read` returns pages instead of one response streaming the whole result set over one long connection: holding one SQLite read transaction open for a whole large rebuild would pin one MVCC snapshot in place for as long as the rebuild runs, blocking WAL checkpointing that whole time while writes keep piling up in the WAL file. Paging through `limit`-sized requests keeps each read transaction short, so the WAL checkpoints normally between pages. How much this matters depends on write throughput. Day to day, this protection mostly guards against bursts (a batch correction, a spike in normal traffic), not steady load, but the paged design holds up no matter how heavy that load gets.

Fetch time isn't always small next to processing time: some projections process events fast enough that the per-page round trip becomes a real, if still secondary, share of total rebuild time. This is exactly why the page size and its ceiling are configuration, not a fixed constant: a slow projection can use a small `limit` and pay almost nothing for it, while a fast one benefits from a larger `limit` that spreads the round-trip cost over more events per page. The same paging logic also covers both halves of a rebuild, with no mode switch: the client pages through history with `afterSequence` until `hasMore` is `false`, at which point it has caught up, then keeps polling with that same `afterSequence` to get new events as they're appended. Catching up on history and continuing to poll forward are the same loop.

### Response format

The response body is [NDJSON](https://github.com/ndjson/ndjson-spec) (`Content-Type: application/x-ndjson`): one JSON value per line, separated by `\n`, instead of a single JSON array wrapping the whole page. Each line is a self-contained JSON object carrying its own Sequence Position, so a response cut off mid-transfer (a dropped connection, a timeout) still leaves every fully-received line usable: the client resumes with `afterSequence` set to the last `sequence` it fully read, without losing events it already has. A single JSON array offers no such recovery: a response cut short mid-array is invalid JSON, and the whole page is lost. NDJSON also lets the server write each row as it comes out of SQLite, without holding the whole page in memory first.

The first line is always a header carrying `hasMore`. Every line after that is one matching event, in ascending Sequence Position order:

```
{"hasMore":true}
{"sequence":12346,"time":"2026-09-01T14:23:05.123456Z","type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}
{"sequence":12347,"time":"2026-09-01T14:23:07.981234Z","type":"user-updated","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}
```

`identifiers` and `metadata` come back in the same compact object shape used when writing (`{"courseId": ["foo", "bar"]}`), grouping multiple values for the same name under one key.

`time` is the moment the event was appended, in ATOM format (RFC 3339) with microsecond precision, always in UTC (`Z`). The store has no timezone setting: `time` is an internal reference value, not something meant for display, so it's always stored and returned in UTC. Converting to local time is left to the application.

`payload` is an opaque string: the store never parses or checks it. Its real format (JSON, XML, or anything else) is a convention owned by the writing application, based on the event's `type`. The store has no notion of it.

Response compression (gzip, negotiated the normal way through `Accept-Encoding`, above a minimum body size) is a nice-to-have, not built yet. Request compression isn't planned at all: writing a large batch of events at once isn't a `POST /append` use case.

### Appending events

The `append` operation is exposed as `POST /append`. The request body carries the events to write, plus an optional Append Condition:

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

`condition.failIfEventsMatch` follows the same grammar as `query` on `read` (an array of `QueryItem`, or `"*"`). `condition` itself is optional: an event with nothing to protect can be appended with no concurrency check at all.

A single `append` call may carry at most **100 events**. This is a fixed limit, not configuration, since it marks an architectural boundary, not a performance trade-off: a Decision Model appends the handful of events from one business decision, not a batch. A `POST /append` over this limit gets `400 Bad Request`. Writing many events at once (a data migration, a bulk import) isn't a `POST /append` use case (see Response format).

On success, the server responds `200 OK`, not `201 Created`, since there's no single addressable resource to point a `Location` header at. This matches `read`/`append` not being modeled as a REST resource. The body confirms the Sequence Position and `time` given to each event, in the order they were sent:

```json
{
  "events": [
    {"sequence": 12348, "time": "2026-09-01T14:25:00.000000Z"},
    {"sequence": 12349, "time": "2026-09-01T14:25:00.000001Z"}
  ]
}
```

If the Append Condition fails, the server responds `409 Conflict`:

```json
{ "error": "ConcurrencyException" }
```

A malformed request gets `400 Bad Request`. An event over the size limit gets `413 Payload Too Large` (see Event size limit and Error responses). If the write queue is already full when the request arrives, it's turned away right away instead of joining, with `503 Service Unavailable`:

```
HTTP/1.1 503 Service Unavailable
Retry-After: 1

{ "error": "AppendQueueFull" }
```

`Retry-After` gives the client a concrete backoff hint, instead of leaving it to guess (see Configuration for `maxQueuedWriters`, and Concurrency handling in Go for the queue itself). `DELETE /` in dev mode gets the same `503`/`Retry-After` treatment, since it joins the same queue (see Dev mode).

### Event size limit

A single event may not be bigger than **64 KiB**, measured as the combined UTF-8 byte length of its `type`, `identifiers`, `metadata`, and `payload`, not a character count. Multi-byte characters (accented text, for instance) count for more than one byte each. An `append` carrying an event over this limit gets `413 Payload Too Large`.

The limit is deliberate, not a technical ceiling to raise later: it keeps an event a short, meaningful statement about the world, rather than a data transport container, and keeps Decision Model replay fast (which can reload hundreds of thousands of events). Larger content (files, documents) belongs in external storage, referenced from the event instead of embedded in it.

Real-world event sizes stay well under the limit, with room to spare over real usage, rather than the limit being a ceiling anything currently pushes against.

The default of 64 KiB is configurable (see Configuration).

### Error responses

Every error response uses the same JSON envelope:

```json
{ "error": "InvalidRequest", "message": "afterSequence must be a non-negative integer" }
```

`error` is a stable code a client can check. `message` is a human-readable detail, included when it helps figure out the problem, left out when it wouldn't add anything (as with `ConcurrencyException` above).

`QUERY /read` and `POST /append` both respond `400 Bad Request` for any malformed or invalid body: invalid JSON, a `query` / `condition.failIfEventsMatch` that isn't an array of `QueryItem` or `"*"`, an empty array anywhere the Query grammar needs a non-empty one (see Query grammar), a non-integer `afterSequence` or `limit`, a `limit` above the configured maximum (see Pagination), an invalid `time.from` / `time.before` timestamp, an event missing its `type`, an event carrying a duplicate identifier or metadata value, more than 20 identifiers/metadata entries (see Metadata), an `append` with more than 100 events (see Appending events), and so on.

Validation is hand-written in Go, not driven by a JSON Schema: the request surface is small, several rules are about meaning rather than pure structure (a valid ATOM timestamp, a consistent `time.from`/`time.before` range, the full DCB `QueryItem` grammar), and a generic schema validator's error messages don't map cleanly onto the `{error, message}` shape above.

## Append Condition and concurrency

Standard DCB flow:
1. `read(query)`: read the relevant events, keep the last `sequence` read (`afterSequence`)
2. Decide on the new events to write (Decision Model)
3. `append(events, condition: {failIfEventsMatch: query, afterSequence})`
4. The operation fails if an event matching `query` exists after `afterSequence`

## Concurrency handling in Go

### Principle: the queue manager

A queue manager gives out exclusive SQLite write access, strictly in the order requests arrive. It knows nothing about a writer's Append Condition or the events it plans to write. Only two states exist: **Active** (at most one writer at a time, the only one allowed to touch SQLite) and **Queued** (every other writer, waiting its turn in line). "Writer" covers both `POST /append` and, in dev mode, `DELETE /` (see Dev mode). Both need the same thing: to be the only one touching SQLite while they work.

**Flow for a writer:**
1. The HTTP handler asks the queue manager to join the line.
2. If the queue is already at its configured depth (see Configuration), the request is turned away right away with `503 AppendQueueFull` (see Error responses), instead of joining.
3. Otherwise it waits until every writer ahead of it (the active one, and everyone queued before it) is done.
4. Once it becomes active, the handler does its work inside one SQLite transaction, on the single write connection: for `/append`, that means checking the Append Condition and, if it holds, inserting the events (see Application-controlled Sequence Position below); for `DELETE /`, that means wiping the tables, with no condition.
5. The handler tells the queue manager it's done. The next queued writer, if any, becomes active.

**Why checking at write time is correct:** the Append Condition is checked right before the insert, against the database's real state at that moment, inside the same transaction as the insert. Nothing else can write between the check and the insert, because strict, in-order admission to the queue guarantees that by itself.

**No conflict checking.** The queue manager never looks at `types`, `identifiers`, `metadata`, or the new events being written. Every request joins the line purely by arrival order: there's no conflict matrix, and no split between conflicting and non-conflicting writers. Two writers with unrelated Append Conditions still run one after the other, since neither one could reach SQLite at the same time as the other anyway (the write connection pool allows only one connection; see Storage: SQLite).

**Cancellation while queued:** a queued writer watches its own HTTP request. If the client disconnects, or the request times out before its turn, it leaves the line right away, freeing its spot for other waiters and skipping the SELECT+INSERT entirely. Everyone behind it simply moves up one spot.

**Cancellation while active:** if the active writer's request is canceled before its transaction commits, the transaction is rolled back and the next queued writer becomes active. If the transaction had already committed, canceling afterward changes nothing for the store: the write happened, only the response never reached the client (see Startup, shutdown, and crash behavior below for the same case).

**`busy_timeout` doesn't matter much for writers.** Since only one writer touches SQLite at a time by design, `_busy_timeout` no longer needs to absorb writer-to-writer waiting on the write connection: strict, in-order admission already guarantees a writer never starts its transaction while another one is mid-transaction.

### Application-controlled Sequence Position

Strict, in-order admission has a second effect beyond concurrency: since only one writer ever touches SQLite at a time, TamarackDB can assign the Sequence Position itself, in memory, instead of leaving it to SQLite's `AUTOINCREMENT`.

`events.sequence` is a plain `INTEGER PRIMARY KEY`, with the value set by the application on insert (see Schema below). Before accepting any writers (reads are unaffected, and can start right away), the process reads the current highest `sequence` in the `events` table, and keeps it in memory as the next-sequence counter. An empty table starts the counter the same way `AUTOINCREMENT` would: the first event gets sequence 1.

The counter is only read, and only moved forward, *after* a writer's work is confirmed to happen: for `/append`, that means working out the sequence numbers for its batch of events only once the Append Condition has been checked and holds, never before. This matters: if the condition fails, the writer inserts nothing and responds `409 Conflict`, and the counter must not have moved, or every failed append would leave a permanent gap in the sequence.

Knowing every event's sequence up front means the whole batch's `events` rows can be written as one multi-row `INSERT`, followed by one multi-row `INSERT` into `identifiers` and one into `metadata`, instead of a per-event round trip to fetch an ID between each event and its tags. `PRAGMA foreign_keys = ON` is still checked right away, not deferred to commit, so a row in `identifiers` or `metadata` still can't point to an `event_sequence` that doesn't exist yet in `events`, within the same transaction: application-controlled sequencing doesn't remove that ordering, only the round trip through SQLite needed to learn each event's ID before its tags can be written.

**Skipping the conflict check when nothing was appended since the read.** When a writer becomes active, if `afterSequence` equals the counter's last-assigned value, no event exists past that point at all. That means `failIfEventsMatch` can't match anything, whatever it is, so the SELECT that would otherwise check it against events written since `afterSequence` can be skipped entirely: the writer goes straight to working out sequence numbers and inserting. This is the common case in practice: a `read` right before an `append`, with no other writer in between. A bare `afterSequence` condition (no `failIfEventsMatch`) never needs a SELECT at all, in any case: "does any event exist after `afterSequence`" can be answered directly from the counter. This is purely an internal shortcut: it changes how a writer reaches its decision, never the decision itself, or anything in the HTTP contract.

### Reads

Reads do **not** go through the queue manager. The DCB spec only asks for locking on append. A `read(query)` queries SQLite directly.

Consistency comes from SQLite's **MVCC** mode (WAL): a read sees a steady snapshot from the moment it starts, and never sees a partly committed write, no matter how far along that write is. A read that starts just before an append finishes simply won't see that new event, which is fine, since the client will use the Sequence Position it actually read as `afterSequence` for its next append anyway.

**Write atomicity:** writing an event touches several tables (the `events` row, one or more identifier rows, one or more metadata rows). These inserts must run inside one explicit SQLite transaction, so an event, its identifiers, and its metadata become visible together, never partway through.

**Sequence Position assignment:** the active writer assigns the Sequence Position itself, in memory, before insert (see Application-controlled Sequence Position above), rather than leaving it to SQLite's `AUTOINCREMENT` and actual commit order.

### Startup, shutdown, and crash behavior

On startup, before opening the store, the process prints a banner and its resolved configuration to stdout: bind address, port, the TLS and auth flags and file paths (`authToken` itself is never printed), database path, dev mode, and the pagination/event-size/queue-depth limits. This is a plain operational aid, for checking at a glance what a given instance is actually set up to do, not a machine-readable format meant for parsing.

Opening the store checks `PRAGMA user_version` against the schema version built into the binary, then reads the current highest `sequence` in the `events` table into the in-memory Sequence Position counter (see Application-controlled Sequence Position above). Both finish before the process accepts any writers, and reads can be served as soon as the store is open.

On `SIGINT` or `SIGTERM`, the process shuts down in order: the HTTP server stops taking new connections and finishes requests already in flight (`http.Server.Shutdown`, capped at 10 seconds), the queue manager closes, then the SQLite store closes, releasing its connections and the `.lock` file. A fatal storage error found mid-flight (see Fatal storage errors below) drives this same ordered shutdown, instead of an abrupt exit. The HTTP server also sets `ReadHeaderTimeout` to 10 seconds, closing a connection that never finishes sending its request headers, instead of holding it open forever.

The queue manager's state (who's active, who's queued) is purely transient, held only in memory for the life of the process. Nothing is saved, and nothing needs to be rebuilt on startup: a freshly started process begins with an empty queue, which is correct: every writer that existed before a crash belonged to an HTTP request whose client connection is now gone too. The in-memory Sequence Position counter is the one piece of writer state that *is* rebuilt on startup, from the database itself (see above), since it has to match what's actually saved on disk.

A panic in one request handler is caught by the HTTP server, without crashing the process. The handler's deferred queue release still runs during the panic's unwind, before that recovery happens, so no writer stays marked active forever. A full process crash (an unrecovered panic, SIGKILL, an out-of-memory kill) takes the whole in-memory queue state down with it, so there's nothing left to leak either way.

SQLite's own atomicity guarantees the store itself: a crash mid-append leaves an unfinished WAL transaction, caught and discarded by its per-frame checksums the next time a connection opens. The event, its identifiers, and its metadata never become visible in a half-written state.

None of this tells the client whether its append actually happened, though: the crash (or any dropped connection) can land after SQLite commits but before the `200 OK` reaches the client. That's the same lost-acknowledgment problem as any request/response protocol over an unreliable connection; SQLite's guarantees don't reach the response. A client that wants to retry safely after a dropped connection should always attach an Append Condition, even one with no `failIfEventsMatch`: an `afterSequence` on its own is enough. If the original append actually went through, the Sequence Position has already moved past it, so the retry fails with `409 Conflict` instead of writing a duplicate event.

**Fatal storage errors:** a SQLite error that suggests the file itself may be damaged (an I/O error, detected corruption, failure to open the database file) is treated as fatal: the process logs it and exits, instead of trying to keep serving requests against a store it can no longer trust. This is deliberately simple: no per-error recovery logic, just a clean restart, which is cheap and safe given the transient state described above, and which `/health` and a process supervisor are already set up to catch and act on. A temporary, non-fatal SQLite error (a busy lock during a WAL checkpoint, say) doesn't count as fatal: it's handled inside that one request, instead of taking the whole process down for every other client.

**Guaranteed release on failure:** for any error that isn't fatal, the handler releases its queue slot in a deferred call, set up right after joining the queue, before the SELECT+INSERT even runs. So it always runs, no matter how that code exits: a returned SQLite error, a panic, or plain success. A writer never stays marked active because of a failure unrelated to its Append Condition.

## Storage: SQLite

SQLite is used as the storage engine, for these reasons:
- Volume (several million rows across the identifiers and metadata tables) fits comfortably within SQLite's limits, with `name + value` indexes
- No external network or process to depend on, in line with the goal of keeping state centralized in memory on the Go side
- Single-writer behavior, in line with the single-process model ("single brain"): the process is the only writer for as long as it runs
- WAL mode allows reads to happen at the same time as writes, without blocking
- A plain, inspectable file format: the database can be opened and queried with ordinary SQLite tools, not some closed format, and backed up the same way, through SQLite's own backup tools (for example `.backup`, `VACUUM INTO`) instead of a raw copy of the file, which can miss commits still sitting in the WAL

A file-level copy is not the only way to back up an instance. `tamarackdb-backup`
reads events over `QUERY /read` from a live source and writes them into a local
file through `Store.Import`, a variant of `Append` that skips sequence
reservation and the append-condition check, since the sequences it receives are
already assigned by the source. The result is a plain SQLite file built through
the same `store.Open` schema path used everywhere else, so unlike a raw file
copy, it can be opened and served as a live instance in its own right.

**Enforcing single-writer at the OS level:** `writeDB.SetMaxOpenConns(1)`, WAL, and `_busy_timeout` only keep writes in order *inside* one process: nothing stops a second `tamarackdb-server` process from opening the same database file and racing the first. `store.Open` closes that gap directly: before touching the SQLite file, it takes an exclusive, non-blocking `flock(2)` on a sibling `<path>.lock` file, and holds it for the life of the process. It's held open, not just briefly grabbed, so the OS releases it automatically on exit or crash: no leftover lock file can ever block a later start. A second process finds the lock already held, and fails fast at startup with `ErrDatabaseLocked`, instead of silently corrupting state or fighting the first process over `SQLITE_BUSY`. This relies on `flock(2)`; TamarackDB only targets Linux. Docker covers every other platform.

### Schema

```sql
PRAGMA user_version = 1;

CREATE TABLE events (
    sequence INTEGER PRIMARY KEY,
    time     TEXT NOT NULL,
    type     TEXT NOT NULL,
    payload  TEXT NOT NULL
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
```

`time` is stored as `TEXT`, not as an integer timestamp: its fixed-width ATOM format sorts the same way alphabetically as it does chronologically, so nothing needs to be converted between what's stored and what's returned.

`identifiers` and `metadata` are `WITHOUT ROWID` tables, keyed by their natural combined primary key `(event_sequence, name, value)`: these are pure link rows, so a separate rowid would just be an extra, unneeded btree. The secondary index `(name, value, event_sequence)` on each table is what serves the DCB matching check directly, with `event_sequence` included so the index alone can answer the scan.

The `events(sequence)` foreign key on both tables is enforced by turning on `PRAGMA foreign_keys = ON` on every connection at startup: SQLite reads foreign key declarations, but doesn't enforce them by default. Turning this on catches implementation bugs (say, an identifier or metadata row written with an `event_sequence` that doesn't match a real event), rather than serving any real functional need, since the store is append-only with a single writer.

Two more pragmas are set on every connection at startup, next to `foreign_keys`: `PRAGMA journal_mode = WAL` (the mode this design assumes throughout, for MVCC reads and checkpoint behavior) and `PRAGMA synchronous = FULL`. `FULL` costs one extra fsync per commit compared to the `NORMAL` mode WAL usually pairs with, but at this scope's write volume that cost doesn't matter, and it buys the strongest durability SQLite offers, for what is, for each application, its single source of truth with no backup copy running behind it.

The write connection also sets `_busy_timeout = 5000` (five seconds), and opens every transaction with `BEGIN IMMEDIATE` (`_txlock=immediate` in the DSN), taking SQLite's write lock at the start of the transaction, rather than waiting until the first write statement runs. The check SELECT and the INSERTs that follow it commit as one atomic unit, with no window where another connection could slip in between them. Since `writeDB.SetMaxOpenConns(1)` already forces every write onto one connection (see Enforcing single-writer at the OS level), the busy timeout only guards against something else briefly holding the file (a passive checkpoint, an external `sqlite3` shell), not against another instance of TamarackDB.

Checkpointing relies on SQLite's own automatic passive checkpoint (triggered on its own once the WAL crosses its default size, without blocking any reader or writer), instead of a separate checkpoint goroutine or schedule. This is exactly what bounded pagination on `/read` protects (see Projection rebuilds): a long-held read transaction can stall that automatic checkpoint for as long as it runs, but nothing about the checkpoint itself needs to be triggered by hand once reads stay short.

On startup, the process reads `PRAGMA user_version` and checks it against the schema version built into the binary. A database file that doesn't exist yet is created fresh, with the schema above setting it at the current version. An existing file whose version doesn't match (older, from a schema that's since changed, or newer, from a downgraded binary) is fatal: the process logs it and refuses to start, the same treatment as any other storage integrity failure (see Startup, shutdown, and crash behavior).

Moving an existing database from one schema version to the next is the job of a separate binary, not the TamarackDB process itself: a dedicated migration tool, run once, on purpose, between a schema change and the next deployment of the main binary. The TamarackDB server never changes its own schema.

## Configuration

TamarackDB's startup configuration (bind address, port, TLS settings, auth token, database path, pagination/event-size limits, and the write queue depth) comes from three sources, in this order:

1. A JSON configuration file, passed via `-config` (defaults to `config.json` in the working directory).
2. `TAMARACKDB_*` environment variables, one per configuration key.
3. Built-in defaults, for the handful of keys that have one (`defaultLimit`, `maxLimit`, `maxEventSize`, `maxQueuedWriters`).

A value set in the configuration file always wins over the matching environment variable. The configuration file itself is optional: an application deployed as one instance per environment, each with its own file, uses it as the single source of truth. A container deployment with no file at all is set up entirely through the environment instead. Both paths produce the same `Config`, and every field is checked the same way regardless of where it came from (see below).

| Key | Environment variable |
|---|---|
| `bindAddress` | `TAMARACKDB_BIND_ADDRESS` |
| `port` | `TAMARACKDB_PORT` |
| `enableTls` | `TAMARACKDB_ENABLE_TLS` |
| `tlsCertFile` | `TAMARACKDB_TLS_CERT_FILE` |
| `tlsKeyFile` | `TAMARACKDB_TLS_KEY_FILE` |
| `enableAuth` | `TAMARACKDB_ENABLE_AUTH` |
| `authToken` | `TAMARACKDB_AUTH_TOKEN` |
| `databasePath` | `TAMARACKDB_DATABASE_PATH` |
| `defaultLimit` | `TAMARACKDB_DEFAULT_LIMIT` |
| `maxLimit` | `TAMARACKDB_MAX_LIMIT` |
| `maxEventSize` | `TAMARACKDB_MAX_EVENT_SIZE` |
| `devMode` | `TAMARACKDB_DEV_MODE` |
| `maxQueuedWriters` | `TAMARACKDB_MAX_QUEUED_WRITERS` |

`maxQueuedWriters` caps how many writers may wait in the queue manager's line at once (see Concurrency handling in Go). A request that arrives when the queue is already at that depth gets `503 AppendQueueFull` (see Error responses) instead of joining. It's optional, like `defaultLimit`/`maxLimit`/`maxEventSize`, and gets a default from `Load` the same way when left out (built-in default: 100). It's deliberately not "0 means no limit": a queue with no cap at all would let a burst, or a broken client, pile up an unlimited number of blocked HTTP connections, so every deployment gets a bound whether it sets one or not. Beyond that default, there's no single right value: size it against how many concurrent users the owning application expects, and remember that a given user isn't always appending, so a burst of writers is normally a small share of total users, not all of them at once.

## Security

TLS and Bearer-token checks are each controlled by their own flag, `enableTls` and `enableAuth`, so a deployment can match its own network's trust level instead of the store forcing one fixed stance. Both default to off, for a deployment where the network is already isolated further out (a private segment, a VPN, a firewall), which would make TLS and per-request auth extra weight on top of a trust boundary already enforced elsewhere.

When `enableTls` is on, the bind address, port, and TLS certificate/key paths are all set the same way (see Configuration). The Go process handles TLS itself, via `ListenAndServeTLS`, with no reverse proxy in front. When `enableTls` is off, the process serves plain HTTP on the configured bind address and port.

When `enableAuth` is on, every registered route needs a Bearer token in the `Authorization` header (`Authorization: Bearer <token>`): `read`, `append`, `/health`, the observability endpoints (`/metrics`, `/debug`), and, in dev mode, `DELETE /` too. The token is a single fixed value, set as `authToken`. A request with no valid token gets `401 Unauthorized` before it reaches any handler logic. Rotating the token means changing the configuration file or environment variable and restarting the process: there's no in-memory rotation, or window where two tokens both work, in line with the queue manager's own transient, in-memory state. When `enableAuth` is off, the API serves every request with no auth check at all.

One token, with no per-client scope, is enough because a TamarackDB instance has exactly one trusted caller: the owning application. If that application itself serves many tenants, keeping them apart is its own job, done with the `tenantId` metadata already carried on events. It's not something TamarackDB's auth layer needs to handle.

### Dev mode

`devMode` (see Configuration) turns on one extra endpoint, `DELETE /`, which wipes every event, identifier, and metadata row from the database; the schema itself stays in place. It responds `204 No Content` on success. The endpoint doesn't exist at all unless `devMode` is `true`. Left at its default of `false`, `DELETE /` gets the stdlib's plain `404`, like any other unregistered path. That keeps this destructive call out of reach in a normal deployment, instead of reachable-but-guarded, matching the "fail loud, keep it simple" stance used throughout: there's no separate permission or confirmation step once `devMode` is on. It joins the same FIFO write-admission queue as `POST /append` (see Concurrency handling in Go), so a wipe can no longer land mid-append, or an append mid-wipe, and it counts toward `maxQueuedWriters` the same way. It's still meant only for local development and test environments, never a production instance. The in-memory Sequence Position counter is deliberately left alone by a wipe: the tables go empty, but the counter keeps climbing from wherever it was.

## Management / observability features

### Health check

A lightweight `GET /health` endpoint confirms the process is responding and SQLite is reachable (with a trivial `SELECT 1`), for a process supervisor or load balancer to check. Even for a single-instance service, a health check still helps restart and alerting logic. On success it responds `200 OK` with a small JSON body, `{"status": "ok", "version": "1.2.3"}`. On failure to reach SQLite, it responds `503 Service Unavailable` rather than `500`, the usual signal a supervisor or load balancer already expects for "not ready right now," different from the `500` an ordinary request failure returns elsewhere in the API.

### Versioning

The running build's version is a single value, read from the `VERSION` file at the root of the repository and baked into the binary at build time, via `-ldflags "-X main.version=..."`. It's not something the process reads or reloads while running. `./tamarackdb-server -version` prints that value and exits right away, without loading the configuration file or opening the store, for a quick check of what's actually running, without having to reach it over the network. That same value is what `GET /health` reports in its `version` field above.

### Request logging

Every request logs one line to stdout once its handler finishes: HTTP method, path, resulting status code, and how long it took, e.g. `tamarackdb: POST /append 200 1.23ms`. This wraps the whole routed handler, including authentication, so a request turned away with `401 Unauthorized` gets logged just like any other.

### Nice to have: queue observability

Event and row counts, per-type breakdowns, and database file size (anything you can work out from the store's own content) are a query away, straight against the SQLite file, so the store doesn't need to expose them itself. What the file can't answer is the queue manager's own live, in-memory state, which only exists for the life of the process. Two endpoints cover that, kept separate since they serve different needs:

**`GET /metrics`**: Prometheus exposition format, for scraping into existing monitoring:
- `tamarackdb_writer_active` (gauge): whether a writer currently holds exclusive SQLite write access (`1`) or not (`0`)
- `tamarackdb_requests_queued` (gauge): number of write requests (`POST /append`, or, in dev mode, `DELETE /`) currently waiting in the queue
- `tamarackdb_queue_longest_wait_seconds` (gauge): longest current wait, in seconds, among queued write requests; `0` when the queue is empty
- `tamarackdb_writes_admitted_total` (counter): total writers let through to exclusive SQLite write access since startup
- `tamarackdb_appends_failed_total` (counter): total appends that failed on a concurrency conflict (`409 ConcurrencyException`) since startup

**`GET /debug`**: a JSON snapshot for digging into one specific stuck or slow write, too detailed to fit a metric:

```json
{
  "time": "2026-09-01T14:23:05.123456Z",
  "active": {
    "since": "2026-09-01T14:23:04.900000Z",
    "ageSeconds": 0.223
  },
  "queued": [
    {
      "queuedAt": "2026-09-01T14:23:05.000000Z",
      "waitSeconds": 0.1
    }
  ]
}
```

`active` describes the current active writer, if any (`null` when the queue manager is idle). `queued` lists every writer still waiting, oldest first, with `waitSeconds` instead of `ageSeconds`. Neither one carries the writer's Append Condition or events: the queue manager never knows either (see Principle: the queue manager). `queued` is always present, never `null`, even when empty.

Since the queue manager already serializes access to its own state behind a mutex, answering either request is just a quick, always-available read of its current counters, never blocked behind a queued or in-flight write. Both are read-only: no endpoint lets you force-finish a writer's turn, or otherwise change the queue manager's state, since that would bring back the exact race conditions it exists to prevent.

## Implementation

The concrete Go code behind the queue manager, the Query-to-SQL translation, the schema migration tool, and the backup tool live in `internal/queue`, `internal/store`, `cmd/tamarackdb-migrate`, and `cmd/tamarackdb-backup`.
