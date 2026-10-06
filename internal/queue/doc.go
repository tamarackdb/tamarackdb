// Package queue is TamarackDB's FIFO: it hands out the single turn on the
// write connection, strictly in the order requests arrive. It knows
// nothing of what a turn does with it. Package writer runs the writes on
// top of it.
//
// A request is in one of two states: active (at most one at a time, the
// only one allowed to touch the write connection) or queued (waiting for
// its turn). Reads never wait in the FIFO.
//
// # A request's path
//
//  1. The HTTP handler reads and checks the request body, outside the
//     FIFO.
//  2. It joins the line. If maxQueued requests already wait, Join returns
//     ErrFull at once, and the request never joins.
//  3. Otherwise it takes the next ticket, and waits until every request
//     ahead of it is done.
//  4. At the head of the line, it holds the turn until it calls Turn.Done.
//
// A request that joined stays in the line until its turn, even if its
// client disconnects: the request then runs, and the client treats it as
// a lost response. The line is never changed in the middle, so no
// departure can race a turn handed out at the same moment. The price: an
// abandoned request keeps its place, and counts toward maxQueued, until
// its turn. TamarackDB isn't built for many concurrent writes, so that
// place is rarely missed, and a burst of them stays bounded by maxQueued.
//
// # Rules
//
// The FIFO serves requests strictly in arrival order, with no priority
// between them. Letting a bulk delete of projections go first would gain
// nothing, since the writes waiting ahead of it must be written anyway.
// It would also do harm: a waiting write that changes a projection the
// bulk delete removed would then be refused whole, its events included.
//
// The FIFO is bounded: configuration always sets maxQueued. A FIFO with no
// bound would let a burst, or a broken client, pile up any number of
// blocked HTTP connections. (New accepts 0 for no bound, for tests only.)
//
// A request waits as long as it takes, with no timeout. Each request ahead
// holds the turn only for its own work, usually a few milliseconds.
//
// Close turns away every request still waiting, and every later one, with
// ErrClosed. A request holding the turn finishes.
package queue
