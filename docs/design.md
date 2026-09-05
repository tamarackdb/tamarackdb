# TamarackDB — DCB Event Store in Go — Design Specification

## Editorial conventions

- Always write and edit this document in English.
- When a decision was made between option A and option B, write the document as if the chosen option had been the only one ever considered. Do not mention the alternative, the trade-offs weighed, or that a decision was made at all — present the chosen approach as a plain, uncontested fact.
- This applies to future edits too: if a section is revised because a prior choice changes, rewrite it clean rather than layering "previously X, now Y" language, unless explicitly asked to preserve that history.

## Context

TamarackDB is an event store in Go, compliant with the [DCB (Dynamic Consistency Boundaries) specification](https://dcb.events/specification/), accessible via HTTP, using SQLite as the storage engine. The service runs as a single instance ("single brain"), not a multi-instance cluster. The full append orchestration happens on the Go side (goroutines/channels), not in SQL.

Each application owns its own TamarackDB instance, backed by its own SQLite database file. Two applications never share a single TamarackDB instance.

### Name

TamarackDB takes its name from the tamarack (*Larix laricina*), a conifer native to Quebec's boreal forest and one of the few conifers used in dendrochronology, because its annual growth rings are unusually distinct and easy to read. Each ring records one season, laid down once and never revised, and the tree's full history can be reconstructed by reading the rings in order from the center out — the same shape as this event store: an ordered, append-only sequence of immutable facts from which current state is reconstructed by replay.

### Scope

TamarackDB targets internal enterprise systems running in an environment the owning team fully controls — not a general-purpose product competing with existing DCB implementations such as [UmaDB](https://umadb.io/), [MartenDB](https://martendb.io/events/dcb.html), or [EventSourcingDB](https://docs.eventsourcingdb.io/best-practices/dynamic-consistency-boundaries/). Those serve a different scale and audience; this one serves internal systems with modest throughput and few concurrent writers, in line with the write volumes in Production reference data. It isn't designed to sustain thousands of requests per second — every design choice in this document, from a single SQLite file to a single-process gatekeeper with no clustering, follows from that scope.

## Data model

### Identifiers

An **Identifier** is represented internally as a structured pair:

```json
{"name": "courseId", "value": "123"}
```

This avoids the escaping issues of a delimited string (`"courseId:123"`) and allows direct indexing via a `name + value` btree index.

**JSON contract on the API side (writing an event)**: an object whose values are strings or arrays of strings, to natively cover the multi-value case:

```json
{
  "courseId": ["foo", "bar"],
  "otherId": "baz"
}
```

Each key becomes a `name`, each value (or array element) becomes a distinct `{name, value}` row. An event carrying `courseId: ["foo", "bar"]` has both identifiers `courseId:foo` **and** `courseId:bar` at the same time.

### Metadata

**Metadata** is represented the same structured way as Identifiers — a `{name, value}` pair — and follows the same JSON contract on the API side (an object whose values are strings or arrays of strings).

Identifiers and metadata are kept as two distinct namespaces on an event, so the same name can be used independently in each without collision, and so each carries its own semantic intent — business identifiers for domain concepts, everything else (authorship, correlation, tenancy, etc.) as Metadata. Both are structured and indexed the same way, and both can be used in a `QueryItem`.

**Tag** is used as the general term for a `{name, value}` pair when speaking about Identifiers and Metadata collectively — the store treats both the same way structurally, and "Tag" avoids having to say "identifier or metadata" every time. Domain code, however, always deals with the two separately: knowing whether a given `{name, value}` pair is an Identifier or a piece of Metadata is what tells userland code how to hydrate an event correctly when reading it back.

Splitting the spec's single Tag concept into Identifiers and Metadata stays within the DCB specification: it explicitly allows implementations to use different terms and field names, as long as they offer equivalent functionality. Both Identifiers and Metadata behave exactly like Tags for matching purposes — the split is a naming and namespacing choice on top of that equivalent functionality, not a deviation from it.

An event may not carry the same `{name, value}` pair twice within its identifiers, nor twice within its metadata — this mirrors the DCB specification's own constraint that a set of Tags SHOULD NOT contain duplicate Tags. An `append` violating this is rejected with `400 Bad Request` rather than silently deduplicated, consistent with every other semantically invalid request (see Error responses).

An event may not carry more than **20 identifiers** or more than **20 metadata** entries — fixed constants, not configuration, for the same reason as the cap on events per `append` (see Appending events): an event is meant to stay a short, meaningful assertion, not a container for a large multi-value relationship. An `append` violating this is rejected with `400 Bad Request`.

### Two categories of data associated with an event

| Category | Role | Examples |
|---|---|---|
| **Identifiers** | Business identifiers, the primary DCB filtering axis | `courseId`, `userId` as a subject |
| **Metadata** | Everything else associated with the event, also queryable | `tenantId`, `userId` (author), `correlation_id` |

### Volume context

Volume stays comfortably within SQLite's normal capabilities with btree indexing: several million rows across the identifiers and metadata tables at 1 to 2 identifiers per event, grounded in real production numbers (see Production reference data).

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
- The three axes are combined with **AND**: an event must satisfy its type, its identifiers, and its metadata conditions together

No negation is allowed (`<>`, etc.) — deliberately excluded, since a negation describes an unbounded set of matching events ("anything that isn't X"), making it impossible to ever guarantee that no future event could violate the condition.

Two special cases from the spec to respect:
- **`Query.all()`** — a Query can represent "all events"; the reservation manager must be able to represent a "blocks everything" reservation, with no discrete keys to enumerate.
- **`afterSequence` can exceed the position of the last matching event** — this number represents what the client had seen, not necessarily an actual existing event. The concurrency check compares `sequence > afterSequence` against matching events.

Every array in this grammar — the top-level `query`, and a `QueryItem`'s `types`, `identifiers`, or `metadata` — must be non-empty when present; an empty array is rejected with `400 Bad Request` rather than given special meaning. To leave an axis unconstrained within a `QueryItem`, omit that key entirely rather than supplying `[]`. To match every event, use `"*"` in place of `query` rather than an empty array.

A `QueryItem` with no `types`, `identifiers`, or `metadata` at all (`{}`) is valid and matches every event, by the same subset/OR rules stated above — an empty item behaves the same as `"*"` for that item, though `"*"` remains the idiomatic way to express it for the whole query.

## HTTP API

Routes are named after the DCB specification's own operation names, `read` and `append`, rather than modeled as a REST resource — DCB is not a CRUD API over a resource, it is two operations with their own distinct semantics.

### Reading events

The `read` operation is exposed as `QUERY /read`, using the [HTTP QUERY method](https://www.rfc-editor.org/info/rfc10008/) (RFC 10008): safe, idempotent, and cacheable like GET, but carrying a JSON body like POST — necessary here since a Query can be too large or nested to fit in a query string.

The request body is a JSON object with a `query` key, holding either the array of `QueryItem` described above or the literal string `"*"` for `Query.all()`, and optional `afterSequence` / `time` keys:

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

`Query.all()` is represented by the literal string `"*"` in place of the `query` array: no `QueryItem` to filter by means every event matches.

`afterSequence` restricts the read to events whose Sequence Position is strictly greater than the given value — the same `sequence > afterSequence` rule used by the Append Condition's concurrency check. It is optional: omitting it reads from the beginning of the store.

`time` restricts the read to events whose `time` falls in the given range: `time.from` is inclusive (`>=`), `time.before` is exclusive (`<`). Both `time` itself and its `from`/`before` keys are optional independently, so a query can filter from a point onward, up to a point, or between two points. `time` exists purely for search and inspection purposes (e.g. "events from the last hour") and plays no role in the Append Condition: unlike Sequence Position, `time` isn't guaranteed strictly monotonic, so it can never be used as the basis for a concurrency check. `afterSequence` and `time` can be combined; when both are given, an event must satisfy both to match.

### Pagination

Events are always returned in ascending Sequence Position order — the causal order of the log, and the only order a Decision Model ever needs when replaying history. No `order` option is exposed.

An optional `limit` key caps the number of events returned in one response:

```json
{ "query": [...], "afterSequence": 12345, "limit": 500 }
```

Pagination is cursor-based, not offset-based: an offset would be unstable on a log that keeps growing, since events appended between two page requests would shift it, causing skipped or duplicated results. The cursor is `afterSequence` itself — a Sequence Position is immutable and monotonic, so it stays a valid resume point no matter what else gets written in the meantime. To fetch the next page, the client repeats the same `query` with `afterSequence` set to the Sequence Position of the last event received.

The response carries a `hasMore` boolean so the client never has to guess whether it reached the end. The server fetches `limit + 1` rows: if it gets that many, it trims the result back to `limit` and returns `hasMore: true`; otherwise it returns everything it got and `hasMore: false`.

Both the default `limit` (applied when a request omits it) and the server-enforced maximum (the highest `limit` a request is allowed to ask for) are configuration, not fixed constants (see Configuration). Paging performance depends on how fast a given application's projections can process a batch of events (see Live projection rebuilds), which varies enough between applications, and even between projections in the same application, that a single hardcoded page size wouldn't fit all of them.

Left unset, `limit` falls back to a default of **1,000** and a server-enforced maximum of **10,000** — sized so a default page stays comfortable to buffer client-side, and a page at the maximum still processes in a matter of seconds even for a fast projection, keeping the underlying SQLite read transaction short (see Production reference data for the sizing behind these numbers). A request asking for more than the configured maximum is rejected with `400 Bad Request` (see Error responses).

### Live projection rebuilds

`QUERY /read` is also how a projection rebuilds itself: read every event matching the projection's subset of types (and possibly identifiers/metadata) from the beginning of the log, replay them through the projection's transition logic, then keep polling forward to stay live. A rebuild can mean reading a large fraction of the store's total events (see Production reference data).

Rebuilds run live, with the store continuing to accept writes throughout — not only during a maintenance window. This is why `/read` returns bounded pages rather than a single response streaming an entire result set over one long-lived connection: holding one SQLite read transaction open for the whole duration of a large rebuild would pin one MVCC snapshot in place for as long as the rebuild runs, blocking WAL checkpointing for that entire time while writes keep accumulating in the WAL file. Paging through `limit`-sized requests keeps each underlying read transaction short, so the WAL checkpoints normally between pages. How much this matters in practice depends on write throughput (see Production reference data for a real-world estimate); day-to-day this protection guards mostly against bursts — a batch correction, a spike in normal application traffic — rather than steady load, but the bounded-page design holds up regardless of how heavy that load gets.

Fetch time isn't always negligible next to processing time: some projections process events fast enough (see Production reference data) that the per-page round-trip becomes a real, if still secondary, share of total rebuild time. This is exactly why the page size and its ceiling are configuration rather than a fixed constant: a slow projection can page with a small `limit` and pay next to nothing for it, while a fast one benefits from a larger `limit` that amortizes the round-trip cost over more events per page. The same pagination mechanism also serves both halves of a rebuild without switching modes: the client pages through history with `afterSequence` until `hasMore` is `false`, at which point it's caught up, and it keeps polling with that same `afterSequence` to receive new events as they're appended — catch-up and live tail are the same loop.

### Response format

The response body is [NDJSON](https://github.com/ndjson/ndjson-spec) (`Content-Type: application/x-ndjson`): one JSON value per line, separated by `\n`, rather than a single JSON array wrapping the whole page. Each line is a self-contained JSON object carrying its own Sequence Position, so a response interrupted mid-transfer (a dropped connection, a timeout) still leaves every fully-received line usable: the client resumes with `afterSequence` set to the last `sequence` it successfully parsed, without discarding events it already has. A single JSON array offers no such recovery — a response cut short mid-array is invalid JSON, and the whole page is lost. NDJSON also lets the server marshal and write each row as it comes out of SQLite, without accumulating the full page into memory as one slice first.

The first line is always a header object carrying `hasMore`; every following line is one matching event, in ascending Sequence Position order:

```
{"hasMore":true}
{"sequence":12346,"time":"2026-09-01T14:23:05.123456Z","type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}
{"sequence":12347,"time":"2026-09-01T14:23:07.981234Z","type":"user-updated","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}
```

`identifiers` and `metadata` are rendered as the same compact object shape used on the write side (`{"courseId": ["foo", "bar"]}`), grouping multiple values for the same name under one key.

`time` is the moment the event was appended, in ATOM format (RFC 3339) with microsecond precision, always in UTC (`Z`). The store has no timezone configuration: `time` is an internal reference value, not a display value, so it is always recorded and returned in UTC, with local-time conversion left to the application.

`payload` is an opaque string — the store never parses or validates its content. Its actual format (JSON, XML, or anything else) is a convention owned by the writing application, keyed by the event's `type`; the store has no notion of it.

Response compression (gzip, negotiated the standard way via `Accept-Encoding` above a minimum body size) is a nice-to-have, not implemented for now. Request compression isn't planned at all — writing a large batch of events at once isn't a `POST /append` use case.

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

`condition.failIfEventsMatch` follows the same grammar as `query` on `read` (an array of `QueryItem`, or `"*"`). `condition` itself is optional: an event with no invariant to protect can be appended without a concurrency check at all.

A single `append` call may not carry more than **100 events** — a fixed constant, not configuration, since it encodes an architectural boundary rather than a performance trade-off: a Decision Model appends the handful of events produced by one business decision, not a batch. A `POST /append` exceeding this is rejected with `400 Bad Request`. Writing many events at once — a data migration, a bulk import — isn't a `POST /append` use case (see Response format).

On success, the server responds `200 OK` — not `201 Created`, since there is no individually addressable resource to point a `Location` header at, consistent with `read`/`append` not being modeled as a REST resource. The body confirms the Sequence Position and `time` assigned to each event, in the order they were submitted:

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

A malformed request responds `400 Bad Request`; an event over the size limit responds `413 Payload Too Large` (see Event size limit and Error responses).

### Event size limit

A single event may not exceed **64 KiB**, measured as the combined UTF-8 byte length of its `type`, `identifiers`, `metadata`, and `payload` — not a character count, so multi-byte characters (accented text, for instance) count for more than one byte each. An `append` carrying an event over this limit is rejected with `413 Payload Too Large`.

The limit is deliberate, not a technical ceiling to raise later: it keeps an event a short, meaningful assertion about the world rather than a data transport container, and keeps Decision Model replay — which can reload hundreds of thousands of events — fast. Larger content (files, documents) belongs in external storage, referenced from the event rather than embedded in it.

Real-world event sizes stay far under the limit, with comfortable headroom over real-world usage rather than being a ceiling anything is currently pushing against (see Production reference data for the numbers).

The default of 64 KiB is configurable (see Configuration).

### Error responses

Every error response uses the same JSON envelope:

```json
{ "error": "InvalidRequest", "message": "afterSequence must be a non-negative integer" }
```

`error` is a stable code a client can branch on; `message` is a human-readable detail, included when it helps diagnose the problem and omitted when it wouldn't add anything (as with `ConcurrencyException` above).

`QUERY /read` and `POST /append` both respond `400 Bad Request` for any malformed or invalid body: invalid JSON, a `query` / `condition.failIfEventsMatch` that is neither an array of `QueryItem` nor `"*"`, an empty array anywhere the Query grammar requires a non-empty one (see Query grammar), a non-integer `afterSequence` or `limit`, a `limit` above the configured maximum (see Pagination), an invalid `time.from` / `time.before` timestamp, an event missing its `type`, an event carrying a duplicate identifier or metadata value or more than 20 identifiers/metadata entries (see Metadata), an `append` with more than 100 events (see Appending events), and so on.

Validation is hand-written in Go, not driven by a JSON Schema: the request surface is small, several rules are semantic rather than purely structural (a valid ATOM timestamp, a consistent `time.from`/`time.before` range, the full DCB `QueryItem` grammar), and a generic schema validator's error messages don't map cleanly onto the `{error, message}` envelope above.

## Append Condition and concurrency

Standard DCB flow:
1. `read(query)` — read the relevant events, keep the last `sequence` read (`afterSequence`)
2. Decide on the new events to write (Decision Model)
3. `append(events, condition: {failIfEventsMatch: query, afterSequence})`
4. The operation fails if an event matching `query` exists after `afterSequence`

## Concurrency handling in Go

### Principle: the reservation manager

A single central goroutine (the "gatekeeper") knows at all times the full set of Append Conditions currently held by in-flight writes: for each one, the `QueryItem`s of its `failIfEventsMatch` Query, and the type, identifiers, and metadata of each new event it is about to write.

**Flow for a write (append) request:**
1. The HTTP handler sends its Append Condition (Query + new events) to the gatekeeper via a channel, and waits
2. The gatekeeper checks it against every currently held reservation (see below):
    - No conflict with anything held → the reservation is granted
    - A conflict is found → the request is queued until the conflicting reservation is released
3. The handler executes the SELECT (verification) + INSERT in a SQLite transaction
4. The handler releases its reservation with the gatekeeper (`defer`)
5. The gatekeeper re-checks the queued requests against what's now held, and wakes up any that no longer conflict

**Deadlock-free by construction:** the gatekeeper always grants a complete reservation at once, or none at all — never a partial one. Since a single process centralizes the decision, there is never a situation where two requests each hold part of what the other needs, which eliminates any deadlock risk without needing to sort or hash anything.

**Queue order:** the gatekeeper serves queued requests FIFO — in arrival order, regardless of which ones could be granted sooner. This is a detail purely internal to the gatekeeper's own bookkeeping of its wait queue: it's invisible to the HTTP API and independent of the conflict-detection logic, so the scheduling policy can be revisited later without touching anything else.

**Cancellation while queued:** a request waiting in the gatekeeper's queue watches its own HTTP request context. If the client disconnects or the request times out before a reservation is granted, the handler notifies the gatekeeper to drop it from the queue immediately — freeing its spot for other waiters and skipping the SELECT+INSERT entirely, rather than doing that work only to fail writing a response to a connection that's already gone.

### Determining whether two reservations conflict

Two reservations, A and B, conflict if and only if:

```
(a new event of A matches a QueryItem of B's Query)
OR
(a new event of B matches a QueryItem of A's Query)
```

"Matches" is the standard DCB matching predicate — the same one used for `read()`: an event matches a `QueryItem` if the event's type is one of the item's `types` (or the item specifies no types), AND the item's identifiers are a subset of the event's identifiers, AND the item's metadata are a subset of the event's metadata.

A Query-to-Query overlap on its own is not a conflict: two reservations that check the same condition but write events unrelated to that condition never actually invalidate each other's decision, and serializing them anyway would only cost throughput for no correctness benefit. The only thing that can compromise an Append Condition is a *newly written event* satisfying a Query another in-flight writer already based its decision on — so only new-event-against-Query checks matter.

Because the number of reservations held at any moment is small — bounded by concurrent HTTP throughput, not by the size of the store — this conflict check is a direct comparison against each in-flight reservation's actual identifiers and metadata, not a precomputed key lookup. This avoids a subtle trap: an identifier or metadata value present on nearly every event of a given scope (for instance one added automatically to every Query by a middleware) would otherwise become a systematic false-positive collision point, serializing writes that never actually conflict.

### Behavior based on condition overlap

- **Conflicting conditions** (a new event of one matches a `QueryItem` the other is checking) → the writes execute **sequentially**: the second waits for the first to release.
- **Non-conflicting conditions** (e.g. an event tagged `courseId:123` vs. one tagged `courseId:456`, where neither Query cares about the other's identifier) → the writes execute **in parallel**, each granted immediately.

### Reads

Reads do **not** go through the reservation manager. The DCB spec only requires locking on append. A `read(query)` queries SQLite directly.

Consistency is guaranteed by SQLite's **MVCC** mode (WAL): a read sees a consistent snapshot at the moment it starts, never seeing a partially committed write — no matter how far along that write is. A read started just before an append finishes simply won't see that new event, which is correct since the client will use the actually-read `Sequence Position` as `afterSequence` for its next append anyway.

**Write atomicity:** writing an event involves several INSERTs (the `events` row, one or more identifier rows, one or more metadata rows). These INSERTs must be wrapped in an explicit SQLite transaction, to guarantee that an event, its identifiers, and its metadata become visible together, never in an intermediate state.

**Sequence Position assignment:** the `events` table's primary key is a SQLite `INTEGER PRIMARY KEY AUTOINCREMENT` column, which *is* the Sequence Position — SQLite assigns it at INSERT time, strictly increasing with each row. The gatekeeper never tracks or assigns it itself; only the actual commit order matters.

### Startup and crash behavior

The gatekeeper's reservation state is purely transient, held only in memory for the lifetime of the process. Nothing is persisted and nothing needs to be rebuilt on startup: a freshly started process begins with an empty reservation table, which is correct by construction — every reservation that existed before a crash belonged to an HTTP request whose client connection is now gone too.

A panic in a single request handler is recovered by the HTTP server without crashing the process; the handler's deferred reservation release still runs during the panic's unwind, before that recovery happens, so no reservation is left held. A full process crash (an unrecovered panic, SIGKILL, an OOM kill) takes the entire in-memory reservation state down with it, so there is nothing left to leak either way.

SQLite's own atomicity guarantees the store itself: a crash mid-append leaves an incomplete WAL transaction, detected and discarded by its per-frame checksums the next time a connection opens. The event, its identifiers, and its metadata never become visible in a partial state.

None of this tells the client whether its append actually happened, though: the crash (or any dropped connection) can land after SQLite commits but before the `200 OK` reaches the client, which is the same lost-acknowledgment ambiguity as any request/response protocol over an unreliable connection — SQLite's guarantees don't extend to the response. A client that wants to retry safely after a dropped connection should always attach an Append Condition, even one with no `failIfEventsMatch` — an `afterSequence` on its own is enough: if the original append actually succeeded, the Sequence Position has already moved past it, so the retry fails with `409 Conflict` instead of writing a duplicate event.

**Fatal storage errors:** a SQLite error indicating the file itself may be compromised — an I/O error, detected corruption, failure to open the database file — is treated as fatal: the process logs it and exits, rather than trying to keep serving requests against a store it can no longer trust. This is deliberately simple to reason about: no per-error-code recovery logic, just a clean restart, which is cheap and safe given the transient state described above, and which `/health` and a process supervisor are already positioned to detect and act on. A transient, non-fatal SQLite error (a busy lock during a WAL checkpoint, for instance) doesn't fall into this category — it's handled locally within that one request rather than taking the whole process down for every concurrent client.

**Guaranteed release on failure:** for any error that isn't fatal, the handler's reservation release is deferred immediately after the reservation is granted — before the SELECT+INSERT even runs — so it executes regardless of how that code exits: a returned SQLite error, a panic, or ordinary success. A reservation is never left held by a request that failed for a reason unrelated to its Append Condition.

## Storage: SQLite

SQLite is used as the storage engine, for the following reasons:
- Volume (~2.5-5M+ rows across the identifiers and metadata tables) comfortably within SQLite's capabilities with `name + value` indexes
- No external network/process dependency, consistent with the goal of centralizing state in memory on the Go side
- Single-writer semantics, consistent with the single-process model ("single brain") — the process is the only writer for as long as it's running
- WAL mode allows concurrent reads without blocking
- A standard, inspectable file format: the database can be opened and queried with ordinary SQLite tooling, not a proprietary or opaque format, and backed up the same way — through SQLite's own backup mechanism (e.g. `.backup`, `VACUUM INTO`) rather than a raw copy of the main file, which can miss commits still sitting in the WAL

### Schema

```sql
PRAGMA user_version = 1;

CREATE TABLE events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
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

`time` is stored as `TEXT`, not as an integer epoch: its fixed-width ATOM format sorts lexicographically in the same order as chronologically, so no conversion is needed between what's stored and what's returned to the client.

`identifiers` and `metadata` are `WITHOUT ROWID` tables keyed by their natural composite primary key `(event_sequence, name, value)` — these are pure associative rows, so a separate rowid would just be a redundant btree. The secondary index `(name, value, event_sequence)` on each table is what serves the DCB matching predicate directly, with `event_sequence` included for an index-only scan.

The `events(sequence)` foreign key on both tables is enforced by setting `PRAGMA foreign_keys = ON` on every connection at startup — SQLite parses foreign key declarations but does not enforce them by default. Enforcing it catches implementation bugs (e.g. an identifier or metadata row written with a `event_sequence` that doesn't correspond to an actual event) rather than serving any functional requirement, since the store is append-only and single-writer.

Two more pragmas are set on every connection at startup, alongside `foreign_keys`: `PRAGMA journal_mode = WAL` — the mode this design assumes throughout for MVCC reads and checkpoint behavior — and `PRAGMA synchronous = FULL`. `FULL` costs an extra fsync per commit compared to the `NORMAL` mode WAL usually pairs with, but at this scope's write volume (see Production reference data) that cost is negligible, and it buys the strongest durability guarantee SQLite offers for what is, for each application, its single source of truth with no replication behind it.

Checkpointing relies on SQLite's own automatic passive checkpoint — triggered on its own once the WAL crosses its default size threshold, non-blocking to any concurrent reader or writer — rather than a separate checkpoint goroutine or schedule. This is exactly what bounded pagination on `/read` protects (see Live projection rebuilds): a long-held read transaction can stall that automatic checkpoint for as long as it runs, but nothing about the checkpoint itself needs to be triggered manually once reads stay short.

On startup, the process reads `PRAGMA user_version` and compares it against the schema version built into the binary. A database file that doesn't exist yet is created fresh, with the schema above establishing it at the current version. An existing file whose version doesn't match — older, from a schema that's since changed, or newer, from a downgraded binary — is fatal: the process logs it and refuses to start, the same treatment as any other storage integrity failure (see Startup and crash behavior).

Migrating an existing database from one schema version to the next is the job of a separate binary, not the TamarackDB process itself — a dedicated migration tool run once, deliberately, between a schema change and the next deployment of the main binary. The TamarackDB server never migrates a schema on its own.

## Configuration

TamarackDB's startup configuration — bind address, port, TLS settings, auth token, database path, and pagination/event-size limits — is resolved from three sources, in order of precedence:

1. A JSON configuration file, passed via `-config` (defaults to `config.json` in the working directory).
2. `TAMARACKDB_*` environment variables, one per configuration key.
3. Built-in defaults, for the handful of keys that have one (`defaultLimit`, `maxLimit`, `maxEventSize`).

A value set in the configuration file always wins over the matching environment variable. The configuration file itself is optional: an application deployed as one instance per environment, each with its own file, uses it as the single source of truth; a container deployment with no file at all is configured entirely through the environment instead. Both paths produce the same `Config`, and every field is validated the same way regardless of where it came from (see below).

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

## Security

TLS and Bearer-token authentication are each controlled independently by their own boolean, `enableTls` and `enableAuth`, so a deployment matches its own network trust boundary instead of the store enforcing one fixed posture. Both default to disabled, for a deployment where the network itself is already isolated upstream — a private network segment, a VPN, a firewall boundary — making TLS and per-request auth redundant weight on top of a trust boundary already enforced elsewhere.

When `enableTls` is true, the bind address, port, and TLS certificate/key paths are all set the same way (see Configuration); the Go process terminates TLS itself via `ListenAndServeTLS`, with no reverse proxy in front of it. When `enableTls` is false, the process serves plain HTTP on the configured bind address and port.

When `enableAuth` is true, every endpoint — `read`, `append`, `/health`, and any nice-to-have observability endpoint (`/metrics`, `/debug`) — requires a Bearer token in the `Authorization` header (`Authorization: Bearer <token>`). The token is a single static value, defined as `authToken`. A request without a valid token is rejected with `401 Unauthorized` before reaching any handler logic. Rotating the token means changing the configuration file or environment variable and restarting the process — there is no in-memory rotation or multi-token acceptance window, consistent with the gatekeeper's own transient, in-memory state. When `enableAuth` is false, the API is served with no authentication at all.

A single token, with no per-client scoping, is sufficient because a TamarackDB instance has exactly one trusted caller: the owning application. If that application is itself multi-tenant, tenant isolation is its own responsibility, enforced using the `tenantId` metadata already carried by events — it is not something TamarackDB's authentication layer needs to provide.

## Management / observability features

### Health check

A lightweight `GET /health` endpoint confirming the process is responsive and SQLite is reachable (e.g. a trivial `SELECT 1`), for use by a process supervisor or load balancer — even though this is a single-instance service, a health check is still useful for restart/alerting logic. On success it responds `200 OK` with a small JSON body, `{"status": "ok", "version": "1.2.3"}`; on failure to reach SQLite, `503 Service Unavailable` rather than `500` — the conventional signal a supervisor or load balancer already expects for "not ready right now", distinct from the `500` an ordinary request failure returns elsewhere in the API.

### Versioning

The running build's version is a single value, read from the `VERSION` file at the root of the repository and baked into the binary at build time via `-ldflags "-X main.version=..."` — not something the process reads or reloads at runtime. `./tamarackdb -version` prints that value and exits immediately, without loading the configuration file or opening the store, for a quick check of what's actually running without having to reach it over the network. That same value is what `GET /health` reports in its `version` field above.

### Nice to have: gatekeeper observability

Event and row counts, per-type breakdowns, database file size — anything derivable from the store's own content — are a query away against the SQLite file directly, so the store doesn't need to expose them itself. What the file can't answer is the gatekeeper's own live, in-memory state, which only exists for the lifetime of the process. Two endpoints cover that, kept separate because they serve different needs:

**`GET /metrics`** — Prometheus exposition format, for scraping into existing monitoring:
- Number of currently held reservations (gauge)
- Number of requests currently queued, waiting for a reservation (gauge)
- Longest current wait time among queued requests (gauge)
- Throughput counters: appends granted/sec, appends failed (ConcurrencyException)/sec (counters)

**`GET /debug`** — a JSON snapshot for investigating a specific stuck or slow append, too structured to fit a metric:
- List of currently held reservations (their Query and pending new events) and their age
- The queue of pending append requests

Since the gatekeeper already serializes access to its state via its own channel, answering either request is just another message type it can handle — no separate locking needed to read a consistent snapshot of its own counters. Both are read-only: no endpoint allows forcibly releasing a reservation or otherwise mutating the gatekeeper's state, since that would reintroduce the exact race conditions the gatekeeper exists to prevent.

## Implementation

The concrete Go structures behind the reservation manager, the Query → SQL translation, and the schema migration tool are implemented, in `internal/gatekeeper`, `internal/store`, and `cmd/migrate` respectively.

## Production reference data

Figures cited throughout this document as justification are grounded in two real production systems, both measured as of September 2026.

**System A** — a DCB application:
- ~2.5 million events, accumulated over 8 years
- 58 projections; the heaviest reads roughly 945,000 of those events on a rebuild
- ~2.5-5 million rows across the identifiers and metadata tables combined, at 1 to 2 identifiers per event on average
- Write throughput averages roughly 850 events/day over its lifetime
- Tracing (total events processed divided by the rebuild script's running time) shows its fastest projection processing upward of 999 events/second during a rebuild

**System B** — built on aggregates rather than DCB; its numbers still ground the read/rebuild figures above, since replaying events works the same way regardless of the consistency model that produced them:
- ~959,000 events
- Average combined size of `type` + `payload` + `metadata`: ~1,700 characters
- Largest event ever recorded: 14,576 characters (`BenefitUpdatedEvent`), roughly 22% of the 64 KiB event size limit
- Only two event types ever exceed 10,000 characters