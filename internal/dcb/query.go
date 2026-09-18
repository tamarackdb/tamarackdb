package dcb

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// QueryItem is one item of a Query, combined with the others by OR. A
// zero-value QueryItem ({}) poses no constraint at all, so it isn't a
// meaningful item to send: Validate rejects it. To match every event, use
// Query.all() (JSON "*") instead of an item with nothing set.
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
	if i.Types == nil && i.Identifiers == nil && i.Metadata == nil {
		return &ValidationError{Err: ErrEmptyQueryItem, Message: "a QueryItem must specify at least one of types, identifiers, or metadata"}
	}
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
//
// Items that are exact duplicates of one another (same types, same
// identifiers, same metadata, regardless of order) are silently
// collapsed to one: they add nothing to the OR beyond a redundant SQL
// clause. This applies wherever a Query is built, including a
// condition.failIfEventsMatch on /append.
type Query struct {
	all   bool
	items []QueryItem
}

func QueryAll() Query                  { return Query{all: true} }
func NewQuery(items []QueryItem) Query { return Query{items: dedupeQueryItems(items)} }

// dedupeQueryItems drops exact duplicate QueryItem, keeping the first
// occurrence. It does not detect one item logically subsuming another,
// only identical items.
func dedupeQueryItems(items []QueryItem) []QueryItem {
	if len(items) < 2 {
		return items
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]QueryItem, 0, len(items))
	for _, item := range items {
		key := queryItemKey(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

// queryItemKey returns a string uniquely identifying a QueryItem's
// content, independent of the order of its Types/Identifiers/Metadata.
// %q quotes each name/value so a boundary between fields can never be
// confused with a character inside one.
func queryItemKey(item QueryItem) string {
	types := append([]string(nil), item.Types...)
	sort.Strings(types)

	ids := append([]Identifier(nil), item.Identifiers...)
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].Name != ids[j].Name {
			return ids[i].Name < ids[j].Name
		}
		return ids[i].Value < ids[j].Value
	})

	meta := append([]Metadata(nil), item.Metadata...)
	sort.Slice(meta, func(i, j int) bool {
		if meta[i].Name != meta[j].Name {
			return meta[i].Name < meta[j].Name
		}
		return meta[i].Value < meta[j].Value
	})

	var b strings.Builder
	for _, t := range types {
		fmt.Fprintf(&b, "t:%q\n", t)
	}
	for _, id := range ids {
		fmt.Fprintf(&b, "i:%q=%q\n", id.Name, id.Value)
	}
	for _, m := range meta {
		fmt.Fprintf(&b, "m:%q=%q\n", m.Name, m.Value)
	}
	return b.String()
}

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
	*q = Query{items: dedupeQueryItems(items)}
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
// for safe retries after a startup or crash). Defined in dcb rather than
// internal/api because both internal/api (decoding the request) and
// internal/store (checking it against the database) need the same shape.
// internal/queue, which admits writers before any condition is checked,
// never needs to know this type at all.
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
	ErrEmptyQueryItem        = errors.New("empty QueryItem")
	ErrEmptyQueryItemArray   = errors.New("empty QueryItem array")
	ErrNegativeAfterSequence = errors.New("negative afterSequence")
)
