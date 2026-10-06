// Command tamarackdb-server runs the TamarackDB HTTP server. It loads the
// configuration, opens the SQLite store, and serves the HTTP API until a
// shutdown signal or a fatal storage error.
//
// # Startup
//
//  1. It prints its version and its resolved configuration to stdout.
//  2. It refuses to start if the database file doesn't exist: only
//     tamarackdb-init creates one. A wrong data directory, or a disk that
//     isn't mounted, would otherwise get a new, empty store, and the
//     history would be split in two.
//  3. It opens the store: it takes the lock on the database file, checks
//     the schema version, and reads the highest Sequence Position and the
//     store ID into memory (see package store).
//  4. It runs PRAGMA optimize, so every start refreshes the query
//     planner's statistics. A failure is logged, and the server starts
//     anyway: old statistics slow queries down without making them wrong.
//  5. It serves requests. It runs nothing on a timer of its own: PRAGMA
//     optimize runs again on POST /optimize, which an operator's timer
//     calls.
//
// # What lives only in memory
//
// The FIFO's state, the open transactions, and the counters of GET /stats
// live only in memory, for the life of the process. Nothing of them is
// saved, and nothing is rebuilt: a new process starts with an empty FIFO
// and no transaction. That's right: every client waiting before a crash
// lost its connection too, and a client whose transaction is lost gets
// 404 TransactionNotFound and runs its command again. The Sequence
// Position counter and the store ID are read back from the database at
// startup, since they have to match what's on disk.
//
// # Shutdown
//
// On SIGINT or SIGTERM, the server shuts down in order:
//
//  1. The HTTP server stops taking new connections (http.Server.Shutdown,
//     capped at 10 seconds).
//  2. At the same moment, the FIFO closes: the requests still waiting get
//     503 ShuttingDown, and so does any later one. A request already
//     holding the turn finishes. A request waiting in the FIFO only ends
//     once it gets its turn, so the HTTP server would otherwise wait for
//     it.
//  3. The HTTP server finishes the requests still in flight.
//  4. The store closes last, releasing its connections and the .lock
//     file. The open transactions are dropped with the process: the
//     transaction registry has no goroutine to stop.
//
// # Connection timeouts
//
// A connection that doesn't finish sending its request headers within 10
// seconds is closed, and so is a keep-alive connection with no request
// for 2 minutes. A streamed read has its own limit for each line (see
// package api).
//
// # Failures
//
// A panic in a request handler is caught by the HTTP server, without
// crashing the process. The handler's deferred rollback and the release
// of its turn still run during the unwind, so no turn stays taken, and a
// write's reserved Sequence Positions are given back.
//
// A crash during a write leaves an uncommitted WAL transaction, discarded
// the next time a connection opens. Neither the write's events nor its
// projections ever become visible, in whole or in part.
//
// A SQLite error that suggests the file itself may be damaged (an I/O
// error, detected corruption, a failure to open the file) is fatal: the
// server logs it and shuts down in the same order as on a signal. A
// temporary error, such as a busy lock during a checkpoint, fails only its
// request. Any error while opening the store is fatal. There is no
// recovery logic for each error: a clean restart is cheap and safe, given
// what lives only in memory, and /health and a process supervisor are set
// up to catch it.
package main
