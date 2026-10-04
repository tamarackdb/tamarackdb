---
title: "Reset"
description: "POST /reset, in development mode only: empties the store and draws a new store ID, for tests. What happens to the writes queued before and after it."
slug: "reset"
weight: 7
---

`POST /reset` empties the store, for tests. It exists only in development mode (see
[Development mode](/docs/operations/dev-mode/)). Never rely on it against a production instance.

```sh
curl -X POST http://127.0.0.1:8085/reset
```

## What it does

- In one SQLite transaction, it deletes every event and every projection, sets the Sequence Position counter back so
  the next event gets sequence 1, and draws a new store ID (see [Store ID](/docs/concepts/store-id/)).
- It responds `204 No Content`.
- For the application, a reset is like a restart of the server on a brand new file.

A client library's test suite can start each test, or each run, from an empty store instead of tracking what earlier
tests left behind.

## Its turn

A reset waits for its turn like a write (see [Writing](/docs/http-api/write/#waiting-for-a-turn)):

- The writes queued before it go through, then the reset deletes them.
- A write queued after it runs on the new store:
  - a condition that carries the old store ID gets `409`;
  - a `replace` or `delete` of a projection gets `409`, since its version no longer exists;
  - a condition with no `afterSequence`, or a write with no condition, goes through: it holds on any store.
- A reset can get `503 WriteQueueFull`. Closing the connection while it waits takes it out of the queue, and nothing is
  deleted.
