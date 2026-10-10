// Package ndjson writes NDJSON: one JSON value per line, written to the
// destination as each value comes in, in one Write per line. It knows
// nothing of events, HTTP, or what a line means: callers pass any value
// JSON can encode, or build a line by hand with AppendString, for a hot
// path that can't afford encoding/json's reflection.
package ndjson
