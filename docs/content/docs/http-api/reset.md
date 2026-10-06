---
title: "Reset"
description: "POST /reset, in development mode only and during a pause: empties the store and draws a new store ID, for tests, between pause and resume."
slug: "reset"
weight: 9
---

`POST /reset` empties the store, for tests. It exists only in development mode (see
[Development mode](/docs/operations/dev-mode/)). Never rely on it against a production instance.

It's accepted only during a [pause](/docs/http-api/pause/). A test suite resets the store between two tests with three
calls:

```sh
curl -X POST http://127.0.0.1:8085/pause
curl -X POST http://127.0.0.1:8085/reset
curl -X POST http://127.0.0.1:8085/resume
```

## What it does

- In one SQLite transaction, it deletes every event and every projection, sets the Sequence Position counter back so
  the next event gets sequence 1, and draws a new store ID (see [Store ID](/docs/concepts/store-id/)).
- It responds `204 No Content`.
- Outside a pause in place, a requested one included, it gets `409 NotPaused`, and nothing changes (see
  [What runs during what](/docs/http-api/conventions/#what-runs-during-what)).
- It leaves the pause in place: `POST /resume` lets transactions begin again.
- For the application, a reset is like a restart of the server on a brand new file.

A client library's test suite can start each test, or each run, from an empty store instead of tracking what earlier
tests left behind. A transaction left open by a failed test holds the pause back until `txIdleTimeout`: a test suite
can set it short.

**Why only during a pause.** During a pause, no transaction exists: a reset never falls in the middle of one, and no
transaction ever sees the store ID change.

## Its turn

A reset waits for its turn like a write (see [Waiting for a turn](/docs/http-api/conventions/#waiting-for-a-turn)):

- The writes queued before it go through, then the reset deletes them.
- A write queued after it runs on the new store:
  - a `replace` or `delete` in `POST /projections` gets `409`, since its version no longer exists;
  - a `create` in `POST /projections` goes through, on the new store.
- A reset can get `503 WriteQueueFull`. A reset that joined the queue runs, even if its client leaves (see
  [The client leaving](/docs/http-api/conventions/#the-client-leaving)).
