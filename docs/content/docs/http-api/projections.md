---
title: "Projections"
description: "Projections outside any transaction: writing them with POST /projections, reading one with its version, and deleting them in bulk for a rebuild."
slug: "projections"
weight: 6
---

Writing, reading, and deleting projections outside any transaction. A command writes its projections in a
[transaction](/docs/http-api/transactions/#writing-projections) instead. What a projection is, and how versions work,
is in [Concepts: Projections](/docs/concepts/projections/).

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## Writing projections

`POST /projections` creates, replaces, and deletes projections, all or nothing, in its turn. It serves an
asynchronous projector and a [rebuild](/docs/concepts/projections/#rebuilds): the client sends the version it read of
each projection itself.

```sh
curl -X POST http://127.0.0.1:8085/projections \
  -H "Content-Type: application/json" \
  -d '{
    "create":  [ { "type": "daily-sales", "id": "2026-10-03", "payload": "{\"total\":4230}" } ],
    "replace": [ { "type": "projector-position", "id": "daily-sales", "version": "1b2c3d4e-5f60-4718-9a0b-c1d2e3f4a5b6", "payload": "17" } ],
    "delete":  [ { "type": "daily-sales", "id": "2026-10-02", "version": "9f3c2a1e-7b4d-4c8e-a5f6-0d1e2f3a4b5c" } ]
  }'
```

| Key | What it holds |
|---|---|
| `create` | `type`, `id`, `payload`: a projection that MUST NOT exist yet |
| `replace` | `type`, `id`, `version`, `payload`: a projection that MUST still be at `version` |
| `delete` | `type`, `id`, `version`: a projection that MUST still be at `version` |

- Every key is optional, and a missing one is an empty list.
- A `payload` is a string, and an empty string is valid. A missing or `null` payload gets `400`, so a key dropped on
  the client (JavaScript's `JSON.stringify` drops `undefined` values) never writes an empty payload.
- The same `type` + `id` MUST NOT appear twice in one write, across the three lists. So the order in which the lists
  are applied doesn't matter.
- A write with nothing in it responds `200` right away, without waiting for a turn.
- The write waits for its turn (see [Waiting for a turn](/docs/http-api/conventions/#waiting-for-a-turn) and
  [The client leaving](/docs/http-api/conventions/#the-client-leaving)). In its turn, every projection is written,
  each one conditional on its version, in one SQLite transaction, or nothing is.

**Why three lists with versions.** In a transaction, the server knows the version read, since the read goes through
it. Outside one, only the client knows it: it sends it, and a `create` has none. One list with an operation flag
would leave combinations of keys to forbid; three lists with a fixed shape each leave none.

### Limits

Each limit is a setting, with its default in [Configuration](/docs/operations/configuration/#settings).

| Limit | Over it |
|---|---|
| `maxProjectionsPerWrite` projections, across the three lists | `400` |
| `maxProjectionSize` bytes per projection: its `type`, `id`, and `payload` together | `413` |
| `maxRequestBodySize` bytes for the whole body | `413` |

- Each error from a limit names its setting, for example `request carries 612 projections, more than
  maxProjectionsPerWrite (500)`, so a developer who hits one in development knows which setting to raise.
- A rebuild larger than `maxProjectionsPerWrite` writes in several calls.

### Response

`200 OK`, with the store ID in the `X-Tamarackdb-Store` header:

```json
{
  "create":  [ { "version": "5a6b7c8d-9e0f-4a1b-8c2d-3e4f5a6b7c8d" } ],
  "replace": [ { "version": "d4c3b2a1-0f9e-4d8c-b7a6-5f4e3d2c1b0a" } ]
}
```

- The new version of each created and replaced projection, in the order they were sent.
- Both lists are always present, empty if need be.

### Conflicts

A projection that isn't where the write expects it gets `409 ConcurrencyException`, and nothing is written. The
`message` names the projection by its place in the body:

| Cause | `message` |
|---|---|
| A `create` whose `type` + `id` already exists | `create[0] already exists` |
| A `replace` or `delete` whose version isn't the stored one, or whose projection no longer exists | `replace[0] no longer has the given version` |

After a `409`, the client reads the projection again, and decides again from what it finds (see
[Versions](/docs/concepts/projections/#versions)).

**Why a `delete` of a missing projection gets `409`.** The client sent a version, and a missing projection has none:
the condition doesn't hold, as for a `replace`. The client learns that another write removed the projection since its
read, a bulk delete for example.

### A lost response

If the response is lost, the client can send the same write again. If the first attempt went through, the retry
always gets `409`: each `create` already exists, and each `replace` or `delete` finds another version, or no
projection. A write of projections is never applied twice.

A `409` on a retry doesn't say whether the first attempt went through, or another write came first. Either way, the
client reads the projections again and decides from what it finds.

## Reading a projection

```sh
curl -i http://127.0.0.1:8085/projections/user-profile/123
```

```
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8
X-Tamarackdb-Version: 9f3c2a1e-7b4d-4c8e-a5f6-0d1e2f3a4b5c
X-Tamarackdb-Store: 5b0c7e2a-1f4d-4a9b-8c3e-6d2f1a0b9e47

{"name":"Ada Lovelace"}
```

- `200 OK`: the body is the payload exactly as written, with no JSON envelope around it, since its format is up to
  the writing application. The version is in the `X-Tamarackdb-Version` header: keep it to replace or delete the
  projection later.
- `404 ProjectionNotFound`: no projection exists at that `type` + `id`. This is an ordinary answer, not a failure: an
  application that gets a `404` usually creates the projection.
- Both carry the store ID in the `X-Tamarackdb-Store` header.
- The read sees committed projections only, and never waits for a write.
- `type` and `id` are path segments, so they're percent-encoded: an `id` of `a/b` is
  `/projections/user-profile/a%2Fb`, and a space is `%20`. The same goes for `DELETE /projections/{type}`.

## Bulk delete

```sh
curl -X DELETE http://127.0.0.1:8085/projections/user-profile
curl -X DELETE http://127.0.0.1:8085/projections
```

- `DELETE /projections/{type}` deletes every projection of one type. `DELETE /projections` deletes every projection.
  Both respond `204 No Content`.
- Neither takes a version: they're meant for a [rebuild](/docs/concepts/projections/#rebuilds), and never report a
  conflict.
- Both wait for their turn (see [Waiting for a turn](/docs/http-api/conventions/#waiting-for-a-turn)):
  - a write queued before the delete goes through first, and the delete then removes what it wrote;
  - a later write that replaces or deletes a projection the delete removed gets `409`;
  - a delete can get `503 WriteQueueFull`;
  - a delete that joined the queue runs, even if its client leaves (see
    [The client leaving](/docs/http-api/conventions/#the-client-leaving)).
- The store ID doesn't change.
