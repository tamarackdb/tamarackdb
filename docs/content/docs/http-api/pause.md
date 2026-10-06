---
title: "Pause"
description: "POST /pause and POST /resume: stop transactions from beginning, for a fixed point in the log, a rebuild, or a deploy, and let them begin again."
slug: "pause"
weight: 7
---

`POST /pause` stops transactions from beginning, and `POST /resume` lets them begin again. Events enter the log only
through a transaction: during a pause, no event is appended. Everything else goes on.

A pause serves an operation that needs the log to stand still:

- **A fixed point.** `POST /pause` returns the last Sequence Position, once the pause is in place. No event comes
  after it until `/resume`, so every projector can catch up to it.
- **A rebuild with no event missed.** Pause, delete the projections with a
  [bulk delete](/docs/http-api/projections/#bulk-delete), write them again with
  [`POST /projections`](/docs/http-api/projections/#writing-projections) up to the last Sequence Position, then resume.
- **A deploy.** Pause, switch the application to its new version, then resume: no command decides an event on the old
  version that the new one would read.

TamarackDB doesn't know why a pause is asked for, and never ends one on its own. A single process of the application
coordinates `/pause` and `/resume`: the pause has no owner, and the first `/resume` ends it for everyone.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## `POST /pause`

```sh
curl -X POST http://127.0.0.1:8085/pause
```

Each call waits for its turn (see [Waiting for a turn](/docs/http-api/conventions/#waiting-for-a-turn)). In its turn,
it looks at where the pause stands, and answers without waiting for anything else:

- **Transactions are still open.** The pause is requested, if it isn't already: from then on, `POST /tx` gets
  `503 Paused`. The open transactions go on normally, up to their commit. The response is `202 Accepted`, with how
  many are open:

  ```json
  {"openTransactions":3}
  ```

- **No transaction is open.** The pause takes hold. The response is `200 OK`, with the last Sequence Position, and the
  store ID in the `X-Tamarackdb-Store` header. The events of every transaction that has ended come before that
  position.

  ```
  200 OK
  X-Tamarackdb-Store: 5b0c7e2a-1f4d-4a9b-8c3e-6d2f1a0b9e47

  {"lastSequence":5042}
  ```

- **The pause is in place.** `/pause` does nothing, and responds `200` with the same last Sequence Position.

The coordinator MUST call `/pause` again, after a short delay, until it gets `200`:

```sh
pause() { curl -s -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:8085/pause; }
until [ "$(pause)" = 200 ]; do sleep 1; done
```

- `200` always means the pause is in place.
- A lost response is settled by calling `/pause` again: it answers `200` with the same last Sequence Position.
- `503 WriteQueueFull` or `503 ShuttingDown`: nothing changes, and the coordinator calls again.
- A requested pause stays until `/resume` withdraws it. A coordinator that dies during its loop leaves it in place,
  like a forgotten pause: [`GET /stats`](/docs/operations/observability/) shows it.

**Why wait for the open transactions.** A command that began before the pause goes to the end, and its work isn't
lost. And during a pause, no transaction exists: the rule has no exception.

**Why the coordinator calls again.** A `/pause` that waited in the server would hold a connection for as long as the
transactions last. Answering at once keeps nothing on the server between two calls. The coordinator is a central
process that already drives the whole operation: a loop costs it little.

## `POST /resume`

```sh
curl -X POST http://127.0.0.1:8085/resume
```

- Without a pause, it does nothing, and responds `204 No Content` at once.
- Otherwise, it waits for its turn, then ends the pause, or the requested pause, and responds `204`. Transactions can
  begin again.
- It can get `503 WriteQueueFull` or `503 ShuttingDown`, like any request that waits for its turn. Nothing changes
  then.

**Why it waits for its turn.** `/pause` and `/resume` both go through the queue: they never cross. Each one finds, in
its turn, the state the other left, and does what it would do on its own.

## What a pause blocks

During a pause, requested or in place, `POST /tx` gets `503 Paused`, and nothing else is refused. `POST /reset`, in
development mode, is accepted only during a pause in place. The table of what each request does in each state is in
[What runs during what](/docs/http-api/conventions/#what-runs-during-what).

What an application does with a command refused by `503 Paused` is up to it.

## How long it lasts

- A pause lasts until `POST /resume`. The server never ends one on its own.
- A pause survives a restart of the server. A requested pause doesn't: the server restarts without one, and the
  coordinator calls `/pause` again.
- A backup never copies the pause (see [Backup](/docs/operations/backup/)).

**Why it survives a restart.** An operation relies on it. If the server restarted during a rebuild and lost the
pause, commands could append events the rebuild would never see.

A pause in place shows in [`GET /health`](/docs/operations/health-check/), and a requested one in
[`GET /stats`](/docs/operations/observability/).
