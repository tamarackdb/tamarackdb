package dcb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Fixed DCB-domain constants, not configuration.
const (
	MaxIdentifiers     = 20  // max identifiers per event
	MaxMetadata        = 20  // max metadata entries per event
	MaxEventsPerAppend = 100 // max events in a single POST /append call
)

// Identifier is a business-identifier {name, value} pair, the primary
// DCB filtering axis. It is the exact JSON shape used inside a QueryItem:
// {"name": "userId", "value": "123"}.
type Identifier struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Metadata is a {name, value} pair for everything that isn't a business
// identifier (authorship, correlation, tenancy, ...). Structurally
// identical to Identifier but a distinct Go type, so the two namespaces
// can never be mixed by mistake at a call site or in a matching check.
type Metadata struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// IdentifierSet is the ordered collection of an event's Identifiers,
// serialized as the compact API shape: {"courseId": ["foo","bar"], "otherId": "baz"}.
type IdentifierSet []Identifier

// MetadataSet is the Metadata equivalent of IdentifierSet.
type MetadataSet []Metadata

func (s IdentifierSet) MarshalJSON() ([]byte, error) {
	return marshalCompact(s, func(i Identifier) (string, string) { return i.Name, i.Value })
}

func (s *IdentifierSet) UnmarshalJSON(data []byte) error {
	items, err := unmarshalCompact(data, func(name, value string) Identifier {
		return Identifier{Name: name, Value: value}
	})
	if err != nil {
		return err
	}
	*s = items
	return nil
}

func (s MetadataSet) MarshalJSON() ([]byte, error) {
	return marshalCompact(s, func(m Metadata) (string, string) { return m.Name, m.Value })
}

func (s *MetadataSet) UnmarshalJSON(data []byte) error {
	items, err := unmarshalCompact(data, func(name, value string) Metadata {
		return Metadata{Name: name, Value: value}
	})
	if err != nil {
		return err
	}
	*s = items
	return nil
}

