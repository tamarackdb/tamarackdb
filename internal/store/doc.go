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
// ReadDecision takes the events and the highest Sequence Position in the
// same snapshot.
//
// A read loads all its events in memory, then ends its read transaction,
// before a single event is sent. It MUST NOT hold its connection, or pin
// its snapshot, while a client receives it. A client that processes each
// event as it arrives would hold a read connection for as long as it
// works. And while a snapshot is pinned, the WAL can't start over from
// its beginning (see Checkpoints): with several clients reading in turn
// while others write, it grows with every write. Memory is the price. A
// page is bounded by its limit; a decision read, by the limit its caller
// sets (package tx).
//
// # The Sequence Position counter
//
// The store assigns Sequence Positions itself, from a counter in memory,
// instead of SQLite's AUTOINCREMENT. This works because only one request
// at a time touches the write connection.
//
//   - Open reads the highest sequence in events and starts the counter
//     after it. An empty table starts at 1.
//   - DeleteAllEvents leaves the counter as it is: the next event
//     continues the positions.
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
// SQLite's automatic passive checkpoint runs once the WAL crosses its
// default size, without blocking any reader or writer. It copies the WAL
// into the database file only up to the snapshot of the oldest reader.
// The WAL starts over from its beginning only once everything is copied,
// and no reader still uses it when a write begins. Reads in turn, while
// writes go on, never leave that moment: every reader has a snapshot a
// little behind the last commit. During a rebuild with several
// projectors in parallel, the WAL then grows with every write, to the
// size of everything the rebuild writes. A WAL file never shrinks on its
// own.
//
// Checkpoint runs a TRUNCATE checkpoint instead. It takes the write lock,
// waits for the readers still in the WAL, copies all of it, and empties
// the file. A reader that begins once everything is copied reads the
// database file, and doesn't hold it back. It waits at most the busy
// timeout: a reader still there after that makes it give up, without
// error. Package writer decides when it runs.
//
// # Query planner statistics
//
// Optimize runs PRAGMA optimize. The server calls it at startup, and on
// POST /optimize, in its turn in the FIFO. events only grows, so
// statistics gathered once drift further from reality the longer the
// process runs. PRAGMA optimize only analyzes again the tables that
// changed enough to matter, so it's cheap enough to run often. A full
// ANALYZE never runs on its own: it's for a one-off bulk import, run by
// hand while the server is stopped.
//
// # The pause
//
// The pause is kept in the store table, in paused_at, and in memory next
// to the Sequence Position counter. It MUST survive a restart: an
// operation that relies on it, a rebuild for example, would otherwise see
// commands start again because the server restarted. SetPause and
// ClearPause write it on the write connection, in their caller's turn in
// the FIFO. DeleteAllEvents leaves it as it is. What a pause blocks, and
// how it begins and ends, is package tx's.
//
// # Schema version
//
// The schema version is PRAGMA user_version, built into the binary (see
// schema.go). It is the major version of TamarackDB. Open gives a file
// with no table the current schema and version. A file at any other
// version, older or newer, fails Open with a *SchemaVersionError. Open
// never changes the schema of an existing file.
//
// Migrate, run by tamarackdb-migrate while the server is stopped, is the
// only way to change it (see migrate.go). It carries one migration, from
// the previous major version to this one, and MUST NOT migrate from any
// other version: an operator who skips major versions runs each
// tamarackdb-migrate in turn. The migration and the new user_version MUST
// be committed in one transaction, so a failure leaves the file at the
// previous version. Migrate MUST take the lock Open takes: a server
// already running checks the version only at startup, and would go on
// writing with the old schema.
package store
