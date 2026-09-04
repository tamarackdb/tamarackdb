// Package ndjson writes NDJSON responses in the header-line-first framing
// TamarackDB's HTTP API needs: a caller-supplied header value as the first
// line, followed by N already-marshaled body lines, one JSON value per
// line. It has no knowledge of dcb.Event, HTTP, gatekeeper, or store —
// callers supply arbitrary JSON-marshalable values, so this package is
// reusable and testable entirely on its own.
package ndjson

import (
	"bytes"
	"encoding/json"
	"io"
)

// Writer accumulates NDJSON body lines into an internal buffer, then
// Flush writes the header line followed by the buffered body in one shot
// to the destination io.Writer.
//
// Buffering the body before the header line is unavoidable, not an
// oversight: TamarackDB's wire format requires a header carrying
// e.g. hasMore as the very first line, but that value is often only
// knowable once a result page has been fully iterated. This is a
// deliberate, bounded compromise — bounded by whatever the caller's own
// page-size limits are — not the unbounded, whole-page-as-a-slice
// buffering NDJSON is specifically meant to avoid: what's kept here is
// only the small amount of already-serialized bytes needed to make
// header-first framing possible at all, never a slice of decoded values.
//
// A useful side effect: because nothing is written to the real
// destination until Flush, a caller that hits a mid-page error can still
// discard everything buffered so far and write a clean error response
// instead of a broken, half-written stream.
type Writer struct {
	buf bytes.Buffer
}

// NewWriter returns an empty Writer ready to accept lines via WriteLine
// or WriteValue.
func NewWriter() *Writer { return &Writer{} }

// WriteLine appends one already-marshaled JSON value (no trailing
// newline) as its own NDJSON line. The caller is responsible for
// producing valid JSON — WriteLine does not re-validate it.
func (w *Writer) WriteLine(line []byte) error {
	if _, err := w.buf.Write(line); err != nil {
		return err
	}
	return w.buf.WriteByte('\n')
}

// WriteValue marshals v via encoding/json (so v's own MarshalJSON is used
// when v implements json.Marshaler) and appends the result as its own
// line.
func (w *Writer) WriteValue(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.WriteLine(b)
}

// Flush marshals header via encoding/json and writes it as the first
// line, followed by every buffered body line, to dst. Call this exactly
// once, after every WriteLine/WriteValue call for the page is done and
// the header's true content is known.
func (w *Writer) Flush(dst io.Writer, header any) error {
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return err
	}
	if _, err := dst.Write(headerJSON); err != nil {
		return err
	}
	if _, err := dst.Write([]byte("\n")); err != nil {
		return err
	}
	_, err = w.buf.WriteTo(dst)
	return err
}

// Len reports the number of bytes currently buffered (body only, not the
// not-yet-written header).
func (w *Writer) Len() int { return w.buf.Len() }
