---
title: "Errors"
description: "The error envelope of the TamarackDB HTTP API, every error code with its status and meaning, and the causes of a 400 InvalidRequest response."
slug: "errors"
weight: 7
---

Every error from an endpoint uses one JSON envelope, with a stable code a client can check.

## Envelope

```json
{ "error": "InvalidRequest", "message": "afterSequence must be a non-negative integer" }
```

- `error` is a stable code.
- `message` is a human-readable detail. It's left out when it wouldn't add anything, as with `WriteQueueFull`.

A few responses carry plain text instead, since they come from Go's HTTP server before any endpoint runs: `404` for an
unknown path, `405` for a known path with the wrong method, and the errors for a malformed HTTP request or oversized
headers.

## Codes

| Status | `error` | When |
|---|---|---|
| `400` | `InvalidRequest` | A malformed or invalid body (see below) |
| `401` | `Unauthorized` | A missing or invalid Bearer token, only when `enableAuth` is on (see [Security](/docs/operations/security/#bearer-token)) |
| `404` | `ProjectionNotFound` | `GET /projections/{type}/{id}` for a projection that doesn't exist |
| `409` | `ConcurrencyException` | `POST /write`: an Append Condition doesn't hold or was read on another store, or a projection doesn't match the stored one. The `message` names the item (see [Conflicts](/docs/http-api/write/#conflicts)). Nothing was written |
| `413` | `PayloadTooLarge` | An event or a projection over its size limit, or a body over `maxRequestBodySize`. The `message` names the setting |
| `500` | `InternalError` | An unexpected failure on the server |
| `503` | `WriteQueueFull` | `POST /write`, a bulk delete, or `POST /reset`, while too many requests already wait for their turn. Nothing was written |
| `503` | `ShuttingDown` | `POST /write`, a bulk delete, or `POST /reset`, waiting or arriving while the server shuts down. Nothing was written |
| `503` | `Unavailable` | `GET /health` only: SQLite can't be reached (see [Health check](/docs/operations/health-check/)) |

How each code is logged is in [Logs](/docs/operations/logs/).

## Causes of a 400

`QUERY /events` and `POST /write` respond `400 InvalidRequest` for:

- **The body**: invalid JSON, anything after the JSON value other than whitespace, or an unknown key at any level.
- **A query** (`query`, or a condition's `failIfEventsMatch`): not an array of query items or `"*"`, an empty array
  where the grammar needs a non-empty one, an empty item, more than 100 items, or more than 100 values in one item (see
  [Query grammar](/docs/http-api/query-grammar/)).
- **Reading**: a non-integer or negative `afterSequence`, or a `limit` below 1 or above `maxEventsPerPage`.
- **An event**: a missing `type`, a missing or `null` `payload`, an empty array as a tag value, a duplicate tag, or more
  than 20 identifiers or metadata entries (see [Events](/docs/concepts/events/)).
- **A condition**: a negative `afterSequence`, an `afterSequence` without `store`, or a `store` without `afterSequence`.
- **A projection**: a missing `type`, `id`, `version` (for `replace` and `delete`), or `payload` (for `create` and
  `replace`), a key its list doesn't take, or the same `type` + `id` twice in one write.
- **A write**: more events, conditions, or projections than one write allows (see
  [Writing](/docs/http-api/write/#limits)).

The `message` names the item at fault by its place in the body, for example `events[3]` or `projections.create[0]`, and
names the setting behind a limit.
