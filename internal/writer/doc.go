// Package writer runs TamarackDB's writes on top of the FIFO in package
// queue and the store in package store. A write waits for its turn in the
// FIFO, runs alone on the write connection, in a SQLite transaction of
// its own, then gives the turn to the next request. Everything that
// touches the write connection goes through a Writer: the commit of a
// transaction, POST /projections, the bulk deletes of projections, a
// reset, and the hourly PRAGMA optimize.
//
// # A write that has started goes to the end
//
// A request's context ends its wait: if the client disconnects while
// waiting, or just as its turn comes, the request leaves the FIFO and its
// work never runs. Once the work runs, it gets the context without its
// cancellation (context.WithoutCancel). database/sql rolls a SQLite
// transaction back when its context is cancelled: a client that left
// halfway through would otherwise undo its write at a random point. The
// client deals with not knowing the outcome instead.
//
// # Time
//
// WritePending keeps the time each event already carries: in a
// transaction, an event gets its time when its write reaches the server,
// long before the commit.
//
// # Store ID
//
// WritePending checks, in its turn, that the store ID is still the one
// the transaction began on. A reset also runs in its turn, so the store ID
// can't change between that check and the commit.
//
// # Counters
//
// A Writer counts, since startup, the writes committed, the writes
// refused by a conflict, and the requests turned away by a full FIFO
// (see Stats). They're kept in memory only, for GET /stats.
package writer
