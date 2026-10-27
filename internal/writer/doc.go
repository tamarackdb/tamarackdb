// Package writer runs TamarackDB's writes on top of the FIFO in package
// queue and the store in package store. A write waits for its turn in the
// FIFO, runs alone on the write connection, in a SQLite transaction of
// its own, then gives the turn to the next request. Everything that
// touches the write connection goes through a Writer: the commit of a
// transaction, POST /projections, the bulk deletes of events and
// projections, and PRAGMA optimize.
//
// # A write that has started goes to the end
//
// A request that joined the FIFO goes to the end, even if its client
// disconnects while it waits (see package queue). Its work gets the
// context without its cancellation (context.WithoutCancel). database/sql
// rolls a SQLite transaction back when its context is cancelled: a client
// that left would otherwise undo its write at a random point. The client
// deals with not knowing the outcome instead.
//
// # Time
//
// WritePending keeps the time each event already carries: in a
// transaction, an event gets its time when its write reaches the server,
// long before the commit.
//
// # Checkpoints
//
// A turn whose write took the WAL past 64 MiB ends with a TRUNCATE
// checkpoint, before the next request gets the turn (see
// Writer.checkpoint, and package store for why the WAL would grow
// otherwise). The request that crossed the threshold waits for it.
//
// # Counters
//
// A Writer counts, since startup, the writes committed, the writes
// refused by a conflict, the requests turned away by a full FIFO, and
// the checkpoints that succeeded or gave up (see Stats). They're kept in
// memory only, for GET /stats.
package writer
