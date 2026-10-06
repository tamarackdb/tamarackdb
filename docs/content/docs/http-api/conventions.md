---
title: "Conventions"
description: "What every endpoint of the TamarackDB HTTP API has in common: connecting, strict request bodies, waiting for a turn, the client leaving, and the store ID."
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
| [`POST /projections`](/docs/http-api/projections/#writing-projections) | Create, replace, and delete projections outside any transaction, all or nothing, in its turn |
| [`GET /projections/{type}/{id}`](/docs/http-api/projections/#reading-a-projection) | Read one committed projection |
| [`DELETE /projections/{type}`](/docs/http-api/projections/#bulk-delete) | Delete every projection of one type, in its turn |
| [`DELETE /projections`](/docs/http-api/projections/#bulk-delete) | Delete every projection, in its turn |
| [`POST /pause`](/docs/http-api/pause/) and `POST /resume` | Stop transactions from beginning, in its turn, and let them begin again |
| [`POST /optimize`](/docs/http-api/optimize/) | Refresh the statistics SQLite plans queries with, in its turn |
| [`POST /reset`](/docs/http-api/reset/) | Delete all events and projections and draw a new store ID, in its turn (development mode only) |

- "In its turn" means the request waits behind the requests that arrived before it: the server runs them one at a
  time, in the order they arrive (see [Waiting for a turn](#waiting-for-a-turn)).
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
- A request that waits for its turn is read and checked in full before it joins the queue. An invalid body gets `400`
  or `413` right away.

**Why strict.** Most keys are optional. A misspelled one would otherwise be dropped without a word: a misspelled
`afterSequence` would widen a read, and a misspelled list in a write would drop it.

**Why a body cap.** It keeps a client from making the server read an unbounded body into memory before the other
limits are checked. It isn't checked against the other limits: a body can reach it before every one of its items
reaches its own. It bounds one request, not a transaction, which is built over many requests: a transaction has
limits of its own (see [Transactions](/docs/http-api/transactions/#limits)).

**Why check the body before the turn.** A client sending its body slowly would otherwise hold the turn, and every
request behind it, for as long as it likes.

## Waiting for a turn

These requests wait for their turn: a transaction's commit, `POST /projections`, the bulk deletes of projections,
`POST /pause` and `POST /resume`, `POST /optimize`, and `POST /reset`.

- They go through one at a time, in the order they arrive. Each waits, with its connection held open, behind the
  requests that arrived before it.
- Each holds the turn only for its own SQLite transaction, usually a few milliseconds.
- When too many requests already wait (`maxQueuedWrites`, see [Configuration](/docs/operations/configuration/)), a
  new one gets `503 WriteQueueFull` and never joins the queue. Nothing is written.
- A request waiting or arriving while the server shuts down gets `503 ShuttingDown`. Nothing is written.
- The server puts no limit on how long a request waits. A client that stops waiting doesn't take its request out of
  the queue (see [The client leaving](#the-client-leaving)).

## The client leaving

- A client that leaves before its whole body is sent has asked for nothing. The server drops the request, answers
  nothing, and changes nothing. A call on a transaction leaves the transaction as it is.
- A client that leaves during a call on a transaction, once its body is sent, doesn't end the transaction. The call
  goes to the end; only a streamed read stops, at its next line. The transaction then ends by
  [`DELETE /tx/{txId}`](/docs/http-api/transactions/#abandon), or after `txIdleTimeout` (see
  [Configuration](/docs/operations/configuration/)).
- A request that joined the queue goes to the end, even if its client leaves while it waits. The client then can't
  tell whether it was written: it's a lost response, and the client handles it as such (see
  [A lost response](/docs/http-api/transactions/#a-lost-response) for a commit, and
  [A lost response](/docs/http-api/projections/#a-lost-response) for a write of projections).

**Why a request can't leave the queue.** A client that leaves already has to handle a lost response, since the
connection can drop at any time. Running its request anyway costs it nothing more.

## Store ID header

The `X-Tamarackdb-Store` header carries the store ID (see [Store ID](/docs/concepts/store-id/)) on every response
that depends on the store:

| Response | Header |
|---|---|
| `QUERY /events` | On every page, empty pages included |
| `GET /projections/{type}/{id}` | On `200` and on `404` |
| `POST /projections` | On `200` |
| `POST /pause` | On `200` |

- No transaction endpoint carries it: the store ID never changes while a transaction lives (see
  [Store ID](/docs/concepts/store-id/)).
- A read takes the store ID in the same SQLite snapshot as the events or the projection it returns, so a response
  never pairs the data of one store ID with another.
- A write takes it in the same SQLite transaction as what it wrote.
- A request refused before it reads or writes (`400`, for example) carries no store ID.

## Errors

Every error from an endpoint uses one JSON envelope, with a stable code (see [Errors](/docs/http-api/errors/)).
