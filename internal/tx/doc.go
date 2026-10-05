// Package tx keeps TamarackDB's transactions. A transaction lives in
// memory, private to the client that began it, and touches SQLite only to
// read, and once more at commit, in its turn in the FIFO (see package
// writer). Nothing is locked while it lives: a conflict with another
// write is found at commit, by the Append Conditions its reads opened and
// by the versions of the projections it read.
//
// # One decision, one read, one write
//
// A read of events opens a condition, and the write of events that
// follows closes it, with events or with none. Projections are read and
// written only while no condition is open. A call that breaks a rule ends
// the transaction, like any other error: the server doesn't accommodate a
// client library with a bug.
//
// Since nothing comes between a read and its write, no pending event can
// be added after a read and before its decision. An event added later,
// that matches a condition already closed, isn't an error: each decision
// has its place in the order of the pending events, and what comes after
// it lands after it in the log.
//
// An empty write still closes a condition, and the condition is still
// checked at commit. A decision to do nothing then stays protected: if
// what it read changes before the commit, the commit is refused.
//
// # What a transaction holds
//
// For each transaction, the Registry keeps the store ID it began on, the
// open condition if any, the closed conditions in order, the pending
// events, and the state of every projection it read. Nothing of it is
// written before the commit.
//
// # Its end
//
// A transaction ends at its commit, whatever the outcome, at its first
// error, when abandoned, or after Config.IdleTimeout without a call. The
// Registry then keeps nothing of it, so it can't tell an unknown
// transaction from one that ended: both get ErrNotFound. When the server
// stops, every open transaction is lost, and nothing is rebuilt: the
// client gets ErrNotFound and runs its command again, as after a
// conflict.
//
// # The pause
//
// Events enter the log only through a transaction, so a pause only has
// to stop transactions from beginning: Begin returns ErrPaused while one
// is requested or in place. Everything else goes on, writes of
// projections outside a transaction included.
//
// Pause first refuses Begin, then waits for the open transactions to end,
// outside the FIFO, since their commits must go through it. A commit
// counts until its write ends, not only until it leaves the Registry (see
// Commit). Then Pause takes its turn and records the pause in the store,
// which keeps it across a restart. A transaction that never ends holds
// the pause back: the wait has no limit, and Resume withdraws it.
//
// Resume and Reset take their turn in the FIFO too, so they can't cross
// the turn in which Pause records the pause: whichever comes first, the
// FIFO's order settles it. A Pause whose request they withdraw gets
// ErrPauseCancelled. At shutdown, CancelPause answers a Pause still
// waiting for transactions, which closing the Writer doesn't wake.
package tx