// marshalCompact renders a slice of {name,value} pairs grouped by name:
// a single value renders as a bare string, multiple values under the
// same name render as an array. Keys are sorted for deterministic
// output. An empty/nil set renders as {}.
func marshalCompact[T any](items []T, split func(T) (name, value string)) ([]byte, error) {
	groups := make(map[string][]string, len(items))
	order := make([]string, 0, len(items))
	for _, it := range items {
		name, value := split(it)
		if _, ok := groups[name]; !ok {
			order = append(order, name)
		}
		groups[name] = append(groups[name], value)
	}
	sort.Strings(order)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, name := range order {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		buf.Write(keyJSON)
		buf.WriteByte(':')
		vals := groups[name]
		var valJSON []byte
		if len(vals) == 1 {
			valJSON, err = json.Marshal(vals[0])
		} else {
			valJSON, err = json.Marshal(vals)
		}
		if err != nil {
			return nil, err
		}
		buf.Write(valJSON)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// unmarshalCompact parses the compact {name: string|[]string} shape.
// This is a structural/shape check only (wrong JSON type, empty array
// value); it returns a plain error, not a *ValidationError. Domain
// rules (duplicates, count caps) are EventData.Validate's job.
func unmarshalCompact[T any](data []byte, build func(name, value string) T) ([]T, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("dcb: expected an object of name to string or array of strings: %w", err)
	}
	var out []T
	for name, rawVal := range raw {
		var single string
		if err := json.Unmarshal(rawVal, &single); err == nil {
			out = append(out, build(name, single))
			continue
		}
		var multi []string
		if err := json.Unmarshal(rawVal, &multi); err != nil {
			return nil, fmt.Errorf("dcb: value for %q must be a string or an array of strings", name)
		}
		if len(multi) == 0 {
			return nil, fmt.Errorf("dcb: value for %q must not be an empty array", name)
		}
		for _, v := range multi {
			out = append(out, build(name, v))
		}
	}
	return out, nil
}

// EventData is everything known about an event before it is appended:
// what a client submits to POST /append, and all the matching predicate
// (see match.go) ever needs. Sequence and Time play no part in matching.
type EventData struct {
	Type        string        `json:"type"`
	Identifiers IdentifierSet `json:"identifiers"`
	Metadata    MetadataSet   `json:"metadata"`
	Payload     string        `json:"payload"`
}

// Size returns the combined UTF-8 byte length of Type, the name/value
// text of every Identifier and Metadata entry, and Payload, the exact
// quantity the 64 KiB event size limit is measured against. The limit
// itself is configuration, not enforced here.
func (e EventData) Size() int {
	n := len(e.Type) + len(e.Payload)
	for _, id := range e.Identifiers {
		n += len(id.Name) + len(id.Value)
	}
	for _, md := range e.Metadata {
		n += len(md.Name) + len(md.Value)
	}
	return n
}

// Validate checks the domain rules that apply to a single event
// regardless of the rest of the append request: a non-empty Type, at
// most MaxIdentifiers/MaxMetadata entries, and no duplicate {name,value}
// pair within either set.
func (e EventData) Validate() error {
	if e.Type == "" {
		return &ValidationError{Err: ErrMissingType, Message: "event is missing its type"}
	}
	if len(e.Identifiers) > MaxIdentifiers {
		return &ValidationError{Err: ErrTooManyIdentifiers, Message: fmt.Sprintf(
			"event carries %d identifiers, more than the maximum of %d", len(e.Identifiers), MaxIdentifiers)}
	}
	if duplicateExists(e.Identifiers) {
		return &ValidationError{Err: ErrDuplicateIdentifier, Message: "event carries a duplicate identifier"}
	}
	if len(e.Metadata) > MaxMetadata {
		return &ValidationError{Err: ErrTooManyMetadata, Message: fmt.Sprintf(
			"event carries %d metadata entries, more than the maximum of %d", len(e.Metadata), MaxMetadata)}
	}
	if duplicateExists(e.Metadata) {
		return &ValidationError{Err: ErrDuplicateMetadata, Message: "event carries a duplicate metadata entry"}
	}
	return nil
}

func duplicateExists[T comparable](items []T) bool {
	seen := make(map[T]struct{}, len(items))
	for _, it := range items {
		if _, ok := seen[it]; ok {
			return true
		}
		seen[it] = struct{}{}
	}
	return false
}

// Event is a fully materialized, persisted event: EventData plus the
// Sequence Position and Time assigned by the store at append time.
type Event struct {
	Sequence int64
	Time     time.Time
	EventData
}

// timeLayout renders/parses `time` in ATOM format (RFC 3339) with fixed
// microsecond precision, always UTC, matching the exact wire format used
// in responses (e.g. "2026-09-01T14:23:05.123456Z").
// Go's default time.Time JSON marshaling (RFC3339Nano) trims trailing
// zero digits, so it cannot be used as-is here.
const timeLayout = "2006-01-02T15:04:05.000000Z07:00"

func (e Event) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Sequence    int64         `json:"sequence"`
		Time        string        `json:"time"`
		Type        string        `json:"type"`
		Identifiers IdentifierSet `json:"identifiers"`
		Metadata    MetadataSet   `json:"metadata"`
		Payload     string        `json:"payload"`
	}{
		Sequence:    e.Sequence,
		Time:        e.Time.UTC().Format(timeLayout),
		Type:        e.Type,
		Identifiers: e.Identifiers,
		Metadata:    e.Metadata,
		Payload:     e.Payload,
	})
}

func (e *Event) UnmarshalJSON(data []byte) error {
	var wire struct {
		Sequence    int64         `json:"sequence"`
		Time        string        `json:"time"`
		Type        string        `json:"type"`
		Identifiers IdentifierSet `json:"identifiers"`
		Metadata    MetadataSet   `json:"metadata"`
		Payload     string        `json:"payload"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339Nano, wire.Time)
	if err != nil {
		return fmt.Errorf("dcb: invalid time %q: %w", wire.Time, err)
	}
	e.Sequence, e.Time = wire.Sequence, t.UTC()
	e.EventData = EventData{Type: wire.Type, Identifiers: wire.Identifiers, Metadata: wire.Metadata, Payload: wire.Payload}
	return nil
}

// ValidationError describes a domain-rule violation from this package.
// internal/api maps every *ValidationError, regardless of which
// sentinel it wraps, to a 400 {"error":"InvalidRequest","message": ...}
// response; Err lets callers (and tests) branch on the specific rule
// with errors.Is without parsing Message.
type ValidationError struct {
	Err     error
	Message string
}

func (e *ValidationError) Error() string { return e.Message }
func (e *ValidationError) Unwrap() error { return e.Err }

var (
	ErrMissingType         = errors.New("event is missing its type")
	ErrTooManyIdentifiers  = errors.New("too many identifiers")
	ErrTooManyMetadata     = errors.New("too many metadata entries")
	ErrDuplicateIdentifier = errors.New("duplicate identifier")
	ErrDuplicateMetadata   = errors.New("duplicate metadata entry")
)
