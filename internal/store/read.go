package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// ReadFilter holds Read's parameters, already resolved/validated by the
// caller: default/max limit applied.
// Store adds no defaults of its own except AfterSequence's implicit 0:
// omitting it reads from the beginning of the store.
type ReadFilter struct {
	Query         dcb.Query
	AfterSequence *int64 // nil = from the beginning; sequence > *AfterSequence
	Limit         int    // must be >= 1; Read fetches Limit+1 rows
	Backwards     bool   // newest first
}

// DecisionFilter holds ReadDecision's parameters. A Limit cuts only the
// events returned: the position, and so the Append Condition built from
// the read, stays the same.
type DecisionFilter struct {
	Query     dcb.Query
	Backwards bool // newest first
	Limit     int  // 0 = every match
}

// Page is one page of Read: its events, oldest first, or newest first
// backwards, and whether more match after the last one.
type Page struct {
	Events  []ReadEvent
	HasMore bool
}

// Read runs a paginated DCB read on the read pool: it sees committed
// events only. It loads the whole page in memory and ends its read
// transaction before it returns, so the connection goes back to the pool,
// and the snapshot is released, before the caller sends a single event.
// f.Limit bounds the memory a page takes.
func (s *Store) Read(ctx context.Context, f ReadFilter) (Page, error) {
	if f.Limit < 1 {
		return Page{}, fmt.Errorf("store: Read: Limit must be >= 1, got %d", f.Limit)
	}
	tx, err := s.readDB.BeginTx(ctx, nil)
	if err != nil {
		return Page{}, wrapf("begin read", err)
	}
	// The transaction only reads: rolling it back just ends it.
	defer tx.Rollback()
	sqlStr, args := buildReadSQL(f)
	events, err := queryEvents(ctx, tx, sqlStr, args)
	if err != nil {
		return Page{}, wrapf("Read", err)
	}
	// buildReadSQL asks for Limit+1 rows: the extra one only tells
	// whether more match.
	if len(events) > f.Limit {
		return Page{Events: events[:f.Limit], HasMore: true}, nil
	}
	return Page{Events: events}, nil
}

// Decision is the result of ReadDecision: the events, and the position,
// the highest Sequence Position committed in the snapshot they were read
// from (0 for an empty store).
type Decision struct {
	Events   []ReadEvent
	Position int64
}

// ReadDecision runs the read a decision rests on, on the read pool: the
// committed events f.Query matches, all of them unless f.Limit is set.
// There is no page: a decision rests on one read. It reads the position
// in the same SQLite snapshot: an Append Condition built from this read
// uses that position as its afterSequence, whatever f.Limit. For
// dcb.QueryNone it reads no event.
//
// Like Read, it loads every event in memory and ends its read
// transaction before it returns. Without f.Limit, nothing bounds that
// memory: the caller sets one (see tx.Config.MaxEventsPerTxRead).
func (s *Store) ReadDecision(ctx context.Context, f DecisionFilter) (Decision, error) {
	tx, err := s.readDB.BeginTx(ctx, nil)
	if err != nil {
		return Decision{}, wrapf("begin read", err)
	}
	defer tx.Rollback()
	var d Decision
	if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(sequence), 0) FROM events").Scan(&d.Position); err != nil {
		return Decision{}, wrapf("read position", err)
	}
	if f.Query.None() {
		return d, nil
	}
	sqlStr, args := buildEventsSQL(f.Query, 0, f.Backwards)
	if f.Limit > 0 {
		sqlStr, args = sqlStr+" LIMIT ?", append(args, f.Limit)
	}
	if d.Events, err = queryEvents(ctx, tx, sqlStr, args); err != nil {
		return Decision{}, wrapf("ReadDecision", err)
	}
	return d, nil
}

// querier is what a read needs, satisfied by both *sql.DB and *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// queryEvents runs a query built by buildEventsSQL and returns every row.
func queryEvents(ctx context.Context, q querier, sqlStr string, args []any) ([]ReadEvent, error) {
	rows, err := q.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []ReadEvent
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

func buildReadSQL(f ReadFilter) (string, []any) {
	after := int64(0)
	if f.AfterSequence != nil {
		after = *f.AfterSequence
	}
	sqlStr, args := buildEventsSQL(f.Query, after, f.Backwards)
	return sqlStr + " LIMIT ?", append(args, f.Limit+1)
}

// buildEventsSQL selects the events q matches after Sequence Position
// after, in ascending order, or descending with backwards, with no limit.
func buildEventsSQL(q dcb.Query, after int64, backwards bool) (string, []any) {
	var b strings.Builder
	args := make([]any, 0, 6)
	b.WriteString(`SELECT events.sequence, events.time, events.type, events.payload,
  events.identifiers, events.metadata
FROM events
WHERE events.sequence > ?`)
	args = append(args, after)

	if where, whereArgs := queryToSQL(q, after); where != "" {
		b.WriteString(" AND ")
		b.WriteString(where)
		args = append(args, whereArgs...)
	}
	if backwards {
		b.WriteString(" ORDER BY events.sequence DESC")
	} else {
		b.WriteString(" ORDER BY events.sequence ASC")
	}
	return b.String(), args
}

// ReadEvent is one event read. Time, Identifiers, and Metadata are kept
// as stored, already in the form the HTTP API returns (see
// insertEventsBatch). A read filters in SQL and never needs them decoded,
// so they aren't: a garbled column goes to the client as is, like the type
// and the payload.
type ReadEvent struct {
	Sequence    int64
	Time        string // raw events.time text, already UTC and dcb.TimeLayout-formatted
	Type        string
	Identifiers json.RawMessage
	Metadata    json.RawMessage
	Payload     string
}

func scanEvent(rows *sql.Rows) (ReadEvent, error) {
	var seq int64
	var tm, typ, payload string
	var idsJSON, mdJSON []byte
	if err := rows.Scan(&seq, &tm, &typ, &payload, &idsJSON, &mdJSON); err != nil {
		return ReadEvent{}, err
	}
	return ReadEvent{
		Sequence:    seq,
		Time:        tm,
		Type:        typ,
		Identifiers: json.RawMessage(idsJSON),
		Metadata:    json.RawMessage(mdJSON),
		Payload:     payload,
	}, nil
}
