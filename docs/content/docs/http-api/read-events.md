---
title: "Reading events"
description: "QUERY /events: reading committed events a page at a time, the NDJSON response and its trailer, cursor pagination, and resuming a page that was cut short."
slug: "read-events"
weight: 3
---

`QUERY /events` reads committed events, a page at a time.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## Request

It uses the [HTTP QUERY method](https://www.rfc-editor.org/info/rfc10008/) (RFC 10008): safe and idempotent like
`GET`, but with a body like `POST`, since a query can be too large to fit in a URL.

```sh
curl -X QUERY http://127.0.0.1:8085/events \
  -H "Content-Type: application/json" \
  -d '{
    "query": [ { "identifiers": [ { "name": "userId", "value": "123" } ] } ],
    "afterSequence": 12345,
    "limit": 500
  }'
```

| Key | Required | What it does |
|---|---|---|
| `query` | yes | A query in the [query grammar](/docs/http-api/query-grammar/), or `"*"` for every event |
| `afterSequence` | no | Only events with a Sequence Position strictly greater than this value. Left out, the read starts at the beginning |
| `limit` | no | The most events in this page: at least 1, at most `maxEventsPerPage`. Left out, `defaultEventsPerPage` (see [Configuration](/docs/operations/configuration/)) |

- The body is decoded strictly (see [Conventions](/docs/http-api/conventions/#request-bodies)).
- A negative `afterSequence`, or a `limit` below 1 or above `maxEventsPerPage`, gets `400`.
- There is no filter on `time` (see [Events](/docs/concepts/events/#time)), and no `order` option: events always come
  in ascending Sequence Position order, the only order a decision needs.
- A read sees committed events only, and never waits for a write.

## Response

`200 OK`, with the store ID in the `X-Tamarackdb-Store` header, and an [NDJSON](https://github.com/ndjson/ndjson-spec)
body (`Content-Type: application/x-ndjson`): one JSON value per line.

```
{"sequence":12346,"time":"2026-09-01T14:23:05.123456Z","type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}
{"sequence":12347,"time":"2026-09-01T14:23:07.981234Z","type":"user-updated","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}
{"hasMore":true}
```

- Every line but the last is one matching event, in ascending Sequence Position order. Its fields are described in
  [Events](/docs/concepts/events/). `identifiers` and `metadata` come back in the same compact object shape used to
  write them.
- The last line is always a trailer, `{"hasMore": true}` or `{"hasMore": false}`.
- A client MUST tell the trailer apart from an event by shape (a `hasMore` key), not by position: it's only known to be
  last once the stream ends.
- A client SHOULD parse the body line by line, as it arrives, not as one JSON document.

**Why NDJSON.** Each line stands on its own and carries its own Sequence Position. A response cut off mid-transfer
still leaves every full line usable, and the client resumes after the last one. A cut JSON array is invalid, and the
whole page is lost. NDJSON also lets the server write each event as it comes out of SQLite, without holding the page
in memory.

**Why the trailer comes last.** Knowing `hasMore` means fetching the whole page first. Putting it last lets the server
stream each event as it's read.

## Pagination

- The server fetches `limit + 1` events. If it gets that many, it returns `limit` of them with `hasMore: true`.
  Otherwise it returns everything with `hasMore: false`.
- To fetch the next page, repeat the same `query` with `afterSequence` set to the Sequence Position of the last event
  received.
- The same loop follows new events live: keep polling with `afterSequence` set to the last Sequence Position seen.
  Once `hasMore` reads `false`, the client has caught up, and later calls pick up new events as they're written.

**Why a cursor, not an offset.** Events written between two calls would shift an offset, and the client would skip or
repeat events. A Sequence Position never changes, so it stays a valid place to resume, whatever was written since.

## A page cut short

A page can end without its trailer: the connection dropped, a timeout, or a failure on the server's side after the
first lines were sent (the status code is already on the wire by then, so no error response can follow).

- A client MUST treat a page with no trailer as cut short, never as complete.
- It resumes with `afterSequence` set to the last event line it fully received. No event is skipped or repeated.
- The server gives each line 30 seconds to go out, renewed on every line. A client that stops reading for longer gets
  its connection closed, and sees a page with no trailer. A client SHOULD read each page as it arrives.
