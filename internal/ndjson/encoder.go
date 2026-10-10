package ndjson

import (
	"encoding/json"
	"io"
	"unicode/utf8"
)

// Writer writes NDJSON lines directly to dst, one at a time: a caller
// streaming a large result can write each line as it becomes available
// instead of holding the whole response in memory first.
type Writer struct {
	dst io.Writer

	// line is reused for every line, so a line costs one Write to dst and
	// no allocation once it has grown to the longest line.
	line []byte
}

// NewWriter returns a Writer that streams lines to dst as WriteLine,
// WriteValue, and WriteAppend are called.
func NewWriter(dst io.Writer) *Writer { return &Writer{dst: dst} }

// WriteLine writes one already-marshaled JSON value (no trailing newline)
// as its own NDJSON line. The caller is responsible for producing valid
// JSON: WriteLine does not re-validate it.
func (w *Writer) WriteLine(line []byte) error {
	return w.WriteAppend(func(dst []byte) []byte { return append(dst, line...) })
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

// WriteAppend writes, as its own line, the JSON value appendLine appends
// to dst (no trailing newline). It's for a hot path that builds its lines
// by hand, with AppendString, instead of through encoding/json's
// reflection. The caller is responsible for producing valid JSON.
func (w *Writer) WriteAppend(appendLine func(dst []byte) []byte) error {
	w.line = append(appendLine(w.line[:0]), '\n')
	_, err := w.dst.Write(w.line)
	return err
}

// AppendString appends s to dst as a JSON string, with the escapes of
// encoding/json.Marshal in the Go version go.mod names: '"' and '\\', the
// control characters, '<', '>' and '&', U+2028 and U+2029, and each byte
// of invalid UTF-8 as \ufffd. Its output always decodes to the same string
// as json.Marshal's. The bytes differ only where encoding/json's own output
// changed between Go versions: Go 1.27 writes invalid UTF-8 as a raw U+FFFD.
func AppendString(dst []byte, s string) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if b >= 0x20 && b != '"' && b != '\\' && b != '<' && b != '>' && b != '&' {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch b {
			case '\\', '"':
				dst = append(dst, '\\', b)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hex[b>>4], hex[b&0xF])
			}
			i++
			start = i
			continue
		}
		c, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case c == utf8.RuneError && size == 1:
			dst = append(dst, s[start:i]...)
			dst = append(dst, "\\ufffd"...)
		case c == '\u2028' || c == '\u2029':
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hex[c&0xF])
		default:
			i += size
			continue
		}
		i += size
		start = i
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}
