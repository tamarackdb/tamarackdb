// Command tamarackdb-migrate brings a TamarackDB database to the schema
// version of this release, then exits. It runs while the server is
// stopped: it takes the lock the server takes, and refuses to run while
// another process holds it.
//
// It carries a single migration, from the previous major version to this
// one (see store.Migrate). A database already at this version is left
// untouched. It makes no backup, and never goes back to an older version.
package main
