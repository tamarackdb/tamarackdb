---
title: "SQLite"
slug: "sqlite"
weight: 5
---

How TamarackDB uses SQLite as its storage engine.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## Why SQLite

- The volume (several million rows across the tag tables) fits comfortably, with `name + value` indexes.
- No external network or process to depend on.
- One writer, in line with a single process ("single brain"): the server is the only writer for as long as it runs.
- WAL mode lets reads run while a write is in progress, without blocking.
- A plain, open file format: the database can be opened and queried with ordinary SQLite tools, and backed up with
  SQLite's own tools (`.backup`, `VACUUM INTO`) instead of a raw copy, which can miss commits still in the WAL.

## One file

- Events and projections live in one file, `tamarackdb.sqlite`, in `dataDir`, so one SQLite transaction covers both.
  Only the directory is configurable; the file name is fixed.
- Projections are larger than events and are rewritten in place, so they make the WAL grow faster than events alone
  would. This stays small in practice: a write carries the projections of one command, typically about ten. The
  heavy case is a projection rebuild.
- SQLite reuses the space of deleted projections on its own. Giving it back to the operating system takes a `VACUUM`,
  run by hand (see [Maintenance](/docs/operations/maintenance/)). The server never runs one.

## One process per file

- Before touching the SQLite file, the store takes an exclusive, non-blocking `flock(2)` on a sibling
  `<path>.lock` file, and holds it for the life of the process.
- A second process finds the lock held and fails at startup with `ErrDatabaseLocked`.
- The OS releases the lock on exit or crash, so no leftover lock file can block a later start.
- This relies on `flock(2)`: TamarackDB targets Linux only. Docker covers every other platform.

**Why.** One connection, WAL, and a busy timeout only keep writes in order inside one process. Nothing would stop a
second server process from opening the same file and racing the first.

## Connections

- **Write pool**: exactly one connection (`SetMaxOpenConns(1)`), so the SQLite driver enforces the same single writer
  as the [write FIFO](/docs/server-internals/write-fifo/).
- **Read pool**: `readPoolSize` connections, read-only (see [Reads](/docs/server-internals/reads/)).
- Every connection sets, at open:
  - `PRAGMA foreign_keys = ON` (see [Schema](/docs/server-internals/schema/#choices));
  - `PRAGMA journal_mode = WAL`, the mode this design assumes throughout, for MVCC reads and checkpoints;
  - `PRAGMA synchronous = FULL`;
  - `_busy_timeout = 5000` (five seconds).
- The write connection opens every transaction with `BEGIN IMMEDIATE` (`_txlock=immediate`), taking SQLite's write
  lock at the start of the transaction.

**Why `FULL`.** It costs one extra fsync per commit compared to the `NORMAL` mode WAL usually pairs with. At this
scope's write volume the cost doesn't matter, and it buys the strongest durability SQLite offers, for what is each
application's single source of truth.

**Why `BEGIN IMMEDIATE`.** A write's condition checks, projection writes, and inserts all run under the lock, with no
window where another connection could slip in between them. A bulk delete of projections is a single statement that
commits on its own.

**Why the busy timeout.** The FIFO already lets one request use the one write connection at a time. The timeout only
guards against something else briefly holding the file: a passive checkpoint, or an external `sqlite3` shell.

## Checkpoints

- The server relies on SQLite's automatic passive checkpoint, triggered once the WAL crosses its default size,
  without blocking any reader or writer. There is no checkpoint goroutine or schedule.
- Two things can hold it back:
  - a long read, which pins a snapshot (bounded by pagination, and by the 30-second limit on a stalled page, see
    [Reads](/docs/server-internals/reads/));
  - a large write, whose changes can't be checkpointed before it commits (a rebuild sent in one write, for example).

## Query planner statistics

- Once an hour, the server runs `PRAGMA optimize` on the write connection. It waits for its turn in the write FIFO, so
  it never runs during a write.
- A full `ANALYZE` never runs automatically. It's the right tool after a one-off bulk import, run by hand while the
  server is stopped (see [Maintenance](/docs/operations/maintenance/)).

**Why.** `events` only grows, so statistics gathered once drift further from reality the longer the process runs.
`PRAGMA optimize` only re-analyzes tables that changed enough to matter, or have no statistics yet, so it's cheap
enough to run often.
