// Package config loads TamarackDB's startup configuration: socket path or
// bind address and port, auth token, data directory, and the pagination,
// size, queue, and transaction limits.
//
// Values come from a TOML file when present. Any field it leaves out, or
// every field if there's no file, is filled in from the matching
// TAMARACKDB_* environment variable, then from a built-in default. The
// file wins over the environment when both set the same field.
//
// The file may also hold a [backup] section for tamarackdb-backup (see
// BackupConfig). Load reads only [server], so the two binaries can share
// one file or use separate ones.
package config
