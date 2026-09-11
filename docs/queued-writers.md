# Queued Writers — Simplifying Append Concurrency

**Status:** proposed. This document is not yet reflected in [design.md](design.md); it captures a
simplification of the "Concurrency handling in Go" section discussed separately, for review before
it replaces that section.

## Motivation

The current design (`design.md`, "Concurrency handling in Go") centers on a gatekeeper that tracks
every in-flight Append Condition and only serializes writers whose conditions actually conflict,
letting non-conflicting writers proceed in parallel.

That parallelism doesn't reach SQLite, though. The write connection pool is capped at one
connection (`writeDB.SetMaxOpenConns(1)`, see "Storage: SQLite"), consistent with SQLite's own
single-writer semantics in WAL mode. Two writers "granted" at the same time by the gatekeeper still
contend for that single connection immediately afterward — so the conflict-detection matrix doesn't
buy real concurrent access to the database, only a more elaborate admission step ahead of a
bottleneck that serializes everyone regardless of conflict. At TamarackDB's target scope — modest
throughput, few concurrent writers (see "Production reference data" in `design.md`) — this
complexity isn't earning its cost.

Two writers with unrelated Append Conditions were never going to write to SQLite at the same time
in the first place. Whether that serialization happens implicitly (both reach the connection pool
and one blocks, order decided by whichever goroutine's `Begin()` wins the race) or explicitly (an
application-level FIFO queue), the outcome for the two writers is the same. The explicit version is
simpler to reason about and gives the store a queue it can actually observe, bound, and cancel
against — instead of leaving that ordering to database/sql's internal connection-pool queuing and
SQLite's `busy_timeout` retries.

## Design: the queue manager

