# Using TamarackDB

This is a guide for applications and client libraries talking to a running
TamarackDB instance over HTTP. For how the server is built internally
(the reservation manager, the SQLite schema, concurrency handling), see
[design.md](design.md) instead.

Examples below assume a server running locally on the default
`127.0.0.1:8085`, with authentication disabled. Add
`-H "Authorization: Bearer <token>"` to every request when `enableAuth` is
turned on (see the [README](../README.md#configure)).

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

`identifiers` and `metadata` are objects whose values are a string or an
array of strings — an array produces one tag per value, all attached to the
same event. `payload` is an opaque string; TamarackDB never parses it, so its
format (JSON, XML, or anything else) is entirely up to the calling
application.

On success (`200 OK`), the response confirms the Sequence Position and time
assigned to each event, in the order submitted:

```json
{
  "events": [
    { "sequence": 12348, "time": "2026-09-01T14:25:00.000000Z" }
  ]
}
```

A single request may carry up to 100 events, each up to 64 KiB (the combined
size of its `type`, `identifiers`, `metadata`, and `payload`). Larger content
(files, documents) belongs in external storage, referenced from the event
rather than embedded in it.

### Optimistic concurrency

Pass a `condition` to make the append fail if something relevant happened
since you last read:

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
below). `failIfEventsMatch` is a query using the same grammar as `read` (see
below); the append is rejected if any event matching it exists after
`afterSequence`. Both are optional and independent: an event with nothing to
protect can be appended with no `condition` at all.

A failed condition responds `409 Conflict`:

```json
{ "error": "ConcurrencyException" }
```

The usual flow is: `read` the events relevant to your decision, keep the
Sequence Position of the last one seen, decide what to write, then `append`
with that Sequence Position as `afterSequence` and the same query as
`failIfEventsMatch`.

## Reading events

Reading uses the HTTP `QUERY` method (not `GET`), since a query can be too
large or nested to fit in a query string:

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
(`Content-Type: application/x-ndjson`): one JSON value per line. The first
line is always a header carrying `hasMore`; every following line is one
matching event, in ascending Sequence Position order:

```
{"hasMore":false}
{"sequence":12346,"time":"2026-09-01T14:23:05.123456Z","type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}
```

Parse it line by line rather than as one JSON document — that's also what
makes a response safe to resume from if the connection drops mid-transfer
(see Pagination).

### Query grammar

`query` is an array of query items, combined with **OR**. Within one item:

- **OR** across `types` (matches if the event's type is any of the listed ones; omit to match every type)
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

An empty item (`{}`) matches every event, same as `"*"` for the whole query.
Every array in this grammar must be non-empty when present — send `"*"`, or
omit the key, rather than `[]`. There is no negation: a query only ever
describes a bounded set of matching events.

Two optional top-level keys narrow a query further:

- `afterSequence` — only events with a Sequence Position strictly greater than this value
- `time: { "from": "...", "before": "..." }` — only events whose `time` falls in this range (`from` inclusive, `before` exclusive); either key, or `time` itself, can be omitted

```json
{ "query": "*", "afterSequence": 12345, "time": { "from": "2026-01-01T00:00:00.000000Z" } }
```

### Pagination

Events are always returned oldest-first (ascending Sequence Position). Cap a
response with `limit`:

```json
{ "query": "*", "afterSequence": 12345, "limit": 500 }
```

`hasMore: true` in the header means more events matched than were returned.
To fetch the next page, repeat the same `query` with `afterSequence` set to
the Sequence Position of the last event you received. Doing this on every
call — not just the first — also handles a response cut off mid-transfer:
resume from the last fully-received line, no events skipped or repeated.

The same loop that pages through history also tails new events live: keep
polling with `afterSequence` set to the last Sequence Position seen; once
`hasMore` reads `false` you've caught up, and further polling picks up new
events as they're appended.

`limit` defaults to whatever the server operator configured (`defaultLimit`,
1000 out of the box) and is capped at `maxLimit` (10000 out of the box); a
request asking for more than that is rejected with `400 Bad Request`.

## Error responses

Every error uses the same envelope:

```json
{ "error": "InvalidRequest", "message": "afterSequence must be a non-negative integer" }
```

`error` is a stable code to branch on; `message` is a human-readable detail,
present when it helps and omitted otherwise.

| Status | `error` | Meaning |
|---|---|---|
| 400 | `InvalidRequest` | Malformed or invalid request body — bad JSON, invalid query shape, `limit` over the configured maximum, more than 100 events in one `append`, and so on |
| 401 | `Unauthorized` | Missing or invalid Bearer token (only when `enableAuth` is on) |
| 409 | `ConcurrencyException` | The append's `condition` failed |
| 413 | `PayloadTooLarge` | An event exceeds the configured maximum size |
| 500 | `InternalError` | Unexpected server-side failure |
| 503 | `Unavailable` | `GET /health` only: storage is unreachable |

## Health check

```sh
curl http://127.0.0.1:8085/health
```

```json
{ "status": "ok", "version": "1.2.3" }
```
