---
title: "Errors"
description: "The error envelope of the TamarackDB HTTP API, every error code with its status and meaning, and the causes of a 400 InvalidRequest response."
slug: "errors"
weight: 10
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
| `404` | `ProjectionNotFound` | A projection that doesn't exist, read with `GET /projections/{type}/{id}` or in a transaction |
| `404` | `TransactionNotFound` | A call on a transaction that is unknown, expired, or already over (see [Transactions](/docs/http-api/transactions/#errors)) |
| `409` | `ConcurrencyException` | A commit or `POST /projections`: a condition doesn't hold, or a projection doesn't match the stored one. The `message` names the cause (see [Transactions](/docs/http-api/transactions/#commit) and [Projections](/docs/http-api/projections/#conflicts)). Nothing was written |
| `409` | `TransactionBusy` | A call on a transaction while another call still runs on it. Nothing changed: the transaction goes on (see [Transactions](/docs/concepts/transactions/#many-transactions-at-once)) |
| `409` | `NotPaused` | `POST /reset` outside a pause in place (see [Reset](/docs/http-api/reset/)). Nothing was deleted |
| `413` | `PayloadTooLarge` | An event or a projection over its size limit, or a body over `maxRequestBodySize`. The `message` names the setting |
| `500` | `InternalError` | An unexpected failure on the server |
| `503` | `Paused` | `POST /tx` while a pause is requested or in place (see [Pause](/docs/http-api/pause/)). No transaction began |
| `503` | `WriteQueueFull` | A commit, `POST /projections`, a bulk delete, `POST /pause`, `POST /resume`, `POST /optimize`, or `POST /reset`, while too many requests already wait for their turn (see [Conventions](/docs/http-api/conventions/#waiting-for-a-turn)). Nothing was written |
| `503` | `ShuttingDown` | A commit, `POST /projections`, a bulk delete, `POST /pause`, `POST /resume`, `POST /optimize`, or `POST /reset`, waiting or arriving while the server shuts down. Nothing was written |
| `503` | `Unavailable` | `GET /health` only: SQLite can't be reached (see [Health check](/docs/operations/health-check/)) |

How each code is logged is in [Logs](/docs/operations/logs/).

## Causes of a 400

An endpoint with a body responds `400 InvalidRequest` for:

- **The body**: invalid JSON, anything after the JSON value other than whitespace, or an unknown key at any level.
- **A query**: missing, not `"all"`, `"none"`, or an array of query
  items, an empty array
  where the grammar needs a non-empty one, an empty item, more than 100 items, or more than 100 values in one item (see
  [Query grammar](/docs/http-api/query-grammar/)).
- **Reading**: a non-integer or negative `afterSequence`, or a `limit` below 1 or above `maxEventsPerPage`.
- **An event**: a missing `type`, a missing or `null` `payload`, an empty array as a tag value, a duplicate tag, or more
  than 20 identifiers or metadata entries (see [Events](/docs/concepts/events/)).
- **A projection**: a missing `type`, `id`, `version` (for `replace` and `delete`), or `payload` (for `create` and
  `replace`), a key its list doesn't take, or the same `type` + `id` twice in one write.
- **A limit**: more projections than one `POST /projections` allows (see
  [Projections](/docs/http-api/projections/#limits)), or a call that would take a transaction over one of its limits
  (see [Transactions](/docs/http-api/transactions/#limits)).
- **A transaction**: a call that breaks one of its rules, such as events written without a read, or a projection
  replaced without being read first (see [Concepts: Transactions](/docs/concepts/transactions/)).

The `message` names the item at fault by its place in the body, for example `events[3]` or `create[0]`, and
names the setting behind a limit.
