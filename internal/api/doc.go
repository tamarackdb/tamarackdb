// Package api is TamarackDB's HTTP layer: routing, authentication, request
// validation, logging, and the error envelope, around packages writer, tx,
// and store. It knows nothing of package config: callers pass an Options
// already resolved.
//
// # Request bodies
//
// A handler reads and checks the whole body before a request joins the
// FIFO. A client sending its body slowly would otherwise hold the turn,
// and every request behind it, for as long as it likes.
//
// # A stalled read
//
// A streamed read (QUERY /events, QUERY /tx/{txId}/events) holds its read
// connection, and pins its SQLite snapshot, until it's fully sent. Each
// line gets 30 seconds to go out, renewed on every line. A read that
// keeps moving is never cut, however slow the client. A client that stops
// reading loses its connection after 30 seconds, and the read connection
// goes back to the pool. Without this limit, readPoolSize stalled clients
// would block every read, /health included, and keep the WAL from being
// checkpointed.
//
// # Transactions
//
// Any error on a transaction ends it, including one this package finds
// before the transaction is reached, such as a malformed body: such a
// request ends it through tx.Registry.Reject.
package api
