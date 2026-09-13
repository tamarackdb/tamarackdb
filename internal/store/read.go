package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// ReadFilter holds Read's parameters, already resolved/validated by the
// caller: default/max limit applied, time strings parsed. Store adds no
// defaults of its own except AfterSequence's implicit 0: omitting it reads
// from the beginning of the store.
type ReadFilter struct {
	Query         dcb.Query
	AfterSequence *int64     // nil = from the beginning; sequence > *AfterSequence
	TimeFrom      *time.Time // nil = unconstrained; inclusive (>=)
	TimeBefore    *time.Time // nil = unconstrained; exclusive (<)
	Limit         int        // must be >= 1; Read fetches Limit+1 rows
}

// Read runs a paginated DCB read. The returned *EventIterator must be
// closed by the caller (directly, or by exhausting Next).
func (s *Store) Read(ctx context.Context, f ReadFilter) (*EventIterator, error) {
	if f.Limit < 1 {
		return nil, fmt.Errorf("store: Read: Limit must be >= 1, got %d", f.Limit)
	}
	sqlStr, args := buildReadSQL(f)
	rows, err := s.readDB.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, wrapf("Read", err)
	}
	return &EventIterator{rows: rows, limit: f.Limit}, nil
}

func buildReadSQL(f ReadFilter) (string, []any) {
	var b strings.Builder
	args := make([]any, 0, 6)
	b.WriteString(`SELECT events.sequence, events.time, events.type, events.payload,
  COALESCE((SELECT json_group_array(json_object('name', identifiers.name, 'value', identifiers.value))
            FROM identifiers WHERE identifiers.event_sequence = events.sequence), '[]'),
  COALESCE((SELECT json_group_array(json_object('name', metadata.name, 'value', metadata.value))
            FROM metadata WHERE metadata.event_sequence = events.sequence), '[]')
FROM events
WHERE events.sequence > ?`)

	after := int64(0)
	if f.AfterSequence != nil {
		after = *f.AfterSequence
	}
	args = append(args, after)

	if f.TimeFrom != nil {
		b.WriteString(" AND events.time >= ?")
		args = append(args, f.TimeFrom.UTC().Format(timeLayout))
	}
	if f.TimeBefore != nil {
		b.WriteString(" AND events.time < ?")
		args = append(args, f.TimeBefore.UTC().Format(timeLayout))
	}
	if where, whereArgs := queryToSQL(f.Query); where != "" {
		b.WriteString(" AND ")
		b.WriteString(where)
		args = append(args, whereArgs...)
	}
	b.WriteString(" ORDER BY events.sequence ASC LIMIT ?")
	args = append(args, f.Limit+1)
	return b.String(), args
}

type jsonPair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func scanEvent(rows *sql.Rows) (dcb.Event, error) {
	var seq int64
	var timeText, typ, payload, idsJSON, mdJSON string
	if err := rows.Scan(&seq, &timeText, &typ, &payload, &idsJSON, &mdJSON); err != nil {
		return dcb.Event{}, err
	}
	t, err := time.Parse(timeLayout, timeText)
	if err != nil {
		return dcb.Event{}, fmt.Errorf("store: corrupt time %q for event %d: %w", timeText, seq, err)
	}
	var ids, mds []jsonPair
	if err := json.Unmarshal([]byte(idsJSON), &ids); err != nil {
		return dcb.Event{}, fmt.Errorf("store: corrupt identifiers for event %d: %w", seq, err)
	}
	if err := json.Unmarshal([]byte(mdJSON), &mds); err != nil {
		return dcb.Event{}, fmt.Errorf("store: corrupt metadata for event %d: %w", seq, err)
	}
	identifiers := make(dcb.IdentifierSet, len(ids))
	for i, p := range ids {
		identifiers[i] = dcb.Identifier{Name: p.Name, Value: p.Value}
	}
	metadata := make(dcb.MetadataSet, len(mds))
	for i, p := range mds {
		metadata[i] = dcb.Metadata{Name: p.Name, Value: p.Value}
	}
	return dcb.Event{
		Sequence:  seq,
		Time:      t,
		EventData: dcb.EventData{Type: typ, Identifiers: identifiers, Metadata: metadata, Payload: payload},
	}, nil
}

// EventIterator streams Read's result page one event at a time. Next
// returns false once Limit events have been returned or the underlying
// query is exhausted; HasMore is only meaningful after that point (i.e.
// once Next has returned false) — before then it is always false.
//
// internal/api's future NDJSON handler drives this with
// Next()/Event()/Err(), marshaling and writing each event as it comes out
// of SQLite; the single underlying query keeps the read transaction
// short-lived, to support live projection rebuilds, never held open
// across pages.
type EventIterator struct {
	rows    *sql.Rows
	limit   int
	n       int
	hasMore bool
	err     error
	cur     dcb.Event
	closed  bool
}

func (it *EventIterator) Next() bool {
	if it.err != nil || it.closed {
		return false
	}
	if it.n >= it.limit {
		// The Limit+1-th row exists iff the underlying SQL (LIMIT
		// Limit+1) has one more row buffered: peek it without decoding
		// or exposing it.
		if it.rows.Next() {
			it.hasMore = true
		} else if err := it.rows.Err(); err != nil {
			it.err = err
		}
		it.Close()
		return false
	}
	if !it.rows.Next() {
		if err := it.rows.Err(); err != nil {
			it.err = err
		}
		it.Close()
		return false
	}
	ev, err := scanEvent(it.rows)
	if err != nil {
		it.err = err
		it.Close()
		return false
	}
	it.cur = ev
	it.n++
	return true
}

func (it *EventIterator) Event() dcb.Event { return it.cur }
func (it *EventIterator) Err() error       { return it.err }
func (it *EventIterator) HasMore() bool    { return it.hasMore }

// Close releases the underlying *sql.Rows/connection. Safe to call more
// than once, and safe to call before exhausting Next (e.g. a client
// disconnects mid-stream); HasMore then simply reflects whatever was known
// at that point.
func (it *EventIterator) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	return it.rows.Close()
}
