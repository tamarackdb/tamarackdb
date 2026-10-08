package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// dumpReader reads a dump one event at a time, in the order of its lines.
// It reads with bufio.Reader.ReadBytes, not bufio.Scanner: a line has no
// size limit.
type dumpReader struct {
	r    *bufio.Reader
	line int // the number of the line read last
}

func newDumpReader(r io.Reader) *dumpReader {
	return &dumpReader{r: bufio.NewReader(r)}
}

// next returns the event of the next line, or io.EOF after the last one.
// The last line may end with a newline or not; an empty line anywhere else
// is an error. Every other error names its line.
func (d *dumpReader) next() (dcb.Event, error) {
	b, err := d.r.ReadBytes('\n')
	if errors.Is(err, io.EOF) && len(b) == 0 {
		return dcb.Event{}, io.EOF
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return dcb.Event{}, fmt.Errorf("read the dump: %w", err)
	}
	d.line++
	b = bytes.TrimSuffix(b, []byte("\n"))
	if len(bytes.TrimSpace(b)) == 0 {
		return dcb.Event{}, fmt.Errorf("line %d: empty line", d.line)
	}
	ev, err := parseLine(b)
	if err != nil {
		return dcb.Event{}, fmt.Errorf("line %d: %w", d.line, err)
	}
	return ev, nil
}

// dumpEvent is a line of the dump. Every field is a pointer or raw JSON,
// so that a missing field and a null one can be refused.
type dumpEvent struct {
	Sequence    *int64          `json:"sequence"`
	Time        *string         `json:"time"`
	Type        *string         `json:"type"`
	Identifiers json.RawMessage `json:"identifiers"`
	Metadata    json.RawMessage `json:"metadata"`
	Payload     *string         `json:"payload"`
}

// parseLine reads one line of the dump: an event in the shape QUERY
// /events returns, with exactly its six fields. Any JSON whitespace is
// accepted. A missing, null, unknown, or repeated field is refused, and so
// is a repeated key at any depth: decoding would keep its last value and
// drop the others without a word.
func parseLine(line []byte) (dcb.Event, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var w dumpEvent
	if err := dec.Decode(&w); err != nil {
		return dcb.Event{}, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return dcb.Event{}, errors.New("unexpected data after the event")
	}
	if err := checkNoDuplicateKeys(line); err != nil {
		return dcb.Event{}, err
	}

	switch {
	case w.Sequence == nil:
		return dcb.Event{}, missing("sequence")
	case w.Time == nil:
		return dcb.Event{}, missing("time")
	case w.Type == nil:
		return dcb.Event{}, missing("type")
	case w.Identifiers == nil || bytes.Equal(w.Identifiers, []byte("null")):
		return dcb.Event{}, missing("identifiers")
	case w.Metadata == nil || bytes.Equal(w.Metadata, []byte("null")):
		return dcb.Event{}, missing("metadata")
	case w.Payload == nil:
		return dcb.Event{}, missing("payload")
	}

	if *w.Sequence < 1 {
		return dcb.Event{}, fmt.Errorf("sequence is %d, it must be 1 or more", *w.Sequence)
	}
	t, err := parseTime(*w.Time)
	if err != nil {
		return dcb.Event{}, err
	}
	var ids dcb.IdentifierSet
	if err := decodeTags("identifiers", w.Identifiers, &ids); err != nil {
		return dcb.Event{}, err
	}
	var md dcb.MetadataSet
	if err := decodeTags("metadata", w.Metadata, &md); err != nil {
		return dcb.Event{}, err
	}

	data := dcb.EventData{Type: *w.Type, Identifiers: ids, Metadata: md, Payload: *w.Payload}
	if err := data.Validate(); err != nil {
		return dcb.Event{}, err
	}
	return dcb.Event{Sequence: *w.Sequence, Time: t, EventData: data}, nil
}

func missing(field string) error {
	return fmt.Errorf("%s is missing or null", field)
}

// parseTime accepts a time only in the exact form the server writes,
// dcb.TimeLayout from a UTC time: formatting it back must give the same
// text. time.Parse alone would also take an offset such as +00:00, and a
// database would then hold two forms of time.
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(dcb.TimeLayout, s)
	if err != nil || t.UTC().Format(dcb.TimeLayout) != s {
		return time.Time{}, fmt.Errorf("time is %q, it must have the form 2006-01-02T15:04:05.000000Z, in UTC", s)
	}
	return t.UTC(), nil
}

// decodeTags decodes identifiers or metadata, which must be an object.
func decodeTags(field string, raw json.RawMessage, v json.Unmarshaler) error {
	if raw[0] != '{' {
		return fmt.Errorf("%s must be an object", field)
	}
	if err := v.UnmarshalJSON(raw); err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	return nil
}

// checkNoDuplicateKeys refuses a key repeated within one object, at any
// depth of the JSON value b. b is already known to be valid JSON.
func checkNoDuplicateKeys(b []byte) error {
	// An object's frame holds its keys so far; an array's holds nil.
	type frame struct {
		keys    map[string]struct{}
		wantKey bool
	}
	var stack []*frame
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if n := len(stack); n > 0 && stack[n-1].keys != nil && stack[n-1].wantKey {
			if key, ok := tok.(string); ok {
				if _, dup := stack[n-1].keys[key]; dup {
					return fmt.Errorf("key %q appears twice in the same object", key)
				}
				stack[n-1].keys[key] = struct{}{}
				stack[n-1].wantKey = false
				continue
			}
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &frame{keys: map[string]struct{}{}, wantKey: true})
			continue
		case json.Delim('['):
			stack = append(stack, &frame{})
			continue
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
		}
		// A value just ended: in an object, a key comes next.
		if n := len(stack); n > 0 && stack[n-1].keys != nil {
			stack[n-1].wantKey = true
		}
	}
}
