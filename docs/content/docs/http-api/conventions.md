---
title: "Conventions"
description: "What every endpoint of the TamarackDB HTTP API has in common: the endpoints, connecting, strict request bodies, the body size cap, and the store ID header."
slug: "conventions"
weight: 1
---

What every endpoint of the HTTP API has in common.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## Endpoints

| Endpoint | Purpose |
|---|---|
| [`QUERY /events`](/docs/http-api/read-events/) | Read committed events |
| [`POST /tx`](/docs/http-api/transactions/) and `/tx/{txId}/...` | Begin a transaction, read and write in it, then commit it in its turn, or abandon it |
| [`POST /write`](/docs/http-api/write/) | Check Append Conditions, append events, and write projections, all or nothing, in its turn |
| [`GET /projections/{type}/{id}`](/docs/http-api/projections/) | Read one committed projection |
| [`DELETE /projections/{type}`](/docs/http-api/projections/#bulk-delete) | Delete every projection of one type, in its turn |
| [`DELETE /projections`](/docs/http-api/projections/#bulk-delete) | Delete every projection, in its turn |
| [`POST /reset`](/docs/http-api/reset/) | Delete all events and projections and draw a new store ID, in its turn (development mode only) |

- "In its turn" means the request waits behind the requests that arrived before it: the server runs them one at a
  time, in the order they arrive.
- `GET /health` and `GET /stats` are operational endpoints (see [Health check](/docs/operations/health-check/) and
  [Observability](/docs/operations/observability/)).

## Connecting

- By default the server listens on a unix socket; it can listen on TCP instead (see
  [Configuration](/docs/operations/configuration/)). With curl, point at the socket with
  `--unix-socket <path> http://localhost/...`.
- The socket's permissions decide who may connect: the client's user MUST be allowed by the server's `socketMode`
  (see [Security](/docs/operations/security/)).
- The server speaks plain HTTP. A client on another host goes through a reverse proxy that handles TLS.
- When `enableAuth` is on, every request MUST carry `Authorization: Bearer <token>`. Without a valid token, the
  request gets `401 Unauthorized`.

The examples in these pages assume a server on `127.0.0.1:8085`, with authentication off.

## Request bodies

- A body is JSON. Every endpoint that takes one decodes it strictly:
  - an unknown key, at any level, gets `400`;
  - anything after the JSON value, other than whitespace, gets `400`.
- Every request body is capped at `maxRequestBodySize` (see [Configuration](/docs/operations/configuration/)). Past
  the cap, the server stops reading and responds `413 PayloadTooLarge`.

**Why strict.** Most keys are optional. A misspelled one would otherwise be dropped without a word: a misspelled
`afterSequence` would widen a read, and a misspelled list in a write would drop it.

**Why a body cap.** It keeps a client from making the server read an unbounded body into memory before the other
limits are checked. It isn't checked against the other limits: a body can reach it before every one of its items
reaches its own. It's the real bound on a write, and the others are rules for each item.

## Store ID header

The `X-Tamarackdb-Store` header carries the store ID (see [Store ID](/docs/concepts/store-id/)) on every response
that depends on the store:

| Response | Header |
|---|---|
| `QUERY /events` | On every page, empty pages included |
| `GET /projections/{type}/{id}` | On `200` and on `404` |
| `POST /write` | On `200` |

- No transaction endpoint carries it: the server keeps the store ID with the transaction.
- A read takes the store ID in the same SQLite snapshot as the events or the projection it returns, so a response
  never pairs the data of one store ID with another.
- A write takes it in the same SQLite transaction as what it wrote.
- A request refused before it reads or writes (`400`, for example) carries no store ID.

## Errors

Every error from an endpoint uses one JSON envelope, with a stable code (see [Errors](/docs/http-api/errors/)).
