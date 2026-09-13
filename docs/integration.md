# Integrating with TamarackDB

This is for anyone writing an application or client library that talks to a
running TamarackDB instance over HTTP. For how to run that instance, see
[deploy.md](deploy.md). For how the server works inside, see [design.md](design.md).

Examples below assume a server running locally on the default `127.0.0.1:8085`,
with authentication off. Add `-H "Authorization: Bearer <token>"` to every request
when `enableAuth` is on (see [deploy.md](deploy.md#configure)).

## Reading events

Reading uses the HTTP `QUERY` method, not `GET`, since a query can be too large or
nested to fit in a URL:

```sh
curl -X QUERY http://127.0.0.1:8085/read \
  -H "Content-Type: application/json" \
  -d '{
    "query": [
      { "identifiers": [ { "name": "userId", "value": "123" } ] }
    ]
  }'
```

Use the literal string `"*"` in place of `query` to read every event:

```sh
curl -X QUERY http://127.0.0.1:8085/read \
  -H "Content-Type: application/json" \
  -d '{ "query": "*" }'
```

The response is [NDJSON](https://github.com/ndjson/ndjson-spec)
(`Content-Type: application/x-ndjson`): one JSON value per line. The first line is
always a header with `hasMore`. Every line after that is one matching event, oldest
first:

```
{"hasMore":false}
{"sequence":12346,"time":"2026-09-01T14:23:05.123456Z","type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}
```

Parse it line by line, not as one JSON document. That way, a response can safely
resume if the connection drops mid-transfer (see Pagination below).

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

An empty item (`{}`) matches every event, just like `"*"` for the whole query.
Every array in this grammar must be non-empty when present: send `"*"`, or leave
the key out, rather than `[]`. There is no way to say "not X": a query only ever
describes a set of matching events, never an exclusion.

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

`hasMore: true` in the header means more events matched than were returned. To
fetch the next page, repeat the same `query` with `afterSequence` set to the
Sequence Position of the last event you got. Do this on every call, not just the
first one: it also lets you resume a response that was cut off mid-transfer, from
the last full line you received, with no events skipped or repeated.

The same loop that pages through history can also follow new events live: keep
polling with `afterSequence` set to the last Sequence Position you saw. Once
`hasMore` reads `false`, you have caught up, and further polling picks up new
events as they arrive. `tamarackdb-backup` (see [backup.md](backup.md)) is a
real example of this loop: it pages through `/read` with `afterSequence` set
to the last sequence it saved locally, and stops once `hasMore` reads
`false`.

`limit` defaults to whatever the server operator set (`defaultLimit`, 1000 out of
the box) and is capped at `maxLimit` (10000 out of the box). Asking for more than
that gets you `400 Bad Request`.

## Appending events

```sh
curl -X POST http://127.0.0.1:8085/append \
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

Pass a `condition` to make the append fail if something relevant happened since
you last read:

```sh
curl -X POST http://127.0.0.1:8085/append \
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
above). `failIfEventsMatch` is a query, using the same grammar as `read` (see
above). The append fails if any event matching it exists after `afterSequence`.
Both are optional and independent: an event with nothing to protect can be
appended with no `condition` at all.

A failed condition gets `409 Conflict`:

```json
{ "error": "ConcurrencyException" }
```

The usual flow is: `read` the events relevant to your decision, keep the Sequence
Position of the last one you saw, decide what to write, then `append` with that
Sequence Position as `afterSequence` and the same query as `failIfEventsMatch`.

## Resetting between test runs

If the server has `devMode` on, `DELETE /` wipes every event, identifier, and
metadata row, leaving an empty database ready for the next test:

```sh
curl -X DELETE http://127.0.0.1:8085/
```

This is useful for a client library's own test suite: start each test, or each
test run, from a clean database instead of tracking what earlier tests left
behind. It responds `204 No Content` and only exists when `devMode` is on; see
[deploy.md](deploy.md#configure). Never rely on it against a production instance.

## Generating test events

`tamarackdb-demo` fills a database with a large set of made-up events, useful for
testing a client library against realistic volume instead of one or two
hand-written events:

```sh
./bin/tamarackdb-demo -db /path/to/tamarack.db -n 100000 -seed 1
```

It writes straight to the database file, not through the running server, so run
it before starting `tamarackdb`, or against a separate file. See
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
| 400 | `InvalidRequest` | Malformed or invalid request body: bad JSON, invalid query shape, `limit` over the configured maximum, more than 100 events in one `append`, and so on |
| 401 | `Unauthorized` | Missing or invalid Bearer token (only when `enableAuth` is on) |
| 409 | `ConcurrencyException` | The append's `condition` failed |
| 413 | `PayloadTooLarge` | An event is bigger than the configured maximum size |
| 500 | `InternalError` | Unexpected server-side failure |
| 503 | `AppendQueueFull` | `append`/`DELETE /` only: the write queue is already full; retry after the `Retry-After` header |
| 503 | `Unavailable` | `GET /health` only: storage is unreachable |
