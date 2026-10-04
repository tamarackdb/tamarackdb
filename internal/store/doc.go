// Package store is TamarackDB's SQLite storage: the schema, the
// translation of a query into SQL, the reads, and the transaction of a
// write. It knows nothing of HTTP, JSON envelopes, configuration, or the
// FIFO.
//
// # One file
//
// Events and projections live in one file, tamarackdb.sqlite, in the data
// directory, so one SQLite transaction covers both. Only the directory is
// configurable.
//
// SQLite reuses the space of deleted projections on its own. Giving it
// back to the operating system takes a VACUUM, run by hand. The server
// never runs one.
//
// # One process per file
//
// Before touching the SQLite file, Open takes an exclusive, non-blocking
// flock(2) on a sibling <path>.lock file, and holds it until Close. A
// second process finds the lock held and fails with ErrDatabaseLocked. The
// operating system releases the lock on exit or crash, so no leftover lock
// file can block a later start. One connection, WAL, and a busy timeout
// only keep writes in order inside one process: nothing else would stop a
// second process from opening the same file and racing the first. This
// relies on flock(2): TamarackDB targets Linux only.
//
// # Connections
//
// The write pool has exactly one connection, so the SQLite driver keeps
// the same single writer as the FIFO. It opens every transaction with
// BEGIN IMMEDIATE: a write's condition checks, projection writes, and
// inserts all run under SQLite's write lock, with no window where another
// connection could slip in between them.
//
// The read pool has readPoolSize connections, read-only.
//
// Every connection sets, at open:
//   - foreign_keys = ON: SQLite reads foreign keys but doesn't enforce
//     them by default. It catches bugs, such as a tag row written for an
//     event that doesn't exist.
//   - journal_mode = WAL, for reads that never wait for a write.
//   - synchronous = FULL: one more fsync per commit than the NORMAL mode
//     WAL usually pairs with. At TamarackDB's write volume the cost
//     doesn't matter, and it buys the strongest durability SQLite offers,
//     for what is each application's single source of truth.
//   - busy_timeout = 5000 (five seconds). The FIFO already lets one
//     request at a time use the write connection: the timeout only guards
//     against something else briefly holding the file, a checkpoint or an
//     external sqlite3 shell.
//
// # Reads
//
// A read runs on the read pool, in a read transaction of its own. WAL
// mode gives it a steady snapshot of committed data: it never sees a
// write in progress, and a write never blocks it. A read that starts just
// before a commit doesn't see the new events. That's fine: a client that
// then writes uses the Sequence Position it actually read.
//
// Each read takes the store ID first, then the events or the projection.
// SQLite takes the snapshot at the first statement, so both come from the
// same one: a response never pairs the data of one store ID with another,
// even if a reset commits in between. ReadDecision takes the highest
// Sequence Position in that same snapshot.
//
// A read holds its connection, and pins its snapshot, until its
// EventIterator is closed. Package api bounds how long a client may take
// to receive it.
//
// # The Sequence Position counter
//
// The store assigns Sequence Positions itself, from a counter in memory,
// instead of SQLite's AUTOINCREMENT. This works because only one request
// at a time touches the write connection.
//
//   - Open reads the highest sequence in events and starts the counter
//     after it. An empty table starts at 1.
//   - Reset sets the counter back, so the next event gets 1.
//   - Append reserves its positions only once every condition holds and
//     every projection is written. A projection conflict then ends the
//     write before any position is taken.
//   - Unless the commit succeeds (the insert fails, the commit fails, or
//     the code panics), Append gives its positions back before it ends. A
//     failed write leaves no gap in the sequence.
//
// Knowing every event's sequence up front lets Append insert its events in
// one multi-row INSERT, then the tags in one INSERT per tag table, instead
// of a round trip per event.
//
// The counter often answers an Append Condition with no SQL at all:
//   - If the condition's afterSequence is the last assigned position, no
//     event exists after it, so its query can't match anything: the
//     condition holds.
//   - A condition with afterSequence and no query only asks whether any
//     event exists after it: the counter answers.
//
// The shortcut changes how the answer is reached, never the answer.
//
// # Checkpoints
//
// The store relies on SQLite's automatic passive checkpoint, run once the
// WAL crosses its default size, without blocking any reader or writer.
// Two things can hold it back: a long read, which pins a snapshot, and a
// large write, whose changes can't be checkpointed before it commits.
//
// # Query planner statistics
//
// Optimize runs PRAGMA optimize. The server calls it every hour, in its
// turn in the FIFO. events only grows, so statistics gathered once drift
// further from reality the longer the process runs. PRAGMA optimize only
// analyzes again the tables that changed enough to matter, so it's cheap
// enough to run often. A full ANALYZE never runs on its own: it's for a
// one-off bulk import, run by hand while the server is stopped.
//
// # Schema version
//
// The schema version is PRAGMA user_version, built into the binary (see
// schema.go). Open creates a new file with the current schema and
// version. A file at any other version, older or newer, fails Open with a
// *SchemaVersionError. The store never changes its own schema.
package store
