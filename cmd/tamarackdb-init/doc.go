// Command tamarackdb-init creates a new TamarackDB database: the data
// directory if it's missing, and a database file in it with the schema
// and a new store ID, ready for tamarackdb-server. It refuses to overwrite
// an existing database.
package main
