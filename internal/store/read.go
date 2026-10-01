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
	var b strings.Builder
	args := make([]any, 0, 6)
	b.WriteString(`SELECT events.sequence, events.time, events.type, events.payload,
  events.identifiers, events.metadata
FROM events
WHERE events.sequence > ?`)

	after := int64(0)
	if f.AfterSequence != nil {
		after = *f.AfterSequence
	}
	args = append(args, after)

	if where, whereArgs := queryToSQL(f.Query); where != "" {
		b.WriteString(" AND ")
		b.WriteString(where)
		args = append(args, whereArgs...)
	}
	b.WriteString(" ORDER BY events.sequence ASC LIMIT ?")
	args = append(args, f.Limit+1)
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

// EventIterator streams Read's result page one event at a time. Next
// returns false once Limit events have been returned or the underlying
// query is exhausted; HasMore is only meaningful after that point (i.e.
// once Next has returned false); before then it is always false.
//
// internal/api's QUERY /events handler drives this with Next()/Event()/Err(),
// writing each event as it comes out of SQLite; the single underlying
// query keeps the read transaction short-lived, to support live projection
// rebuilds, never held open across pages.
type EventIterator struct {
	rows    *sql.Rows
	tx      *sql.Tx // the read transaction Close ends
	storeID string  // read in the same transaction as the page
	limit   int
	n       int
	hasMore bool
	err     error
	cur     ReadEvent
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

func (it *EventIterator) Event() ReadEvent { return it.cur }
func (it *EventIterator) Err() error       { return it.err }
func (it *EventIterator) HasMore() bool    { return it.hasMore }

// StoreID returns the store ID read in the same snapshot as the page.
func (it *EventIterator) StoreID() string { return it.storeID }

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
	err := it.rows.Close()
	if it.tx != nil {
		// The transaction only read: rolling it back just ends it.
		err = errors.Join(err, it.tx.Rollback())
	}
	return err
}
