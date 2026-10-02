---
title: "Projections"
description: "Reading one projection with GET /projections/{type}/{id}, its version header, and deleting projections in bulk, by type or all at once, for a rebuild."
slug: "projections"
weight: 5
---

Reading one projection, and deleting projections in bulk. Projections are written with
[`POST /write`](/docs/http-api/write/). What a projection is, and how versions work, is in
[Concepts: Projections](/docs/concepts/projections/).

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
- `404 ProjectionNotFound`: no projection exists at that `type` + `id`. This is an ordinary answer, not a failure: a
  projector that gets a `404` usually creates the projection.
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
- Both wait for their turn like a write (see [Writing](/docs/http-api/write/#waiting-for-a-turn)):
  - a write queued before the delete goes through first, and the delete then removes what it wrote;
  - a later write that replaces or deletes a projection the delete removed gets `409`;
  - a delete can get `503 WriteQueueFull`;
  - closing the connection while it waits takes it out of the queue, with nothing deleted.
- The store ID doesn't change.
