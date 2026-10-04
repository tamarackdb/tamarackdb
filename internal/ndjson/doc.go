// Package ndjson writes NDJSON: one JSON value per line, written to the
// destination as each value comes in, with no buffering. It knows nothing
// of events, HTTP, or what a line means: callers pass any value JSON can
// encode.
package ndjson
