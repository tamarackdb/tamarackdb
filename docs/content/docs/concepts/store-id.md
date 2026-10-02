---
title: "Store ID"
description: "The store ID names the history a database file holds. Where it appears, when it changes, and why a position is always a store ID with a Sequence Position."
slug: "store-id"
weight: 5
---

The store ID names the history a database file holds. A Sequence Position only means something next to it.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## What it is

- A UUID, drawn when the database file is created, and kept in the file (see
  [Schema](/docs/server-internals/schema/)).
- It changes only when the store is emptied with [`POST /reset`](/docs/http-api/reset/), in dev mode. After a reset,
  Sequence Positions start over at 1, so sequence 5 names a different event.
- A backup file has a store ID of its own, drawn when the backup created it (see [Backup](/docs/backup/)).
- Two responses that carry the same store ID come from the same history.

## Where it appears

- In the `X-Tamarackdb-Store` header of every response that depends on the store (see
  [Conventions](/docs/http-api/conventions/#store-id-header)).
- In an Append Condition, next to its `afterSequence` (see [Append Condition](/docs/concepts/append-condition/)).

## Positions

A position is a pair: the store ID and a Sequence Position.

- A client that keeps a position MUST keep both together.
- If a later read returns a different store ID, the position no longer means anything: the client starts over from the
  beginning.
- A projector that keeps its position in a projection doesn't even see the change: a reset deletes that projection
  with the rest, and the projector starts from zero on its own.
