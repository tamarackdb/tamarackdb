// Command tamarackdb-demo fills a TamarackDB data directory with a large
// set of made-up data, to try reads and storage at scale. Each event has
// a random type, one or two identifiers, a tenant metadata entry, and
// filler text as payload. Each projection has a random type, a numeric
// id, and longer filler text. It writes straight to the store, not through
// the HTTP server.
package main
