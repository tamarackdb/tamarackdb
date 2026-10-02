---
title: "Lifecycle"
slug: "lifecycle"
weight: 4
---

How the server starts, stops, and fails.

## Startup

1. It prints a banner and its resolved configuration to stdout (see [Install](/docs/operations/install/#startup-banner)).
2. It opens the store:
   - It takes the lock on the database file (see [SQLite](/docs/server-internals/sqlite/#one-process-per-file)).
   - It creates the file if it doesn't exist, with the current schema and a new store ID.
   - It checks the schema version (see [Schema](/docs/server-internals/schema/#schema-version)).
   - It reads the highest `sequence` into the [Sequence Position counter](/docs/server-internals/sequence-counter/),
     and the store ID into memory next to it.
3. It serves requests.

## What lives only in memory

- The FIFO's state (the request holding the turn, the requests waiting) and the write counters are held only in memory,
  for the life of the process. Nothing is saved, and nothing is rebuilt: a new process starts with an empty FIFO,
  which is right, since every client waiting before a crash lost its connection too.
- The Sequence Position counter and the store ID are rebuilt at startup, from the database, because they have to match
  what's on disk.

## Shutdown

On `SIGINT` or `SIGTERM`, the server shuts down in order:

1. The HTTP server stops taking new connections (`http.Server.Shutdown`, capped at 10 seconds).
2. At the same moment, the [write FIFO](/docs/server-internals/write-fifo/) closes: requests still waiting get
   `503 ShuttingDown`, and so does any later one. A request already holding the turn finishes.
3. The HTTP server finishes the requests still in flight.
4. The store closes last, releasing its connections and the `.lock` file.

**Why the FIFO closes first.** A request waiting in it only ends once it gets its turn, so the HTTP server would
otherwise wait for it.

## Connection timeouts

- `ReadHeaderTimeout` is 10 seconds: a connection that never finishes sending its request headers is closed.
- `IdleTimeout` is 2 minutes: a keep-alive connection with no request for that long is closed.
- A `QUERY /events` page has its own limit per line (see [Reads](/docs/server-internals/reads/#a-stalled-page)).

## Failures

- **A panic in a request handler** is caught by the HTTP server, without crashing the process. The handler's deferred
  rollback and the release of its turn still run during the unwind, so no turn stays taken. A write's reserved
  Sequence Positions are given back the same way.
- **A process crash** (an unrecovered panic, `SIGKILL`, an out-of-memory kill) takes the whole in-memory state with
  it, so nothing is left to leak.
- **A crash during a write** leaves an uncommitted WAL transaction, discarded the next time a connection opens. Neither
  the write's events nor its projections ever become visible, in whole or in part. The counter, read back from the
  database, matches.

## Fatal storage errors

- A SQLite error that suggests the file itself may be damaged (an I/O error, detected corruption, failure to open the
  database file) is fatal: the server logs it and drives the same ordered shutdown as a signal.
- A temporary SQLite error (a busy lock during a WAL checkpoint, say) isn't fatal: it fails that one request, and
  nothing it would have written is kept.
- Any error while opening the store at startup is fatal.

**Why.** No per-error recovery logic: a clean restart is cheap and safe given the in-memory state above, and `/health`
and a process supervisor are already set up to catch it.
