---
title: "Architecture"
slug: "architecture"
weight: 2
---

## Context

TamarackDB is an event store in Go. It follows the [DCB (Dynamic Consistency Boundaries)
specification](https://dcb.events/specification/), is reachable over HTTP, and uses SQLite as its storage engine. The
service runs as a single instance ("single brain"), not a multi-instance cluster.

It also stores projections: projections an application writes in the same write as the events that changed them (see
Projections). An application that keeps its projections elsewhere never has to touch this mechanism.

Applications can share a single TamarackDB instance when they share events. TamarackDB does not track which
application produced an event.

### Name

TamarackDB takes its name from the tamarack (*Larix laricina*), a conifer native to Quebec's boreal forest. It's one
of the few conifers used in dendrochronology, because its growth rings are unusually clear and easy to read. Each ring
records one season, laid down once and never changed. You can read the tree's whole history by reading the rings from
the center out. This event store works the same way: an ordered, append-only list of facts that never change, from
which you rebuild current state by replaying them.

### Scope

TamarackDB serves applications with modest throughput. Every design choice here follows from that scope: one SQLite
file, one writer, no clustering. It trades speed for simplicity, on purpose. A system that needs high write
throughput is not a good fit for TamarackDB.

Eventual consistency is supported, not imposed. An application can make each command atomic, with its events and
every projection they change written together, or let its projectors and processors catch up later, each in a
transaction of its own. The choice belongs to the application, through its client library, by where it ends its
transactions. The server knows nothing about it.

### Transactional model

A transaction lives in the client library, not on the server. While a command runs, the library keeps what the
command wants to write in memory: new events, the Append Conditions they depend on, and projection changes. Reads
made inside the transaction see those pending writes, merged into what the server returns. When the transaction ends,
the library sends everything in one `POST /write`.

The server checks the write and applies it in one SQLite transaction of its own: every Append Condition must hold, and
every projection must still be at the version read. Then it appends the events and writes the projections, or writes
nothing at all. The server keeps nothing between two requests: no open transaction, no pending write, no timer.

Many transactions run at the same time, one per thread or request of the application. None of them holds anything on
the server while it runs: reads never wait, and a slow client blocks no one. Conflicts between transactions are found
when each write arrives, one write at a time, by its Append Conditions and projection versions. A transaction that
became stale learns it then, with `409 ConcurrencyException`.

Decision models and event handlers are code in the application, not in TamarackDB: the server stores what they read
and write, and never runs them.

### The courtyard

A picture helps to reason about this model. It follows [Pull-The-Plug
Modeling](https://maximegosselin.com/posts/pull-the-plug-modeling/): imagine the work done with no electricity, by
people with paper and pencils, to reason about concurrency without getting lost in technical details.

- **The courtyard is the application, and the people are its threads or requests.** The application decides how many
  people come into the courtyard, not TamarackDB.
- **The board and the bulletin board.** The board is large, on wheels, and it pivots; its name is written at the top of
  the side that faces the courtyard. On it is the log of events, numbered in the order they're added (`sequence`).
  Pages are glued end to end: whoever reads the board sees one long list of events, not pages. Nothing is ever taken
  off the board. On a huge bulletin board are the projections: one card pinned per projection, with a version number
  that changes on every write. A card can be replaced or taken down. Everyone can look at both, but only the clerks
  write on them. People keep their backs to them, and only turn around when they really need to read. No one is told
  when the clerks write.
- **The notebook.** Starting a transaction is taking a blank notebook. A person holds one at a time, and no one else
  sees it. In it, the person writes down their wishes, with no numbers: only events glued to the board have one. When
  they look at the board or the bulletin board, they add in their head what's in their notebook. They can throw the
  notebook away at any time; handing it to the head clerk is the write. Either way, they no longer have a notebook.
- **A notebook that stands on its own.** The head clerk knows nothing of what the person did before reaching them:
  everything to check is written in the notebook. Next to each wish for events, the person notes what the decision
  rests on: the board's name and the number they read it up to (`afterSequence`), and which events would have changed
  their mind (`failIfEventsMatch`). A wish that rests on no reading notes neither name nor number: it holds on any
  board. For each card they want to change, they note the version they read.
- **The bookmark.** One person can follow several lines of thought in the same transaction (the processors). When they
  look at the board for one of them, they slip a bookmark into the notebook. Before writing down the wish that comes out
  of it, they read again what was added to the notebook after the bookmark. If one of those additions bears on the
  decision, they tear the notebook up: they decided on a stale view, and the whole notebook is suspect. This is the
  only check that falls to them: only they know their notebook in order.
- **A projector is a person like any other.** In an eventually consistent application, the person playing a projector
  wears a watch that reminds them, now and then, to go look at the board: no one tells them when the clerks write.
  They take a notebook (it costs nothing), read the card holding their marker on the bulletin board (the board's name
  and the last number processed) and note its version, then look at the board after that number. If they find new
  events, they write down the cards to create, replace, or take down, and their new marker, and get in line.
  Otherwise, they throw the blank notebook away. Their notebook never holds new events: the ones they project are
  already on the board. A newcomer with no marker card reads the board from the start: they see its name there, and
  note their first marker with the last number processed. A rebuild goes the same way, in one notebook or several.
- **A refused projector starts over.** If the head clerk refuses a projector's notebook because a card is no longer at
  the version noted, nothing is written, neither the cards nor the new marker, since they go together. The person
  throws the notebook away and starts again at the next tick of the watch, from their marker card: if someone moved
  that marker in the meantime, they pick up where it stopped, with no event projected twice or skipped. A card doesn't
  carry the name of who wrote it: the head clerk can't stop two people from touching the same ones.
- **The head clerk, in a corner of the courtyard, handles notebooks one at a time.** People who are ready line up in
  front of them. Once in line, a person no longer touches their notebook. They can leave the line; the person behind
  them then takes their place.
- **Only the head clerk decides, and only once.** They check what the notebook notes against the board and the
  bulletin board as they are at that moment. If the board's name isn't the one the notebook notes, if an event added
  after the noted number would have changed the decision, or if a card is no longer at the version noted, they refuse
  the whole notebook (`409`). Otherwise, they have everything written. No one tries to guess the verdict: turning
  around to look at the board just before getting in line would prove nothing, since the board can change during the
  wait. Refused, the person leaves the line. If they want, they start over: take a blank notebook, look at the board,
  decide, write it down.
- **Under-clerks who act together.** The head clerk directs under-clerks: one who glues to the board, after the last
  page, a page holding every event of the notebook, and one for each card touched on the bulletin board (created,
  replaced, or taken down). They all wait for the order, then act together. No one ever sees a write half done: the
  whole notebook appears at once, or nothing does (the SQLite transaction).
- **Leaving before or during the write.** The person must still be there when the head clerk gives the order to write;
  if they left before, nothing is written. Once the order is given, the under-clerks finish, even if the person
  leaves. They then don't know whether their notebook was written, and it's up to them to deal with it.
- **Taking cards down in bulk.** A person with no notebook can ask the head clerk to take down every card of one type,
  or the whole bulletin board. They get in line like everyone else: the notebooks that arrived before them are written
  first, and the removal applies to everything accepted before it.
- **Turning the board around (dev mode only).** A person with no notebook can ask for the board to be turned around.
  The head clerk handles the request in its turn, like a notebook: the ones that arrived before it are written on the
  visible side, then the board pivots. The side that comes up is blank and carries a new name, and no one can read the
  old side any more. The bulletin board is cleared at the same time. A notebook that notes the old name will be
  refused. Those who keep a marker elsewhere (a projector that remembers the name and the last number read, for
  example) see that the name changed, and start over from zero.

In the courtyard, people make writes. Committing is what the SQLite transaction does inside one write.

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
DCB specification's own rule that a set of Tags should not contain duplicates. A write that breaks this rule gets
`400 Bad Request`, instead of being silently deduplicated, like every other invalid request (see Error responses).

An event can't carry more than **20 identifiers**, or more than **20 metadata** entries. These are fixed limits, not
configuration: an event should stay a short, meaningful statement, not a container for a large list of values. A
write that breaks this rule gets `400 Bad Request`.

### Store ID

Every database file has a store ID: a UUID drawn when the file is created, kept in the single row of the `store`
table (see Schema), and drawn again by `POST /reset` (see Reset). Two responses that carry the same store ID come from
the same history. A Sequence Position only means something next to the store ID it was read with: after a reset,
sequence 5 names a different event. A position kept by a client is therefore a pair, the store ID and the Sequence
Position.

The store ID is in the `X-Tamarackdb-Store` header of every response that depends on the store: `QUERY /events` on
every page, empty ones included, `GET /projections/{type}/{id}` on both `200` and `404`, and `POST /write` on `200`. A
read takes it in the same SQLite snapshot as the events or the projection, so a response never pairs the data of one
store ID with another. A write takes it in the same SQLite transaction as what it wrote. A request refused before it
reads or writes (`400`, for example) carries no store ID.

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

Values are compared exactly, byte for byte: case counts, and no Unicode normalization happens.

Negation (`<>`, and so on) is not allowed. It's left out on purpose: a negation describes an unlimited set of matching
events ("anything that isn't X"), so there's no way to guarantee that no future event could break the condition.

`afterSequence` can be past the last matching event: this number is just what the client had seen, not necessarily
a real event. The concurrency check compares `sequence > afterSequence` against matching events.

Every array in this grammar (the top-level `query`, or a `QueryItem`'s `types`, `identifiers`, or `metadata`) must be
non-empty when present. An empty array gets `400 Bad Request`, rather than meaning something special. To leave an axis
open within a `QueryItem`, leave that key out entirely, instead of sending `[]`. To match every event, use `"*"` in
place of `query`, instead of an empty array.

A `QueryItem` must specify at least one of `types`, `identifiers`, or `metadata`. An empty item (`{}`) is invalid and
gets `400 Bad Request`: it poses no constraint.

A Query carries at most **100** `QueryItem`, and a `QueryItem` at most **100** values across its `types`,
`identifiers`, and `metadata` combined. These are fixed limits, not configuration: they keep the SQL a Query turns into
well inside SQLite's own limits on expression depth and bound parameters, which a Query of about 1,000 terms would
otherwise hit, failing inside SQLite instead of getting a clear `400 Bad Request`. They apply to `query` and to a
condition's `failIfEventsMatch` alike, counted after duplicate items are dropped.

If two `QueryItem` in the same array are exact duplicates (same types, identifiers, and metadata, in any order),
TamarackDB silently keeps one and drops the rest: a repeated item adds nothing to the OR beyond a wasted clause. This
applies to `query` on `QUERY /events` and to `failIfEventsMatch` on `POST /write` alike, since both use this same
grammar. It's useful when a client builds one Query by merging several sources (several models reacting to the same
identifier, for example) and doesn't want to bother deduplicating them itself first.

A client library matches its own pending events against a Query, in memory (see Transactional model). Its matcher must
follow this grammar exactly, or a command could miss one of its own pending events without any error. The repository
publishes shared test cases for that, `testdata/query-cases.json`: each case is a query, an event, and whether it
matches. A test in `internal/store` checks every case against the SQL the server runs; a client library replays them
against its matcher.

## HTTP API

Every endpoint is named by the resource it acts on (`/events`, `/projections`) or by what it does (`/write`,
`/reset`). No request belongs to a transaction on the server: each one stands on its own.

| Endpoint | Purpose |
|---|---|
| `QUERY /events` | Read committed events |
| `GET /projections/{type}/{id}` | Read one committed projection |
| `POST /write` | Check Append Conditions, append events, and write projections, all or nothing, in its turn |
| `DELETE /projections/{type}` | Delete every projection of one type, in its turn |
| `DELETE /projections` | Delete every projection, in its turn |
| `POST /reset` | Delete all events and projections and draw a new store ID, in its turn (dev mode only) |

"In its turn" means the request waits in the write FIFO (see Concurrency handling in Go). `GET /health`, `GET
/metrics`, and `GET /debug` are covered in Management / observability.

### Writing

`POST /write` carries one write: the events to append, the Append Conditions they depend on, and the projection
changes:

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
  "conditions": [
    {
      "failIfEventsMatch": [ ... ],
      "afterSequence": 12345,
      "store": "5b0c7e2a-1f4d-4a9b-8c3e-6d2f1a0b9e47"
    }
  ],
  "projections": {
    "create":  [{ "type": "user-list-entry", "id": "789", "payload": "..." }],
    "replace": [{ "type": "user-profile", "id": "123", "version": "9f3c...", "payload": "..." }],
    "delete":  [{ "type": "user-list-entry", "id": "456", "version": "1b2c..." }]
  }
}
```

Every key is optional, and a missing one is an empty list. The body is decoded strictly: an unknown key, at any level,
gets `400 Bad Request`, since a misspelled optional key would otherwise drop a whole list without a word.

The body is read and checked in full before the write joins the FIFO: a client sending its body slowly never holds
the turn, and an invalid body gets `400 Bad Request` without waiting. When its turn comes, the write runs in one
SQLite transaction:

1. Every Append Condition is checked, in order, against the events committed before this write (see Append Condition
   and concurrency). The first that doesn't hold ends the write.
2. The projections are written, each one conditional on its version (see Projections). The first that doesn't hold
   ends the write.
3. The events get their Sequence Positions and their `time`, and are inserted.
4. The SQLite transaction commits.

Events and projections become durable together, or nothing is written. Conditions are checked even when the write
carries nothing else. A write with nothing at all (no event, no condition, no projection) responds `200` right away,
without joining the FIFO.

On success, the server responds `200 OK`, with the store ID in the `X-Tamarackdb-Store` header. The body gives the
Sequence Position and `time` of each event, in the order they were sent, and the new version of each created and
replaced projection:

```json
{
  "events": [
    {"sequence": 12348, "time": "2026-09-01T14:25:00.000000Z"}
  ],
  "projections": {
    "create": [{"version": "5a6b..."}],
    "replace": [{"version": "d4c3..."}]
  }
}
```

Every list is always present, empty if need be.

A condition that doesn't hold, or a projection that isn't at the version given, gets `409 ConcurrencyException`, with
a `message` naming the item by its place in the body: `conditions[1] no longer holds`, `conditions[0] was read on
another store`, `projections.replace[0] no longer has the given version`, `projections.create[0] already exists`.

**Limits.** A write carries at most `maxEventsPerWrite` events (100 by default), at most as many conditions, and at
most `maxProjectionsPerWrite` projections across the three lists (500 by default). Each event is at most
`maxEventSize` bytes, and each projection at most `maxProjectionSize` (see Event size limit). The body as a whole is
at most `maxRequestBodySize` (see Error responses). These limits count everything one transaction of the application
writes, since it all goes out in one write. Each error from a limit names its setting, for example `request carries
612 projections, more than maxProjectionsPerWrite (500)`, so a developer who hits one in development knows what to
ask the operator to raise.

**The client leaving.** A client that disconnects while its write waits in the FIFO leaves the FIFO, and nothing is
written. The server checks once more that the client is still there just as the turn comes. Once the SQLite
transaction has started, the write goes to the end, even if the client leaves: the transaction no longer depends on
the request's context. The client then can't tell whether the write happened.

**A lost response.** If the connection drops before the response arrives, the client can't tell whether the write
happened, and reading the events again doesn't settle it: finding nothing can mean "not written" or "not written yet".
A client that wants to retry safely sends the same write again, with the same Append Conditions, `afterSequence`
included. The FIFO serves writes in the order they arrive, so the retry runs after the first attempt. If the first
attempt went through, its events are past that position and match the condition, so the retry fails with `409
ConcurrencyException` instead of appending a duplicate event. Otherwise, the retry is written normally.

### Reading events

Events are read with `QUERY /events`, using the [HTTP QUERY method](https://www.rfc-editor.org/info/rfc10008/)
(RFC 10008): safe, idempotent, and cacheable like GET, but carrying a JSON body like POST. This is needed since a Query
can be too large or nested to fit in a query string.

The request body is a JSON object with a `query` key. That key holds either the array of `QueryItem` shown above, or
the literal string `"*"` for `Query.all()`, plus an optional `afterSequence` key:

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
  "afterSequence": 12345
}
```

`afterSequence` limits the read to events with a Sequence Position strictly greater than the given value, the same
`sequence > afterSequence` rule used by the Append Condition's concurrency check. It's optional: leaving it out reads
from the start of the store.

The body is decoded strictly: an unknown key, at any level, gets `400 Bad Request`. Every key but `query` is optional,
so a misspelled one would otherwise widen the read without a word.

There is no filter on `time`. `time` comes from the server's clock, which can jump back (see Response format), so
neither a decision nor a read should depend on it. An application that looks events up by period tags them when it
writes them (a `month` metadata entry, for example) and queries that tag.

A read runs on the read connection pool, outside any write. It sees committed events only, and never waits for a
write (see Reads). The response carries the store ID (see Store ID).

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

Both the default `limit` (`defaultEventsPerPage`, used when a request leaves it out) and the server-enforced maximum
(`maxEventsPerPage`, the highest `limit` a request may ask for) are configuration, not fixed constants (see
Configuration). How fast an application's projections can process a batch of events (see Projection rebuilds) varies
enough between applications, and even between projections in the same application, that one fixed page size wouldn't fit
all of them.

Left unset, `limit` falls back to a default of **1,000**, with a server-enforced maximum of **10,000**. That's sized
so a default page is easy to buffer client-side, and a page at the maximum still finishes in a matter of seconds even
for a fast projection. Asking for more than the configured maximum gets `400 Bad Request` (see Error responses).

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

`time` is when TamarackDB wrote the event, read from the server's clock during the write, in ATOM format (RFC 3339)
with exactly 6 fractional digits, always in UTC (`Z`). Every event of one write shares the same `time`: order within a
write comes from Sequence Position. The store has no timezone setting: `time` is a reference value, not something
meant for display. Converting to local time is left to the application.

`time` usually follows Sequence Position order, but nothing guarantees it. Day to day, NTP corrects a small drift
smoothly, by slowing the clock down or speeding it up, never by moving it back. A jump back is still possible: a large
gap corrected at once (often at boot), a virtual machine resumed, the time set by hand, a leap second handled badly.
Order always comes from `sequence`.

`payload` is an opaque string: the store never parses or checks it. Its real format (JSON, XML, or anything else) is
a convention owned by the writing application, based on the event's `type`. The store has no notion of it.

### Projections

A projection is the current state a projector computes from events: an opaque payload identified by `type` + `id`, with
no history. The `type` is like a class, and each projection is one instance of it. Unlike an event, a projection can be
overwritten or removed; the store only ever holds its current state. Every projection can be rebuilt from events,
which is why backups leave projections out (see Backup). Storing projections in TamarackDB is optional: an application
that keeps its projections elsewhere never has to touch it.

Projections live in the same SQLite file as events (see Storage: SQLite), and are written in the same write. Events
and projections become durable together, or neither does.

**Reading a projection**: `GET /projections/{type}/{id}` returns `404 ProjectionNotFound` if no projection exists for that
`type` + `id`, and `200` otherwise, with the payload as the response body, exactly as written. There's no JSON
envelope around it, since the payload's own format (JSON, XML, plain text) is up to the writing application, not
something the store imposes a wrapper on top of. The projection's version comes in the `X-Tamarackdb-Version` header
(see Versions below). The read runs on the read connection pool and sees committed projections only.

A projection is always read by `type` and `id`. There is no query over projections. In the URL, both are
percent-encoded as path segments: an `id` of `a/b` is written `a%2Fb`.

**Writing projections**: the `projections` object of `POST /write` creates, replaces, and deletes projections (see
Writing). Each list has a fixed shape: `create` takes `type`, `id` and `payload`; `replace` takes `type`, `id`,
`version` and `payload`; `delete` takes `type`, `id` and `version`. Three lists, rather than one list with an
operation flag, leave no combination of keys to forbid. A payload is a string, and an empty string is valid; a missing
or `null` payload gets `400 Bad Request`, so a key that goes missing on the client (JavaScript's `JSON.stringify`
drops `undefined` values) never writes an empty payload. The same `type` + `id` can't appear twice in one write,
across the three lists, so the order in which the lists are applied doesn't matter. Each projection is at most
`maxProjectionSize` bytes, measured like an event: the combined UTF-8 byte length of its `type`, `id`, and `payload`
(see Configuration).

**Versions.** Every projection has a version, a random UUID (version 4) generated on every write and stored in the
`version` column. Each write is conditional: a `create` inserts only if the `type` + `id` is free, and a `replace` or
`delete` touches the row only if its stored version is the one given. An entry that touches no row fails the whole
write with `409 ConcurrencyException`, with the entry named in `message` (for example `projections.replace[0]`). A
`delete` of a projection that no longer exists fails the same way: another write deleted it since the version was
read.

The check is what keeps two transactions from overwriting each other's projections: each one read a version, and the
second write to reach the head of the FIFO finds it changed. The version is opaque rather than a counter: a client
can't compute the next one instead of reading it, and since a random UUID never repeats, a stale copy never matches
again after the projection is deleted and created anew, or after a rebuild. A projection doesn't carry who wrote it,
so the server can't keep two projectors from touching the same projections: giving each projector its own types is
the application's rule.

**Deleting projections in bulk**: `DELETE /projections/{type}` deletes every projection of one type, and `DELETE
/projections` deletes every projection. Both wait for their turn in the FIFO like a write, and respond `204 No
Content`. A write queued before a bulk delete goes through first, and the delete then removes what it wrote. A later
write that replaces or deletes a projection the bulk delete removed gets `409`.

**What a projection may depend on.** It depends on where the application ends its transactions (see Scope):

- **Atomic**: projectors run before the write, while the events are still pending in the client library, so they have
  no `sequence` or `time` yet. A projection can't use them. A business date goes in the payload or the metadata.
- **Eventually consistent**: projectors read events that are already written, so a projection may use `sequence`
  and `time` too.

Either way, a rebuild reads the same events back from `QUERY /events`, so it produces the same projections.

### Projection rebuilds

A rebuild replays events to write projections again. How it's organized is up to the application: one thread
replaying every event in order, several projectors in parallel, or anything else. TamarackDB provides the calls it's
built from:

- `DELETE /projections/{type}` and `DELETE /projections` clear the projections to rebuild.
- `QUERY /events` pages through the events to replay.
- `POST /write` writes the rebuilt projections, in one write or several: a `create` the first time, then a `replace`
  with the version the previous write returned.
- `GET /projections/{type}/{id}` reads a projection back, with its version.

One write keeps the rebuild atomic, but it must fit under `maxProjectionsPerWrite` and `maxRequestBodySize`, and it
holds the turn for as long as its inserts take, with every other write waiting behind it. Several writes each fit the
limits; a projector writes its position (store ID and Sequence Position) with each one, so a rebuild that stops
halfway resumes from there. The choice is the application's.

Reads run side by side on the read connection pool. Writes each wait for a turn in the FIFO, so writes from several
threads interleave, one at a time. Each waiting write counts toward the FIFO depth: many writers at once can get `503
WriteQueueFull`, and retry.

Reads during a rebuild page through `limit`-sized requests instead of one response streaming the whole result set
over one long connection. Holding one SQLite read transaction open for a whole large rebuild would pin one MVCC
snapshot in place for as long as the rebuild runs, blocking WAL checkpointing that whole time while the rebuild's own
projection writes keep piling up in the WAL file. Paging keeps each read transaction short, so the WAL checkpoints
normally between pages.

Reclaiming the disk space of deleted projections with `VACUUM` is a separate, manual step, run while the server is
stopped (see Storage: SQLite).

### Reset (dev mode)

`POST /reset` deletes every event and every projection, sets the Sequence Position counter back so the next event
written gets sequence 1, and draws a new store ID (see Store ID), all in one SQLite transaction. It exists only when
`devMode` is on (see Dev mode). It responds `204 No Content`.

It waits for its turn in the FIFO like a write. The writes queued before it go through, then the reset deletes them.
A write queued after it runs on the new store: a condition that carries the old store ID gets `409`, as does a
`replace` or `delete` of a projection, whose version no longer exists; a condition with no `afterSequence`, or a write
with no condition, goes through, since it holds on any store. For the application, a reset is like a restart of the
server on a brand new file.

### Event size limit

A single event may not be bigger than **64 KiB**, measured as the combined UTF-8 byte length of its `type`,
`identifiers`, `metadata`, and `payload`, not a character count. Multi-byte characters (accented text, for instance)
count for more than one byte each. A write carrying an event over this limit gets `413 Payload Too Large`.

The limit is deliberate, not a technical ceiling to raise later: it keeps an event a short, meaningful statement about
the world, rather than a data transport container, and keeps Decision Model replay fast (which can reload hundreds of
thousands of events). Larger content (files, images) belongs in external storage, referenced from the event
instead of embedded in it.

The default of 64 KiB is configurable (see Configuration).

### Error responses

Every error response from an endpoint of the API uses the same JSON envelope:

```json
{ "error": "InvalidRequest", "message": "afterSequence must be a non-negative integer" }
```

`error` is a stable code a client can check. `message` is a human-readable detail, included when it helps figure out
the problem, left out when it wouldn't add anything (as with `WriteQueueFull`).

A few responses come from Go's `net/http` before any endpoint runs, and carry plain text instead: `404` for an unknown
path, `405` for a known path with the wrong method, and the errors `net/http` itself returns for a malformed HTTP
request or oversized headers.

| Status | `error` | When |
|---|---|---|
| `400` | `InvalidRequest` | Malformed or invalid body, see below |
| `401` | `Unauthorized` | Missing or invalid Bearer token, only when `enableAuth` is on (see Security) |
| `404` | `ProjectionNotFound` | `GET /projections/{type}/{id}` for a projection that doesn't exist |
| `409` | `ConcurrencyException` | `POST /write`: an Append Condition doesn't hold or was read on another store, or a projection doesn't match the stored one. Nothing was written |
| `413` | `PayloadTooLarge` | An event or a projection over its size limit, or a request body over `maxRequestBodySize` |
| `500` | `InternalError` | An unexpected server-side failure |
| `503` | `WriteQueueFull` | A write, a bulk delete, or a reset, while the FIFO is at its configured depth |
| `503` | `ShuttingDown` | A request waiting in the FIFO, or arriving after it closed, while the server shuts down |
| `503` | `Unavailable` | `GET /health` only: SQLite can't be reached (see Health check) |

`QUERY /events` and `POST /write` respond `400 Bad Request` for any malformed or invalid body: invalid JSON, anything
after the JSON value other than whitespace, an unknown key at any level, a `query` or `failIfEventsMatch` that isn't
an array of `QueryItem` or `"*"`, an empty array anywhere the Query grammar needs a non-empty one, more than 100
`QueryItem` or more than 100 values in one `QueryItem` (see Query grammar), a non-integer `afterSequence` or `limit`,
a `limit` below 1 or above `maxEventsPerPage` (see Pagination), an event missing its `type`, an event carrying a
duplicate identifier or metadata value, more than 20 identifiers or metadata entries (see Metadata), a condition with
`afterSequence` and no `store` or the other way around, a projection missing its `payload` or `version`, a duplicate
`type` + `id`, more events, conditions, or projections than one write allows (see Writing), and so on. The `message`
names the item at fault by its place in the body, for example `events[3]` or `projections.create[0]`.

Every request body is capped at `maxRequestBodySize` (8 MiB by default), so a client can't make the server read an
unbounded body into memory before the per-event and per-projection limits are checked. Past the cap, the server stops
reading and responds `413 Payload Too Large`. The cap isn't checked against the other limits: a body can reach it
before every one of its items reaches its own. It's the real bound on a write, and the others are rules for each
item.

## Append Condition and concurrency

Standard DCB flow:
1. `read(query)`: read the relevant events, keep the last `sequence` read (`afterSequence`)
2. Decide on the new events to append (Decision Model)
3. `append(events, condition: {failIfEventsMatch: query, afterSequence})`
4. The operation fails if an event matching `query` exists after `afterSequence`

TamarackDB runs this flow optimistically. A read never locks anything. The decision is made on what was read, and the
write carries the condition that describes what the decision depends on. If another write appended a matching event
in between, the condition fails with `409 ConcurrencyException`, nothing is written, and the client reads again,
decides again, and retries.

A condition also carries `store`, the store ID its `afterSequence` was read with (see Store ID). A condition read on
another store ID than the current one fails without any SQL: its `afterSequence` names a position in a different
history. A condition with `afterSequence` must carry `store`, and a condition without it must not: it read nothing,
so it holds on any store. `failIfEventsMatch` is optional too: an `afterSequence` alone fails if any event at all
exists after it.

A write carries a list of conditions, and every one must hold. A transaction usually has one per decision: each
decision model or processor adds the condition its own read supports. This is more precise than one condition merged
with OR, and never a partial success: the first condition that fails ends the write.

The conditions are checked in the same SQLite transaction as the inserts, against every committed event. Only one
write ever runs at a time (see Concurrency handling in Go), and the write holds SQLite's write lock from its first
statement, so nothing can slip in between the checks and the inserts.

A decision rests on events, never on a projection. A write checks the projections it changes, by their version, and
nothing else: a projection the transaction only read is not checked, since the server never learns what a
transaction read. A projection can be stale the moment it's read. A decision that must hold is protected by an Append
Condition on the events it rests on.

The server checks conditions against committed events only. Inside one transaction of the application, a decision
can also go stale because of a pending event added after the read it was based on, by another processor of the same
command for example. Only the client library knows the order of its reads and pending events, so that check is the
library's (see Transactional model).

### DCB compliance

TamarackDB follows the [DCB specification](https://dcb.events/specification/):

| Requirement | Level | TamarackDB |
|---|---|---|
| Read events filtered by type and/or tags through a Query | MUST | `QUERY /events` |
| Read from a given Sequence Position | SHOULD | `afterSequence` on `QUERY /events` |
| Append one or more events atomically | MUST | Every `POST /write` commits atomically |
| Fail the append if an event matches the Append Condition, when one is given | MUST | `conditions` on `POST /write`, optional for the client |

TamarackDB is DCB compliant: its guarantee is exactly the one of the specification, no more and no less. Only what an
Append Condition or a projection version expresses is protected. A condition left out, or one narrower than the
decision it protects, leaves a race that no error reports. A broader guarantee is a business rule of the application,
not something the event store enforces.

## Concurrency handling in Go

### Principle: the write FIFO

A queue manager gives out the single turn on the write connection, strictly in the order requests arrive. Four kinds
of requests join its FIFO: `POST /write`, the bulk deletes of projections (`DELETE /projections`, `DELETE
/projections/{type}`), `POST /reset`, and the hourly `PRAGMA optimize` (see Storage: SQLite). It knows nothing about
what a request will read or write. Only two states exist: **Active** (at most one request at a time, the only one
allowed to touch the write connection) and **Queued** (every other request, waiting its turn in line). There is no
priority: every kind waits its turn the same way.

**Flow for a request in the FIFO:**
1. The HTTP handler reads and checks the request body first, outside the FIFO.
2. It asks the queue manager to join the line. If the FIFO is already at its configured depth (see Configuration), the
   request is turned away right away with `503 WriteQueueFull`, instead of joining.
3. Otherwise it waits, with its HTTP connection held open, until every request ahead of it is done. If the client
   disconnects, the request leaves the line right away, and everyone behind it moves up one spot. There is no other
   way out of the line.
4. When it reaches the head of the line, the handler checks once more that the client is still there, then runs its
   work on the write connection: a write, a bulk delete, a reset, or `PRAGMA optimize`. From that moment, the work
   runs with a context the client can no longer cancel: `database/sql` would otherwise roll the SQLite transaction back
   halfway if the client left.
5. When the work ends, the request gives the turn to the next one.

Nothing outlives the request: no transaction stays open between two requests, and nothing on the server has to
expire. A request holds the turn only for the time of its own work, usually a few milliseconds.

### Application-controlled Sequence Position

Since only one request ever touches the write connection, TamarackDB assigns the Sequence Position itself, in memory,
instead of leaving it to SQLite's `AUTOINCREMENT`.

`events.sequence` is a plain `INTEGER PRIMARY KEY`, with the value set by the application on insert (see Schema).
When the store opens, the process reads the current highest `sequence` in the `events` table, and keeps it in memory
as the next-sequence counter. An empty table starts the counter the same way `AUTOINCREMENT` would: the first event
gets sequence 1.

A write reserves its Sequence Positions only after every condition holds and every projection is written, never
before. It inserts its events with them in the same call. Unless the commit succeeds (the insert or the commit fails,
or the request panics), the write gives its positions back before it ends, so a failed write leaves no gap in the
sequence. `POST /reset` sets the counter back so
the next event gets sequence 1.

Knowing every event's sequence up front means a write's `events` rows can be written as one multi-row `INSERT`,
followed by one multi-row `INSERT` into `identifiers` and one into `metadata`, instead of a per-event round trip to
fetch an ID between each event and its tags. `PRAGMA foreign_keys = ON` is still checked right away, not deferred to
commit, so a row in `identifiers` or `metadata` still can't point to an `event_sequence` that doesn't exist yet in
`events`, within the same transaction.

**Skipping the conflict check when nothing was appended since the read.** If a condition's `afterSequence` equals the
counter's last-assigned value, no event exists past that point at all. `failIfEventsMatch` can't match anything,
whatever it is, so the SELECT that would otherwise check it can be skipped entirely. A bare `afterSequence` condition
(no `failIfEventsMatch`) never needs a SELECT at all: "does any event exist after `afterSequence`" can be answered
directly from the counter. The shortcut applies to each condition of a write on its own. It's purely internal: it
changes how the decision is reached, never the decision itself, or anything in the HTTP contract.

### Reads

A read doesn't go through the queue manager. It runs on the read connection pool. Consistency comes from SQLite's
**MVCC** mode (WAL): a read sees a steady snapshot of committed data from the moment it starts, and never sees a
write in progress. It's never blocked by a write. A read that starts just before a commit simply won't see the new
events, which is fine: a client that then writes with an Append Condition uses the Sequence Position it actually read
as `afterSequence`.

Each read runs in its own read transaction: it first reads the store ID, then the page or the projection. SQLite takes
the snapshot at the first statement, so both come from the same one. For a `QUERY /events` page, the transaction ends
once the page is fully sent.

A `QUERY /events` page holds its read connection, and pins its snapshot, until the page is fully sent. So each line of
the page gets 30 seconds to go out, renewed on every line: a page that keeps moving is never cut, however slow the
client, but a client that stops reading loses its connection after 30 seconds, and the read connection goes back to
the pool. Without this, `readPoolSize` stalled clients would block every read, `/health` included, and keep the WAL
from checkpointing. The client sees a page with no trailer, and resumes like after any dropped connection (see
Response format).

### Startup, shutdown, and crash behavior

On startup, before opening the store, the process prints a banner and its resolved configuration to stdout: bind
address, port, the socket path and mode, the auth flag (`authToken` itself is never printed), data directory, dev mode,
and the pagination, size, write, and queue-depth limits. This is a plain operational aid, for checking at a glance
what a given instance is actually set up to do, not a machine-readable format meant for parsing.

Opening the store checks the schema version (see Schema), then reads the current highest `sequence` in the `events`
table into the in-memory Sequence Position counter (see Application-controlled Sequence Position above), and the store
ID into memory next to it (see Store ID). Requests are served once the store is open.

On `SIGINT` or `SIGTERM`, the process shuts down in order. The HTTP server stops taking new connections
(`http.Server.Shutdown`, capped at 10 seconds). At the same moment, the queue manager closes: requests still waiting
in the FIFO are turned away with `503 ShuttingDown`, and so is any later one. A write already running finishes. The
HTTP server then finishes the requests still in flight, and the SQLite store closes last, releasing its connections
and the `.lock` file. The FIFO has to close first: a request waiting in it only ends once it gets its turn, so the
HTTP server would otherwise wait for it. A fatal storage error found mid-flight (see Fatal storage errors below)
drives this same ordered shutdown, instead of an abrupt exit. The HTTP server also sets `ReadHeaderTimeout` to 10
seconds, closing a connection that never finishes sending its request headers, instead of holding it open forever,
and `IdleTimeout` to 2 minutes, closing a keep-alive connection that has no request in flight for that long.

The queue manager's state (the request holding the turn, the requests waiting, the write counters) is purely
transient, held only in memory for the life of the process. Nothing is saved, and nothing needs to be rebuilt on
startup: a freshly started process begins with an empty FIFO, which is correct, since every client that was waiting
before a crash lost its connection too. Two pieces of state are rebuilt on startup, because they have to match what's
on disk: the Sequence Position counter and the store ID, read from the database.

A panic in one request handler is caught by the HTTP server, without crashing the process. The handler's deferred
rollback and the release of its turn still run during the panic's unwind, so no turn stays taken forever. A full
process crash (an unrecovered panic, SIGKILL, an out-of-memory kill) takes the whole in-memory state down with it, so
there's nothing left to leak either way.

SQLite's own atomicity guarantees the store itself: a crash during a write leaves an uncommitted WAL transaction,
discarded the next time a connection opens. Neither the write's events nor its projections ever become visible, in
whole or in part. The counter, read back from the database, matches.

**Fatal storage errors:** a SQLite error that suggests the file itself may be damaged (an I/O error, detected
corruption, failure to open the database file) is treated as fatal: the process logs it and exits, instead of trying to
keep serving requests against a store it can no longer trust. This is deliberately simple: no per-error recovery logic,
just a clean restart, which is cheap and safe given the transient state described above, and which `/health` and a
process supervisor are already set up to catch and act on. A temporary, non-fatal SQLite error (a busy lock during a WAL
checkpoint, say) doesn't count as fatal: it fails that one request, and nothing it would have written is kept,
instead of taking the whole process down for every other client.

## Storage: SQLite

SQLite is used as the storage engine, for these reasons:
- Volume (several million rows across the identifiers and metadata tables) fits comfortably within SQLite's limits,
  with `name + value` indexes
- No external network or process to depend on
- Single-writer behavior, in line with the single-process model ("single brain"): the process is the only writer for
  as long as it runs
- WAL mode allows reads to happen at the same time as a write, without blocking
- A plain, inspectable file format: the database can be opened and queried with ordinary SQLite tools, not some closed
  format, and backed up the same way, through SQLite's own backup tools (for example `.backup`, `VACUUM INTO`) instead
  of a raw copy of the file, which can miss commits still sitting in the WAL

Events and projections live in one file, `tamarackdb.sqlite`, so one SQLite transaction covers both. Projections are
larger than events and are rewritten in place, so they make the WAL grow faster than events alone would. This stays
small in practice: a write carries the projections of one command, typically about ten. The one heavy case is a
projection rebuild, which writes every rebuilt projection (see Projection rebuilds). SQLite reuses the space of
deleted projections for later writes on its own. Giving that space back to the operating system takes a `VACUUM`,
which rewrites the whole file, events included. The server never runs one: it's run by hand, with `sqlite3`, while
the server is stopped, the same way as a full `ANALYZE` (see below). Both run as the server's own user: the data
directory is readable by its owner only, and a WAL file left behind by another user is one the server can't open.
Stopping the server costs a few seconds, and keeps the server free of a long operation it would have to coordinate
with reads still in flight.

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

CREATE TABLE projections (
    type    TEXT NOT NULL,
    id      TEXT NOT NULL,
    version TEXT NOT NULL,
    payload TEXT NOT NULL,
    PRIMARY KEY (type, id)
) WITHOUT ROWID;

CREATE TABLE store (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    id        TEXT NOT NULL
);
```

`store` holds a single row, with the store ID (see Store ID). The `CHECK` on `singleton` keeps a second row out. The
row is written with the rest of the schema, in the same transaction.

`projections` is `WITHOUT ROWID`, keyed by `(type, id)`: a projection has no history, so its natural key is also its only
key, with no separate rowid needed. The same key serves `DELETE /projections/{type}`, as a prefix of the primary key.

`time` is stored as `TEXT`, not as an integer timestamp. Its fixed-width UTC format sorts the same way alphabetically
as it does chronologically, and nothing needs to be converted between what's stored and what's returned: a read passes
the stored text straight through. This only holds because
the format is strict: an offset, or a different number of fractional digits, would break the ordering (`05.123Z`
sorts after `05.123456Z`, since `Z` comes after every digit). The server always writes `time` itself, in that exact
format. No index covers `time`: nothing filters or orders on it.

`identifiers` and `metadata` are `WITHOUT ROWID` tables, keyed by their natural combined primary key
`(event_sequence, name, value)`: these are pure link rows, so a separate rowid would just be an extra, unneeded
btree. The secondary index `(name, value, event_sequence)` on each table is what serves the DCB matching check
directly, with `event_sequence` included so the index alone can answer the scan.

`events.identifiers` and `events.metadata` hold the same data again, in the compact object shape the HTTP API returns
(see Response format): a ready-made copy a read can hand back directly, without joining out to the
`identifiers`/`metadata` tables. A read hands these two columns to the client exactly as stored: it never decodes them
into Go values and re-encodes them, since the stored bytes already are the response bytes. Those tables stay what the
DCB matching check and a read's own filtering use, keyed for lookup by `name`/`value`; the columns on `events` are
keyed by nothing but the event itself, meant for handing the whole set back at once.

The `events(sequence)` foreign key on both tables is enforced by turning on `PRAGMA foreign_keys = ON` on every
connection at startup: SQLite reads foreign key declarations, but doesn't enforce them by default. Turning this on
catches implementation bugs (say, an identifier or metadata row written with an `event_sequence` that doesn't match a
real event), rather than serving any real functional need, since events are append-only with a single writer.

Two more pragmas are set on every connection at startup, next to `foreign_keys`: `PRAGMA journal_mode = WAL` (the
mode this design assumes throughout, for MVCC reads and checkpoint behavior) and `PRAGMA synchronous = FULL`. `FULL`
costs one extra fsync per commit compared to the `NORMAL` mode WAL usually pairs with, but at this scope's write
volume that cost doesn't matter, and it buys the strongest durability SQLite offers, for what is, for each
application, its single source of truth.

The write connection opens every transaction with `BEGIN IMMEDIATE` (`_txlock=immediate` in the DSN), taking SQLite's
write lock at the start of the transaction, rather than waiting until the first write statement runs. A write's
condition checks, projection writes, and inserts all run under the lock, with no window where another connection
could slip in between them. A bulk delete of projections runs as a single statement that commits on its own. Each
runs during its own turn in the FIFO, so it has the write connection to itself. The write connection also sets
`_busy_timeout = 5000` (five seconds). Since `writeDB.SetMaxOpenConns(1)` already forces every write onto one
connection, and the FIFO already lets only one request use it at a time, the busy timeout only guards against
something else briefly holding the file (a passive checkpoint, an external `sqlite3` shell), not against another
write.

Checkpointing relies on SQLite's own automatic passive checkpoint (triggered on its own once the WAL crosses its
default size, without blocking any reader or writer), instead of a separate checkpoint goroutine or schedule. Two
things can hold it back: a long read, which pins an MVCC snapshot (bounded by pagination, see Projection rebuilds,
and by the 30-second limit on a stalled page, see Reads), and a large write, whose changes can't be checkpointed
before it commits (a projection rebuild sent in one write, for example). Nothing about the checkpoint itself needs to
be triggered by hand.

Query planner statistics are kept up to date the same hands-off way: once an hour, the process runs `PRAGMA optimize`
on the write connection. It joins the FIFO like a write, so it never runs during one. `events` only grows, for the
life of a deployment that's never restarted, so statistics gathered once at some point in the past drift further from
reality the longer the process stays up. `PRAGMA optimize` is SQLite's own answer to this: cheap enough to run often,
since it only re-analyzes tables it judges to have changed enough to matter (or that have no statistics at all yet),
rather than scanning everything the way a plain `ANALYZE` does. A full `ANALYZE` is never run automatically; it's
still the right tool right after a one-off bulk import, run by hand while the server is stopped.

On startup, the process reads `PRAGMA user_version` and checks it against the schema version built into the binary.
A database file that doesn't exist yet is created fresh, with the schema above setting it at the current version. An
existing file whose version doesn't match (older, from a schema that's since changed, or newer, from a downgraded
binary) is fatal: the process logs it and refuses to start, the same treatment as any other storage integrity failure
(see Startup, shutdown, and crash behavior). The TamarackDB server never changes its own schema.

## Backup

`tamarackdb-backup` keeps a standing copy of an instance's events in local SQLite files, one per store ID of the
source. It does one catch-up run and exits; a scheduler runs it again (see [Backup](/docs/guides/backup/)). Each run:

1. Asks the source for its first event (`QUERY /events` with `afterSequence: 0` and `limit: 1`), only for the store ID
   in the response's header. The event itself is dropped, and the page is empty for an empty source. The store ID
   comes from the network, so it must be a UUID in its canonical form before it becomes a file name: nothing else can
   point outside the backup directory.
2. Creates `dataDir` if it doesn't exist yet, then opens `<store ID>.sqlite` in it with `store.Open`, the same path the
   server uses: a new file gets the server's schema, and the run holds the file's `.lock` (see Storage: SQLite). A
   backup file can't be updated while a server is serving it.
3. Reads the highest Sequence Position already in the file.
4. Pages through the source's `QUERY /events`, over HTTP to `sourceUrl`, or over the unix socket at `sourceSocket`
   for a source on the same host, with `afterSequence` set to that position and `limit` set to `pageLimit`. Every page
   must carry the same store ID as the first response. If it changes, the source was reset during the run: the run
   stops with an error, without importing that page, and the next run starts the new file.
5. Writes each page with `Store.Import`, in one SQLite transaction per page. `Import` is a variant of `Append` that
   skips sequence reservation and the Append Condition check: each event keeps the `sequence` and `time` the source
   gave it. It then moves the local Sequence Position counter past the highest sequence imported.
6. Stops once a page's trailer reads `hasMore: false`.

The file name is what keeps the events of one store out of the file of another: nothing else needs to be stored or
checked. After a reset of the source, the next run sees a new store ID, creates a new file, and starts from zero; the
old file stays as it is.

A page cut short (a response that ends without its trailer, see Response format) fails the run instead of importing a
partial page. So does a page request that takes more than 5 minutes, so a source that stops answering never holds
the backup file's lock past that. There's no retry inside a run: the error goes to stderr, with a non-zero exit code.
Every page imported before the failure is already committed, so the next run resumes right after it.

The backup copies events only. Projections are left out on purpose: every projection can be rebuilt from events (see
Projection rebuilds), so the backup file's `projections` table stays empty.

The result is a regular TamarackDB database file. Unlike a raw copy of the source's file, which can miss commits still
in the WAL, it can be opened and served by `tamarackdb-server` as a live instance. It has a store ID of its own, drawn
when the run created it: served as an instance, it's seen as another store, which is right, since it can be behind
the source. A source on the same host is read straight from its unix socket, with nothing exposed over the network. A
source on another host is reached over HTTPS, through a reverse proxy in front of its socket (see
[Backup](/docs/guides/backup/#a-source-on-another-host)).

## Configuration

TamarackDB's startup configuration (socket path or bind address/port, auth token, data directory, pagination, size,
and write limits, and the FIFO depth) comes from three sources, in this order:

1. A TOML configuration file, passed via `--config` (defaults to `config.toml` in the working directory). Keys live
   under a `[server]` section, so the same file can also hold `tamarackdb-backup`'s `[backup]` section (see
   [Backup](/docs/guides/backup/)); each binary reads only its own section.
2. `TAMARACKDB_*` environment variables, one per configuration key.
3. Built-in defaults, for the keys that have one (`socketPath`, `socketMode`, `dataDir`, `logLevel`, `defaultEventsPerPage`,
   `maxEventsPerPage`, `maxEventSize`, `maxProjectionSize`, `maxEventsPerWrite`, `maxProjectionsPerWrite`,
   `maxRequestBodySize`, `maxQueuedWrites`, `readPoolSize`).

A value set in the configuration file always wins over the matching environment variable. The file is checked as a
whole, `[server]` and `[backup]` sections included: an unknown key, or a key outside any section, is fatal at startup
and named in the error, so a misspelled setting never silently keeps its default. The configuration file
itself is optional: an application deployed as one instance per environment, each with its own file, uses it as the
single source of truth. A container deployment with no file at all is set up entirely through the environment
instead.

| Key | Environment variable | Default |
|---|---|---|
| `socketPath` | `TAMARACKDB_SOCKET_PATH` | `/run/tamarackdb/tamarackdb.sock` |
| `socketMode` | `TAMARACKDB_SOCKET_MODE` | `"0600"` (only with `socketPath`) |
| `bindAddress` | `TAMARACKDB_BIND_ADDRESS` | `127.0.0.1` |
| `port` | `TAMARACKDB_PORT` | `8085` |
| `enableAuth` | `TAMARACKDB_ENABLE_AUTH` | `false` |
| `authToken` | `TAMARACKDB_AUTH_TOKEN` | |
| `dataDir` | `TAMARACKDB_DATA_DIR` | `data` |
| `logLevel` | `TAMARACKDB_LOG_LEVEL` | `warning` |
| `devMode` | `TAMARACKDB_DEV_MODE` | `false` |
| `defaultEventsPerPage` | `TAMARACKDB_DEFAULT_EVENTS_PER_PAGE` | `1000` |
| `maxEventsPerPage` | `TAMARACKDB_MAX_EVENTS_PER_PAGE` | `10000` |
| `maxEventSize` | `TAMARACKDB_MAX_EVENT_SIZE` | `65536` (64 KiB) |
| `maxProjectionSize` | `TAMARACKDB_MAX_PROJECTION_SIZE` | `65536` (64 KiB) |
| `maxEventsPerWrite` | `TAMARACKDB_MAX_EVENTS_PER_WRITE` | `100` |
| `maxProjectionsPerWrite` | `TAMARACKDB_MAX_PROJECTIONS_PER_WRITE` | `500` |
| `maxRequestBodySize` | `TAMARACKDB_MAX_REQUEST_BODY_SIZE` | `8388608` (8 MiB) |
| `maxQueuedWrites` | `TAMARACKDB_MAX_QUEUED_WRITES` | `100` |
| `readPoolSize` | `TAMARACKDB_READ_POOL_SIZE` | `8` |

`dataDir` is the directory holding the database file, `tamarackdb.sqlite`. Only the directory is configurable, the
same convention MySQL's own `datadir` uses: the filename within it is fixed.

`maxProjectionSize` bounds one projection (its `type`, `id`, and `payload`) the same way `maxEventSize` bounds one
event. `maxEventsPerWrite` and `maxProjectionsPerWrite` cap how many events and projections one `POST /write` may
carry; `maxEventsPerWrite` also caps its Append Conditions. `maxRequestBodySize` caps any request body. It isn't checked
against the other limits: it's the real bound on a write, and the others are rules for each item. The defaults are a
cautious starting point: an application finds its real limits in development, with its own data, and the operator
sets them for production.

`maxQueuedWrites` caps how many requests may wait in the FIFO at once. A request that arrives when the FIFO is already
at that depth gets `503 WriteQueueFull` instead of joining. It isn't "0 means no limit": a FIFO with no bound would let
a burst, or a broken client, pile up an unlimited number of blocked HTTP connections, so every deployment gets a bound
whether it sets one or not. How long a request waits needs no setting of its own: each request ahead of it holds the
turn only for the time of its own write. A client that wants a shorter wait closes the connection.

## Security

TamarackDB listens on a unix socket by default (`socketPath`), and switches to TCP once `bindAddress` or `port` is set
(see Configuration); `socketPath` wins whenever it's set, even alongside `bindAddress`/`port`. The unix socket is the
recommended setup: TamarackDB runs on the same host as the application, nothing goes over the network, and the
socket's permissions decide who may connect. A transaction often makes several reads before its one write, and a unix
socket keeps each of those round trips short.

The server speaks plain HTTP only, on the socket and over TCP alike. TLS, for an application or a backup on another
host, is a reverse proxy's job. A server that loads its certificate once at startup would need a restart, cutting
off any write in progress, every time a short-lived certificate is renewed; a reverse proxy renews on its own. It
also handles what surrounds TLS (protocol versions, client certificates, address allowlists) better than a store
should, and keeps a single path for every access from another host, the application's and the backup's alike. The
Bearer token stays checked by the server itself: the proxy passes the `Authorization` header through, and the token
protects the API whatever the transport. `enableAuth` defaults to off, for the recommended setup, where the socket's
permissions already decide who may connect.

On the unix socket, access is controlled by the file's permissions: connecting takes write permission on it. The
server creates the socket under a umask of `0177`, so it starts out as `0600` whatever the process's own umask, then
sets it to `socketMode` (`"0600"` by default). Under a looser umask, such as the common `002`, the socket would
otherwise start out open to the group, and a connection could slip in before `socketMode` is applied. `"0660"` lets
the server's group connect too, for an application running as another user. A socket path is at most 107 bytes long on Linux; a longer `socketPath` is rejected when the
configuration loads, with an error naming the limit.

The database file holds every event and projection in plain SQLite, so anyone who can read it bypasses `enableAuth`
and `socketMode` entirely. The server, `tamarackdb-init`, and `tamarackdb-backup` create a missing data directory as
`0700`, and a new database file as `0600`, whatever the umask; SQLite gives its WAL and shared-memory files the
database file's permissions. A directory or a database file that already exists keeps its permissions: the server
never changes them.

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

### Dev mode

`devMode` (see Configuration) turns on two things, neither reachable otherwise, both meant only for local development
and test environments, never a production instance: `POST /reset`, and Go's standard profiling endpoints under
`/debug/pprof/` (CPU, heap, goroutine, and so on). Neither exists at all unless `devMode` is `true`. Left at its
default of `false`, a request to either gets the stdlib's plain `404`, like any other unregistered path. That keeps
them out of reach in a normal deployment, instead of reachable-but-guarded.

`POST /reset` waits for its turn in the FIFO like a write, then deletes everything and draws a new store ID (see
Reset). The profiling endpoints are read-only and outside the FIFO: they inspect the running process (CPU samples,
memory allocations, goroutine stacks), not the database, so they carry none of `POST /reset`'s data-loss risk. They're
still dev-mode-only because a CPU or heap profile can reveal details about the data flowing through a live request
that a production deployment shouldn't expose to whoever can reach the port.

## Management / observability features

### Health check

A lightweight `GET /health` endpoint confirms the process is responding and SQLite is reachable (with a trivial `SELECT
1` on the read pool), for a process supervisor or load balancer to check. On success it responds `200 OK` with a small
JSON body:

```json
{"status": "ok", "version": "1.2.3"}
```

On failure to reach SQLite, it responds `503 Unavailable` rather than `500`, the usual signal a supervisor or
load balancer already expects for "not ready right now," different from the `500` an ordinary request failure returns
elsewhere in the API.

### Request logging

Every request logs one line to stdout once its handler finishes: HTTP method, path, resulting status code, response size
in bytes, and how long it took, tagged with its level, e.g. `tamarackdb-server: [DEBUG] POST /write 200 106B 1.23ms`.
This wraps the whole routed handler, including authentication, so a request turned away with `401 Unauthorized` gets
logged just like any other. `logLevel` sets the minimum severity a line is written at (see Configuration). The line
never carries a request or response body, so queries, conditions, and events never reach the log.

### Queue and connection pool observability

Event and projection counts, per-type breakdowns, and database file size (anything you can work out from the store's
own content) are a query away, straight against the SQLite file, so the store doesn't need to expose them itself. What
the file can't answer is live, in-memory state that only exists for the life of the process: the request holding the
write turn, the FIFO, the writes so far, and how busy the read and write SQLite connection pools are. Two endpoints
cover that, kept separate since they serve different needs:

**`GET /metrics`**: Prometheus exposition format, for scraping into existing monitoring:
- `tamarackdb_write_active` (gauge): whether a request holds the write turn (`1`) or not (`0`): a write, a bulk delete,
  a reset, or `PRAGMA optimize`
- `tamarackdb_requests_queued` (gauge): number of requests currently waiting in the FIFO
- `tamarackdb_queue_longest_wait_seconds` (gauge): longest current wait, in seconds, among queued requests; `0` when
  the FIFO is empty
- `tamarackdb_writes_committed_total` (counter): total `POST /write` calls committed since startup
- `tamarackdb_writes_rejected_total` (counter, label `reason`): total `POST /write` calls refused with `409
  ConcurrencyException` since startup, by reason: `condition` (an Append Condition didn't hold, or was read on another
  store) or `projection` (a projection wasn't at the version given)
- `tamarackdb_write_duration_seconds` (histogram): how long each `POST /write` held the turn, from its turn to its end,
  however it ended

The two reasons for a refused write point at different things. A refused condition is real business contention: two
decisions raced on the same events. A refused projection is often operational: two instances of one projector running
at once, or a bulk delete during a rebuild.

**`GET /debug`**: a JSON snapshot for digging into one specific slow write, a queue that keeps growing, or a read pool
that looks saturated, too detailed to fit a metric:

```json
{
  "time": "2026-09-01T14:23:05.123456Z",
  "write": {
    "active": {
      "kind": "write",
      "since": "2026-09-01T14:23:05.120000Z",
      "ageSeconds": 0.003
    },
    "queued": [
      {
        "kind": "projections",
        "queuedAt": "2026-09-01T14:23:05.121000Z",
        "waitSeconds": 0.002
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

`write.active` describes the request holding the turn, if any (`null` otherwise): its `kind` and since when. It never
carries what the request reads or writes: the queue manager never knows it. `write.queued` lists every request still
waiting, oldest first, with its `kind`, and `waitSeconds` instead of `ageSeconds`. `write.queued` is always present,
never `null`, even when empty. A `kind` is one of `write` (`POST /write`), `projections` (a bulk delete), `reset`, and
`optimize`.

`httpOpen` is how many requests are currently in flight on each side: on the write side, every request that waits in
the FIFO or holds the turn (writes, bulk deletes, resets); on the read side, every read. `sqliteInUse` and
`sqliteMax` are the underlying SQLite connection pool's usage against its configured ceiling (`database/sql`'s own
`DBStats.InUse`/`MaxOpenConnections`, read straight off the read and write `*sql.DB` pools). `write.sqliteMax` is
always `1`: the write pool is deliberately capped at one connection, so SQLite's own driver enforces the same
single-writer guarantee the FIFO already provides at the HTTP layer. `read.httpOpen` can run higher than
`read.sqliteMax` when the read pool is saturated and the extra requests are waiting inside `database/sql` for a free
connection. A sustained gap between the two is a sign that `readPoolSize` (see
[Deployment](/docs/guides/deployment/)) is too small for the traffic.

Since the queue manager serializes access to its own state behind a mutex, separate from the write connection, and
the SQLite pool stats come straight from `database/sql`'s own counters, answering a `GET /debug` request is always a
quick, non-blocking read: never stuck behind a queued request or a running write.

## Implementation

The concrete Go code lives in `internal/queue` (the FIFO), `internal/txn` (the write manager: a turn in the FIFO for
each write, bulk delete, reset, and `PRAGMA optimize`, and the write counters), `internal/store` (the SQLite
transaction of a write, reads, and the Query-to-SQL translation), and `cmd/tamarackdb-backup` (the backup tool). The
projection wire shape and its validation rules live in `internal/projection`, independent of `internal/dcb`. The
shared matcher cases live in `testdata/query-cases.json`.
