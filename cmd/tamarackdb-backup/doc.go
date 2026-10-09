// Command tamarackdb-backup copies the new events of a TamarackDB
// instance into a local SQLite file, tamarackdb-backup.sqlite, then
// exits. It's meant to run from cron or a systemd timer: there is no
// HTTP server, no loop, and no retry. A run that fails partway is resumed
// by the next one, from the last page imported.
package main