The gatekeeper is replaced by a **queue manager**: a strict FIFO admission queue for any request
that needs exclusive write access to SQLite, with no awareness of a writer's Append Condition or
the events it intends to write. "Writer" is used broadly here — it covers `POST /append` and, in
dev mode, `DELETE /` (see Dev mode's `DELETE /` joins the same queue below). Both need the same
thing: to be the only one touching SQLite for the duration of their work.

There are exactly two states a writer can be in:
- **Active** — at most one writer at a time. The active writer is the only one allowed to touch
  SQLite.
- **Queued** — every other writer currently holding an open HTTP connection to `/append` or
  `DELETE /`, waiting its turn in arrival order.

**Flow for a writer:**
1. The HTTP handler asks the queue manager to join the FIFO.
2. If the queue is already at its configured depth (see Configuration below), the request is
   rejected immediately (see Rejecting when the queue is full) instead of joining.
3. Otherwise it waits until every writer ahead of it — the active one and everyone queued before
   it — has finished.
4. Once it becomes active, the handler does its actual work inside one SQLite transaction using
   the single write connection: for `/append`, that's checking the Append Condition and, if
   satisfied, inserting the events (see Application-controlled Sequence Position below); for
   `DELETE /`, that's wiping the tables, unconditionally.
5. The handler tells the queue manager it's done. The next queued writer, if any, becomes active.

**Why checking at execution time is still correct:** the Append Condition is verified immediately
before the insert, against the database's actual state at that moment, inside the same transaction
as the insert. Correctness never depended on comparing one writer's condition against another's
pending events — it depends on nothing else being able to write between the check and the insert,
which strict FIFO admission guarantees by construction, the same way the gatekeeper's conflict
detection did for the conflicting case. The queue manager simply applies that guarantee to every
writer uniformly rather than only to writers whose conditions were found to overlap.

**No conflict detection.** The queue manager never inspects `types`, `identifiers`, `metadata`, or
the new events being written. Every request is admitted to the FIFO purely by arrival order. This
removes the gatekeeper's conflict matrix entirely: there is no more Query-to-Query overlap
computation, and no distinction between conflicting and non-conflicting writers.

**Cancellation while queued.** A queued writer watches its own HTTP request context, same as
before. If the client disconnects or the request times out before its turn, it leaves the FIFO
immediately; every writer behind it simply moves up one position. No renumbering or bookkeeping is
needed beyond removing it from the queue.

**Cancellation while active.** If the active writer's context is canceled before its transaction
commits, the transaction is rolled back and the next queued writer becomes active. If the
transaction had already committed, cancellation afterward is a no-op from the store's perspective
— the write happened; only the response never reached the client (see "Startup and crash behavior"
in `design.md` for the same lost-acknowledgment case).

**`busy_timeout` becomes unimportant for writers.** Because at most one writer touches SQLite at a
time by construction, `_busy_timeout` no longer needs to absorb writer-to-writer contention on the
write connection; strict FIFO admission already guarantees a writer never attempts to begin its
transaction while another is mid-transaction.

## Application-controlled Sequence Position

Strict FIFO admission has a second consequence beyond concurrency: since at most one writer ever
touches SQLite at a time, TamarackDB can assign the Sequence Position itself, in application
memory, instead of relying on SQLite's `AUTOINCREMENT`.

**Schema change:** `events.sequence` becomes a plain `INTEGER PRIMARY KEY`, with the value supplied
by the application on insert, rather than `INTEGER PRIMARY KEY AUTOINCREMENT`.

**Startup:** before accepting any writers (reads are unaffected and can be served immediately),
TamarackDB reads the current highest `sequence` in the `events` table and keeps it in memory as
the next-sequence counter. An empty table starts the counter the same way `AUTOINCREMENT` would —
the first event gets sequence 1.

**When a writer becomes active:** the counter is only consulted, and only advanced, *after* the
writer's work has been confirmed to happen. For `/append`, that means computing the sequence
numbers for its batch of events only once the Append Condition has been checked and found to
hold — never before. This matters: if the condition fails, the writer inserts nothing and responds
`409 Conflict`, and the counter must not have moved, or every failed append would leave a
permanent gap in the sequence. Once the condition holds, the writer takes the next N values (N
being the number of events in the batch), advances the counter by N, and uses those values for the
insert.

**Why this doesn't remove the events-before-tags ordering:** `PRAGMA foreign_keys = ON` is checked
immediately, not deferred to commit, so a row in `identifiers` or `metadata` still can't reference
an `event_sequence` that doesn't exist yet in `events` within the same transaction. What
application-controlled sequencing removes isn't that ordering — it's the round trip through SQLite
to learn each event's ID before its tags can be written. Knowing every event's sequence upfront
means the whole batch's `events` rows can be written as one multi-row `INSERT`, followed by one
multi-row `INSERT` into `identifiers` and one into `metadata` — instead of interleaving a
per-event round trip to fetch an ID between each event and its tags. This matters more as a batch
grows: a single `/append` call can carry up to 100 events with up to 20 identifiers and 20
metadata entries each, up to 4,100 rows in the extreme case.

## Dev mode's `DELETE /` joins the same queue

In the current design, `DELETE /` (see "Dev mode" in `design.md`) runs outside the gatekeeper's
reservation tracking entirely — a deliberate choice, since it's meant for local development and
test environments only. That leaves it free to race an in-flight append: both reach SQLite through
the same single connection, with no defined contract for which one wins.

Under the queue manager, `DELETE /` stops being a special case. It's a writer like any other: it
joins the same FIFO, waits its turn behind whatever is active or already queued, and once active,
wipes the tables — unconditionally, with no Append Condition to check — then releases, the same as
`/append`. It counts toward `maxQueuedWriters` and gets the same `503` / `Retry-After` treatment if
the queue is full. Reusing the mechanism removes the race for free: a wipe can no longer land
mid-append, or an append mid-wipe, since only one of them is ever active at a time.

The in-memory sequence counter is deliberately left untouched by a wipe. The tables become empty,
but the counter keeps climbing from wherever it was — functionally correct (nothing requires
Sequence Position to restart at 1), and it avoids a dev-mode-only reset path for a feature that
exists for convenience, not correctness.

## Rejecting when the queue is full

An optional cap, `maxQueuedWriters`, bounds how many writers may wait in the FIFO at once, so the
server doesn't accumulate an unbounded number of open HTTP connections during a burst. A request
arriving when the queue is already at that depth is rejected outright rather than joining:

```
HTTP/1.1 503 Service Unavailable
Retry-After: 1

{ "error": "AppendQueueFull" }
```

`Retry-After` gives the client a concrete backoff hint instead of leaving it to guess.

## Configuration

One new key, following the existing precedence rules (config file, then `TAMARACKDB_*` environment
variable, then built-in default — see "Configuration" in `design.md`):

| Key | Environment variable |
|---|---|
| `maxQueuedWriters` | `TAMARACKDB_MAX_QUEUED_WRITERS` |

There's no single right default: `maxQueuedWriters` should be sized against the number of
concurrent users the owning application expects, discounted for the fact that a given user isn't
continuously appending — a burst of simultaneous writers is normally a small fraction of total
users, not all of them at once.

## Terminology changes

| Gatekeeper (current) | Queue manager (proposed) |
|---|---|
| Gatekeeper | Queue manager |
| Reservation | — (no equivalent; admission is unconditional) |
| Held reservation | Active writer |
| Queued reservation request | Queued writer |
| Granted | Admitted |
| Released | Done / finished |
| Conflicting / non-conflicting reservations | — (removed; no conflict concept) |

## Effect on observability

`GET /metrics` and `GET /debug` (see "Nice to have: gatekeeper observability" in `design.md`)
simplify along with the mechanism:
- `/metrics` still reports whether a writer is active, how many are queued, and the longest current
  wait — but drops anything conflict-related, since there's nothing left to compute.
- `/debug` reports the active writer and the ordered list of queued writers (their age), instead of
  a list of held reservations with their Query and pending events.

## Sections of `design.md` this would replace or touch

- "Concurrency handling in Go" — rewritten in full: "Principle: the reservation manager",
  "Determining whether two reservations conflict", and "Behavior based on condition overlap" are
  removed.
- "Reads" — carries over with gatekeeper → queue manager renamed throughout, except for "Sequence
  Position assignment", which reverses instead of just renaming: it currently states the gatekeeper
  "never tracks or assigns it itself; only the actual commit order matters." Under the queue
  manager, the opposite is true — it's the active writer that assigns the Sequence Position, in
  memory, before insert (see Application-controlled Sequence Position above).
- Schema (`CREATE TABLE events`) — `sequence INTEGER PRIMARY KEY AUTOINCREMENT` becomes
  `sequence INTEGER PRIMARY KEY`.
- "Startup and crash behavior" — gatekeeper → queue manager renamed throughout (e.g. "the
  gatekeeper's reservation state is purely transient" and "the handler's reservation release is
  deferred"), plus a new startup step that reads the current highest `sequence` into memory before
  accepting writers, alongside the existing `PRAGMA user_version` check.
- "Dev mode" — `DELETE /` no longer runs outside the queue manager's tracking; it joins the same
  FIFO as `/append` (see Dev mode's `DELETE /` joins the same queue above).
- "Configuration" — add `maxQueuedWriters` / `TAMARACKDB_MAX_QUEUED_WRITERS` to the table.
- "Nice to have: gatekeeper observability" — renamed and updated per Effect on observability above.
- "Error responses" — add the `AppendQueueFull` case alongside the existing `400` / `409` / `413`
  cases.
- `docs/usage.md` references "the reservation manager" in its intro — would need updating to "the
  queue manager" for consistency.
