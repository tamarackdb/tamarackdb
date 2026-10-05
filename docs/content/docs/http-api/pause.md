---
title: "Pause"
description: "POST /pause and POST /resume: stop transactions from beginning, for a fixed point in the log, a rebuild, or a deploy, and let them begin again."
slug: "pause"
weight: 7
---

`POST /pause` stops transactions from beginning, and `POST /resume` lets them begin again. Events enter the log only
through a transaction: during a pause, no event is appended. Everything else goes on.

A pause serves an operation that needs the log to stand still:

- **A fixed point.** `POST /pause` returns the last Sequence Position. No event comes after it until `/resume`, so
  every projector can catch up to it.
- **A rebuild with no event missed.** Pause, delete the projections with a
  [bulk delete](/docs/http-api/projections/#bulk-delete), write them again with
  [`POST /projections`](/docs/http-api/projections/#writing-projections) up to the last Sequence Position, then resume.
- **A deploy.** Pause, switch the application to its new version, then resume: no command decides an event on the old
  version that the new one would read.

TamarackDB doesn't know why a pause is asked for, and never ends one on its own.

## `POST /pause`

```sh
curl -X POST http://127.0.0.1:8085/pause
```

```
200 OK
X-Tamarackdb-Store: 5b0c7e2a-1f4d-4a9b-8c3e-6d2f1a0b9e47

{"lastSequence":5042}
```

A pause takes hold in three steps:

1. **The pause is requested.** From the call on, `POST /tx` gets `503 Paused`. The transactions already open go on
   normally, up to their commit.
2. **The wait.** The server waits for every open transaction to end: commit, abandon, error, or expiry after
   `txIdleTimeout`.
3. **The pause.** Once no transaction is left, the pause waits for its turn (see
   [Waiting for a turn](/docs/http-api/conventions/#waiting-for-a-turn)), and in its turn, takes hold. The response
   gives the last Sequence Position, and the store ID in the `X-Tamarackdb-Store` header. The events of the
   transactions that committed during the wait come before that position.

- During a pause, `/pause` does nothing, and responds `200` at once, with the same last Sequence Position.
- While a pause is requested, a second `/pause` waits with the first, and gets the same response.
- A client that disconnects while its `/pause` waits doesn't withdraw the request: the pause still takes hold.
- `409 PauseCancelled`: the request was withdrawn by `/resume`, or by a reset, before the pause took hold.
- `503 WriteQueueFull` or `503 ShuttingDown` at step 3: the request is withdrawn.

**Why wait for the open transactions.** A command that began before the pause goes to the end, and its work isn't
lost. And during a pause, no transaction exists: the rule has no exception.

## `POST /resume`

```sh
curl -X POST http://127.0.0.1:8085/resume
```

- Without a pause, it does nothing, and responds `204 No Content` at once.
- Otherwise, it waits for its turn, then ends the pause, or withdraws a requested one, and responds `204`. Transactions
  can begin again.
- It can get `503 WriteQueueFull` or `503 ShuttingDown`, like any request that waits for its turn. Nothing changes
  then.

**Why it waits for its turn.** A pause takes hold in its own turn. Taking a turn too, `/resume` can't cross it: if the
pause comes first, it takes hold, then ends; if `/resume` comes first, the pause finds its request withdrawn, and
`/pause` gets `409`.

## What a pause blocks

During a pause, `POST /tx` gets `503 Paused`, and nothing else is refused:

- reads of events and of projections;
- `POST /projections`, and the bulk deletes of projections;
- `GET /health` and `GET /stats`;
- `POST /reset`, in development mode, which ends the pause.

What an application does with a command refused by `503 Paused` is up to it.

**Why projections go on.** A pause holds the log still, not the projections. An operation run during a pause, a
rebuild for example, has to write them.

## How long it lasts

- A pause lasts until `POST /resume`. The server never ends one on its own, except at a reset.
- A pause survives a restart of the server. A requested pause doesn't: the server restarts without one.
- A backup never copies the pause (see [Backup](/docs/operations/backup/)).

**Why it survives a restart.** An operation relies on it. If the server restarted during a rebuild and lost the
pause, commands could append events the rebuild would never see.

A pause in place shows in [`GET /health`](/docs/operations/health-check/), and a requested one in
[`GET /stats`](/docs/operations/observability/).
