package ndjson

import (
	"encoding/json"
	"io"
)

// Writer writes NDJSON lines directly to dst, one at a time, with no
// buffering: a caller streaming a large result can write each line as it
// becomes available instead of holding the whole response in memory first.
type Writer struct {
	dst io.Writer
}

// NewWriter returns a Writer that streams lines to dst as WriteLine/
// WriteValue are called.
func NewWriter(dst io.Writer) *Writer { return &Writer{dst: dst} }

// WriteLine writes one already-marshaled JSON value (no trailing newline)
// as its own NDJSON line. The caller is responsible for producing valid
// JSON: WriteLine does not re-validate it.
func (w *Writer) WriteLine(line []byte) error {
	if _, err := w.dst.Write(line); err != nil {
		return err
	}
	_, err := w.dst.Write([]byte("\n"))
	return err
}

// WriteValue marshals v via encoding/json (so v's own MarshalJSON is used
// when v implements json.Marshaler) and writes the result as its own line.
func (w *Writer) WriteValue(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.WriteLine(b)
}
