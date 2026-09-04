package dcb

import (
	"encoding/json"
	"errors"
	"fmt"
)

// QueryItem is one item of a Query, combined with the others by OR.
// A zero-value QueryItem ({}) is valid and matches every event.
//
// nil vs. a non-nil empty slice is meaningful here: nil means the axis
// is unconstrained (key omitted in JSON); a non-nil empty slice means
// the client sent `[]`, which Validate rejects.
type QueryItem struct {
	Types       []string     `json:"types,omitempty"`
	Identifiers []Identifier `json:"identifiers,omitempty"`
	Metadata    []Metadata   `json:"metadata,omitempty"`
}

func (i QueryItem) Validate() error {
	if i.Types != nil && len(i.Types) == 0 {
		return &ValidationError{Err: ErrEmptyQueryItemArray, Message: "types must be non-empty when present"}
	}
	if i.Identifiers != nil && len(i.Identifiers) == 0 {
		return &ValidationError{Err: ErrEmptyQueryItemArray, Message: "identifiers must be non-empty when present"}
	}
	if i.Metadata != nil && len(i.Metadata) == 0 {
		return &ValidationError{Err: ErrEmptyQueryItemArray, Message: "metadata must be non-empty when present"}
	}
	return nil
}

// Query is a DCB Query: either Query.all() (every event matches, JSON
// "*") or a concrete, non-empty, OR-combined list of QueryItem. Built
// only through QueryAll or NewQuery so the "all and items both set" and
// "neither set" states are unrepresentable; the zero value is invalid
// by design (see Validate) rather than silently meaning "all", since a
// silent default to Query.all() would be a dangerous default for /read.
type Query struct {
	all   bool
	items []QueryItem
}

func QueryAll() Query                  { return Query{all: true} }
func NewQuery(items []QueryItem) Query { return Query{items: items} }

func (q Query) All() bool          { return q.all }
func (q Query) Items() []QueryItem { return q.items } // nil when All()

func (q Query) MarshalJSON() ([]byte, error) {
	if q.all {
		return json.Marshal("*")
	}
	return json.Marshal(q.items)
}

func (q *Query) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if s != "*" {
			return fmt.Errorf("dcb: query string must be \"*\", got %q", s)
		}
		*q = Query{all: true}
		return nil
	}
	var items []QueryItem
	if err := json.Unmarshal(data, &items); err != nil {
		return fmt.Errorf("dcb: query must be an array of QueryItem or \"*\": %w", err)
	}
	*q = Query{items: items}
	return nil
}

func (q Query) Validate() error {
	if q.all {
		return nil
	}
	if len(q.items) == 0 {
		return &ValidationError{Err: ErrEmptyQuery, Message: "query must be a non-empty array of QueryItem, or \"*\""}
	}
	for _, item := range q.items {
		if err := item.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// AppendCondition mirrors condition in POST /append: FailIfEventsMatch
// follows the same grammar as a read Query, and is itself optional
// within a condition (an afterSequence-only condition is valid, used
// for safe retries after a startup or crash). Defined
// in dcb rather than internal/api because both internal/api (decoding
// the request) and internal/gatekeeper (holding it per reservation)
// need the same shape.
type AppendCondition struct {
	FailIfEventsMatch *Query `json:"failIfEventsMatch,omitempty"`
	AfterSequence     *int64 `json:"afterSequence,omitempty"`
}

func (c AppendCondition) Validate() error {
	if c.FailIfEventsMatch != nil {
		if err := c.FailIfEventsMatch.Validate(); err != nil {
			return err
		}
	}
	if c.AfterSequence != nil && *c.AfterSequence < 0 {
		return &ValidationError{Err: ErrNegativeAfterSequence, Message: "afterSequence must be a non-negative integer"}
	}
	return nil
}

var (
	ErrEmptyQuery            = errors.New("empty query")
	ErrEmptyQueryItemArray   = errors.New("empty QueryItem array")
	ErrNegativeAfterSequence = errors.New("negative afterSequence")
)
