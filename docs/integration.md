# Integrating with TamarackDB

This is for anyone writing an application or client library that talks to a
running TamarackDB instance over HTTP. For how to run that instance, see
[deploy.md](deploy.md). For how the server works inside, see [design.md](design.md).

Examples below assume a server running locally on `127.0.0.1:8085`, with
authentication off. TamarackDB listens on a unix socket by default; the
examples apply the same way once you point curl at it with
`--unix-socket <path> http://localhost/...` instead of a host and port (see
[deploy.md](deploy.md#configure)). Add `-H "Authorization: Bearer <token>"` to
every request when `enableAuth` is on (see [deploy.md](deploy.md#configure)).

## Reading events

Reading uses the HTTP `QUERY` method, not `GET`, since a query can be too large or
nested to fit in a URL:

```sh
curl -X QUERY http://127.0.0.1:8085/events \
  -H "Content-Type: application/json" \
  -d '{
    "query": [
      { "identifiers": [ { "name": "userId", "value": "123" } ] }
    ]
  }'
```

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

Parse it line by line, not as one JSON document. That way, a response can safely
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
    { "types": ["user-created", "user-updated"], "identifiers": [{ "name": "userId", "value": "123" }] },
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
- `time: { "from": "...", "before": "..." }`: only events whose `time` falls in
  this range (`from` inclusive, `before` exclusive). Either key, or `time` itself,
  can be left out.

```json
{ "query": "*", "afterSequence": 12345, "time": { "from": "2026-01-01T00:00:00.000000Z" } }
```

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
polling with `afterSequence` set to the last Sequence Position you saw. Once
`hasMore` reads `false`, you have caught up, and further polling picks up new
events as they arrive. `tamarackdb-backup` (see [backup.md](backup.md)) is a
real example of this loop: it pages through `/events` with `afterSequence` set
to the last sequence it saved locally, and stops once `hasMore` reads
`false`.

`limit` defaults to whatever the server operator set (`defaultLimit`, 1000 out of
the box) and is capped at `maxLimit` (10000 out of the box). Asking for more than
that gets you `400 Bad Request`.

## Writing events and documents

```sh
curl -X POST http://127.0.0.1:8085/write \
  -H "Content-Type: application/json" \
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
strings: an array gives one tag per value, all on the same event. `payload` is an
opaque string. TamarackDB never parses it, so its format (JSON, XML, or anything
else) is entirely up to the calling application.

On success (`200 OK`), the response confirms the Sequence Position and time
assigned to each event, in the order you sent them:

```json
{
  "events": [
    { "sequence": 12348, "time": "2026-09-01T14:25:00.000000Z" }
  ]
}
```

A single request may carry up to 100 events, each up to 64 KiB (the combined size
of its `type`, `identifiers`, `metadata`, and `payload`). Put larger content
(files, documents) in external storage, and reference it from the event instead of
embedding it.

### Optimistic concurrency

Pass a `condition` to make the write fail if something relevant happened since
you last read:

```sh
curl -X POST http://127.0.0.1:8085/write \
  -H "Content-Type: application/json" \
  -d '{
    "events": [ { "type": "user-renamed", "identifiers": { "userId": "123" }, "payload": "..." } ],
    "condition": {
      "failIfEventsMatch": [ { "identifiers": [ { "name": "userId", "value": "123" } ] } ],
      "afterSequence": 12345
    }
  }'
```

`afterSequence` is the Sequence Position you last read up to (see Pagination
above). `failIfEventsMatch` is a query, using the same grammar as a read (see
above). The write fails if any event matching it exists after `afterSequence`.
Both are optional and independent: an event with nothing to protect can be
written with no `condition` at all.

A failed condition gets `409 Conflict`:

```json
{ "error": "ConcurrencyException" }
```

The usual flow is: read the events relevant to your decision, keep the Sequence
Position of the last one you saw, decide what to write, then write with that
Sequence Position as `afterSequence` and the same query as `failIfEventsMatch`.

## Documents

Alongside events, `/write` can carry `documents`: an optional list of projections
to create, update, or delete, atomically with the events in the same call (see
[design.md](design.md#documents) for the full mechanism, including why a
document's payload can, rarely, fail to persist without the whole call
failing). A document is identified by `type` + `id`, holds one opaque `payload`,
and carries a `version` for optimistic concurrency.

**Creating a document**: leave `version` out. You've never read this document,
so you know it's new; it's created at version 1. If it already exists, that's a
conflict.

```sh
curl -X POST http://127.0.0.1:8085/write \
  -H "Content-Type: application/json" \
  -d '{ "documents": [ { "type": "user-profile", "id": "123", "payload": "{\"name\":\"Ada\"}" } ] }'
```

A call with `documents` and no `events`/`condition` is valid: this is how you
materialize several projections at once during a rebuild.

**Updating a document**: pass the `version` you last read it at. It's written at
`version + 1`. A `version` that doesn't match what the store has, including a
document that no longer exists, is a conflict.

```sh
curl -X POST http://127.0.0.1:8085/write \
  -H "Content-Type: application/json" \
  -d '{ "documents": [ { "type": "user-profile", "id": "123", "payload": "{\"name\":\"Ada Lovelace\"}", "version": 1 } ] }'
```

**Deleting a document**: set `payload` to `null`, and pass the `version` you
read it at (required; there's no unversioned deletion of a single document).
Deleting an already-absent document is a conflict too, not a silent no-op.

```sh
curl -X POST http://127.0.0.1:8085/write \
  -H "Content-Type: application/json" \
  -d '{ "documents": [ { "type": "user-profile", "id": "123", "version": 2 } ] }'
```

The response reports each document's outcome, in the order you sent them:

```json
{
  "events": [],
  "documents": [
    { "type": "user-profile", "id": "123", "version": 3, "status": "ok" }
  ]
}
```

`status` is `"ok"`, or `"payloadWriteFailed"` if the document's identity and
version were committed but its payload failed to persist (rare; see
[design.md](design.md#documents)). Either way the call as a whole still
succeeds (`200 OK`): only retry the specific document that failed, not the
whole request.

**Reading a document**: `GET /documents/{type}/{id}`.

```sh
curl -i http://127.0.0.1:8085/documents/user-profile/123
```

```
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8
X-Tamarackdb-Document-Version: 3

{"name":"Ada Lovelace"}
```

The response body is the payload exactly as written, not wrapped in a JSON
envelope: its own format (JSON, XML, plain text) is up to the writing
application. The version comes back in the `X-Tamarackdb-Document-Version` header
instead.

This returns `404 DocumentNotFound` if no document exists at that `type` + `id`,
or `503 DocumentNotReady` (with a `Retry-After` header) if it exists but its
payload hasn't caught up yet: retry after the given delay, rather than
treating it as absent.

**Clearing a type before a rebuild**: `DELETE /documents/{type}` removes every
document of that type, unversioned, no `devMode` required:

```sh
curl -X DELETE http://127.0.0.1:8085/documents/user-profile
```

## Resetting between test runs

If the server has `devMode` on, `DELETE /events` wipes every event,
identifier, and metadata row, leaving an empty event log ready for the next
test. Documents are untouched:

```sh
curl -X DELETE http://127.0.0.1:8085/events
```

This is useful for a client library's own test suite: start each test, or each
test run, from a clean event log instead of tracking what earlier tests left
behind. It responds `204 No Content` and only exists when `devMode` is on; see
[deploy.md](deploy.md#configure). Never rely on it against a production instance.

## Generating test events

`tamarackdb-demo` fills an events database with a large set of made-up events,
useful for testing a client library against realistic volume instead of one
or two hand-written events:

```sh
./bin/tamarackdb-demo --dataDir /path/to/data --n 100000 --seed 1
```

It writes straight to the database file, not through the running server, so run
it before starting `tamarackdb-server`, or against a separate data directory. See
[build.md](build.md#demo-dataset) for how to build it and what its flags do.

## Error responses

Every error uses the same shape:

```json
{ "error": "InvalidRequest", "message": "afterSequence must be a non-negative integer" }
```

`error` is a stable code your code can check. `message` is a human-readable detail,
present when it helps and left out otherwise.

| Status | `error` | Meaning |
|---|---|---|
| 400 | `InvalidRequest` | Malformed or invalid request body: bad JSON, invalid query shape, `limit` over the configured maximum, more than 100 events or too many documents in one `write`, a document missing `type`/`id`, a repeated document `type`+`id` in the same call, a document deletion with no `version`, and so on |
| 401 | `Unauthorized` | Missing or invalid Bearer token (only when `enableAuth` is on) |
| 404 | `DocumentNotFound` | `GET /documents/{type}/{id}` only: no document exists at that `type` + `id` |
| 409 | `ConcurrencyException` | The write's `condition` failed, or a document's `version` didn't match |
| 413 | `PayloadTooLarge` | An event, or a document's `payload`, is bigger than the configured maximum size |
| 500 | `InternalError` | Unexpected server-side failure |
| 503 | `AppendQueueFull` | `write`/`DELETE /events`/`DELETE /documents/{type}` only: the write queue is already full; retry after the `Retry-After` header |
| 503 | `DocumentNotReady` | `GET /documents/{type}/{id}` only: the document exists but its payload hasn't caught up yet; retry after the `Retry-After` header |
| 503 | `Unavailable` | `GET /health` only: storage is unreachable |
