// Command tamarackdb-init creates a new TamarackDB database: the data
// directory if it's missing, and a database file in it with the schema
// and a new store ID, ready for tamarackdb-server. It refuses to overwrite
// an existing database.
//
// # Import
//
// With -import, it also writes the events of an NDJSON dump into the new
// database, each with the sequence and the time the dump gives it. This
// is how an application brings its history over from another event store.
//
// The import is all or nothing. It writes into a temporary file next to
// the database file, named tamarackdb.sqlite.import-*, and gives that file
// its final name only once every event is in. A rewrite MUST keep these
// rules:
//
//   - The final name MUST be given with link(2), not rename(2): link fails
//     if the database file exists, even if another process created it
//     during the import, where rename would replace it.
//   - The store MUST be closed, and its WAL file gone, before the link.
//     SQLite finds the WAL by the database file's name: events still in it
//     would be lost under the new name. Closing the last connection copies
//     the WAL into the database file and deletes it; the import checks it.
//   - The temporary file MUST be a new one on every run (os.CreateTemp). A
//     process killed mid-import leaves its file behind; a later run must
//     never reopen it.
//
// Events are committed importBatch at a time, so memory stays bounded
// whatever the size of the dump. The temporary file already makes the
// whole import all or nothing.
//
// The dump is read strictly (see parseLine): a line that isn't exactly an
// event stops the import, since skipping it would lose an event silently.
package main
