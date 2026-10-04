package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
}

// Read runs a paginated DCB read on the read pool: it sees committed
// events only. It reads the store ID and the page in one read transaction,
// so both come from the same SQLite snapshot: a page never pairs the events
// of one store ID with another (see Reset). The returned *EventIterator
// holds that transaction until it's closed, by the caller directly or by
// exhausting Next.
func (s *Store) Read(ctx context.Context, f ReadFilter) (*EventIterator, error) {
	tx, err := s.readDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrapf("begin read", err)
	}
	storeID, err := readStoreID(ctx, tx)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	it, err := readEvents(ctx, tx, f)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	it.tx = tx
	it.storeID = storeID
	return it, nil
}

// ReadDecision runs the read a decision rests on, on the read pool: every
// committed event q matches, with no page limit, since a decision must see
// all of them. In the same SQLite snapshot it reads the store ID and the
// position, the highest Sequence Position committed (0 for an empty
// store): an Append Condition built from this read uses that position as
// its afterSequence. For dcb.QueryNone it reads no event. The returned
// *EventIterator holds the read transaction until it's closed; HasMore is
// always false.
func (s *Store) ReadDecision(ctx context.Context, q dcb.Query) (*EventIterator, error) {
	tx, err := s.readDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrapf("begin read", err)
	}
	it, err := readDecision(ctx, tx, q)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	it.tx = tx
	return it, nil
}

func readDecision(ctx context.Context, tx *sql.Tx, q dcb.Query) (*EventIterator, error) {
	storeID, err := readStoreID(ctx, tx)
	if err != nil {
		return nil, err
	}
	var position int64
	if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(sequence), 0) FROM events").Scan(&position); err != nil {
		return nil, wrapf("read position", err)
	}
	it := &EventIterator{storeID: storeID, position: position}
	if q.None() {
		return it, nil
	}
	sqlStr, args := buildEventsSQL(q, 0)
	rows, err := tx.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, wrapf("ReadDecision", err)
	}
	it.rows = rows
	return it, nil
}

// querier is what a read needs, satisfied by both *sql.DB and *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readEvents(ctx context.Context, q querier, f ReadFilter) (*EventIterator, error) {
	if f.Limit < 1 {
		return nil, fmt.Errorf("store: Read: Limit must be >= 1, got %d", f.Limit)
	}
	sqlStr, args := buildReadSQL(f)
	rows, err := q.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, wrapf("Read", err)
	}
	return &EventIterator{rows: rows, limit: f.Limit}, nil
}

func buildReadSQL(f ReadFilter) (string, []any) {
	after := int64(0)
	if f.AfterSequence != nil {
		after = *f.AfterSequence
	}
	sqlStr, args := buildEventsSQL(f.Query, after)
	return sqlStr + " LIMIT ?", append(args, f.Limit+1)
}

// buildEventsSQL selects the events q matches after Sequence Position
// after, in ascending order, with no limit.
func buildEventsSQL(q dcb.Query, after int64) (string, []any) {
	var b strings.Builder
	args := make([]any, 0, 6)
	b.WriteString(`SELECT events.sequence, events.time, events.type, events.payload,
  events.identifiers, events.metadata
FROM events
WHERE events.sequence > ?`)
	args = append(args, after)

	if where, whereArgs := queryToSQL(q); where != "" {
		b.WriteString(" AND ")
		b.WriteString(where)
		args = append(args, whereArgs...)
	}
	b.WriteString(" ORDER BY events.sequence ASC")
	return b.String(), args
}

// ReadEvent is one row of a Read result. Time, Identifiers, and Metadata
// are left exactly as stored (see insertEventsBatch): time is always
// written from a UTC time.Time in dcb.TimeLayout, and identifiers/metadata are
// always written via IdentifierSet/MetadataSet's own MarshalJSON, so all
// three are already byte-identical to what the HTTP API returns. Nothing in the read
// path needs the structured form (query filtering already happened in SQL),
// so scanEvent skips decoding them at all, trading away read-time corruption
// detection on these three columns for that: a garbled column is
// forwarded to the client as-is instead of failing with a clear "corrupt
// data" error, the same trade-off already made for the type/payload columns.
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

// EventIterator streams Read's result page, or ReadDecision's result, one
// event at a time. Next returns false once Limit events have been
// returned (Read only) or the underlying query is exhausted; HasMore is only meaningful after that point (i.e.
// once Next has returned false); before then it is always false.
//
// internal/api's QUERY /events handler drives this with Next()/Event()/Err(),
// writing each event as it comes out of SQLite; the single underlying
// query keeps the read transaction short-lived, to support live projection
// rebuilds, never held open across pages.
type EventIterator struct {
	rows     *sql.Rows // nil when the read selects no event at all
	tx       *sql.Tx   // the read transaction Close ends
	storeID  string    // read in the same transaction as the page
	position int64     // ReadDecision only: the highest Sequence Position in the snapshot
	limit    int       // 0: no limit (ReadDecision)
	n        int
	hasMore  bool
	err      error
	cur      ReadEvent
	closed   bool
}

func (it *EventIterator) Next() bool {
	if it.err != nil || it.closed {
		return false
	}
	if it.rows == nil {
		it.Close()
		return false
	}
	if it.limit > 0 && it.n >= it.limit {
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

func (it *EventIterator) Event() ReadEvent { return it.cur }
func (it *EventIterator) Err() error       { return it.err }
func (it *EventIterator) HasMore() bool    { return it.hasMore }

// StoreID returns the store ID read in the same snapshot as the page.
func (it *EventIterator) StoreID() string { return it.storeID }

// Position returns, for ReadDecision, the highest Sequence Position
// committed in the snapshot the events were read from.
func (it *EventIterator) Position() int64 { return it.position }

// Close releases the underlying *sql.Rows, read transaction, and
// connection. Safe to call more
// than once, and safe to call before exhausting Next (e.g. a client
// disconnects mid-stream); HasMore then simply reflects whatever was known
// at that point.
func (it *EventIterator) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	var err error
	if it.rows != nil {
		err = it.rows.Close()
	}
	if it.tx != nil {
		// The transaction only read: rolling it back just ends it.
		err = errors.Join(err, it.tx.Rollback())
	}
	return err
}
