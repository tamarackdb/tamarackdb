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
// caller: default/max limit applied, clientTime range strings parsed.
// Store adds no defaults of its own except AfterSequence's implicit 0:
// omitting it reads from the beginning of the store.
type ReadFilter struct {
	Query            dcb.Query
	AfterSequence    *int64     // nil = from the beginning; sequence > *AfterSequence
	ClientTimeFrom   *time.Time // nil = unconstrained; inclusive (>=)
	ClientTimeBefore *time.Time // nil = unconstrained; exclusive (<)
	Limit            int        // must be >= 1; Read fetches Limit+1 rows
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
	b.WriteString(`SELECT events.sequence, events.client_time, events.write_time, events.type, events.payload,
  events.identifiers, events.metadata
FROM events
WHERE events.sequence > ?`)

	after := int64(0)
	if f.AfterSequence != nil {
		after = *f.AfterSequence
	}
	args = append(args, after)

	if f.ClientTimeFrom != nil {
		b.WriteString(" AND events.client_time >= ?")
		args = append(args, f.ClientTimeFrom.UTC().Format(timeLayout))
	}
	if f.ClientTimeBefore != nil {
		b.WriteString(" AND events.client_time < ?")
		args = append(args, f.ClientTimeBefore.UTC().Format(timeLayout))
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

// ReadEvent is one row of a Read result. ClientTime, WriteTime, Identifiers,
// and Metadata are left exactly as stored (see insertEventsBatch):
// client_time is the client's string, already validated as UTC in
// timeLayout, write_time is always written from a UTC time.Time in
// timeLayout, and identifiers/metadata are always written via
// IdentifierSet/MetadataSet's own MarshalJSON, so all four are already
// byte-identical to what the HTTP API returns. Nothing in the read
// path needs the structured form (query filtering already happened in SQL),
// so scanEvent skips decoding them at all, trading away read-time corruption
// detection on these four columns for that: a garbled column is now
// forwarded to the client as-is instead of failing with a clear "corrupt
// data" error, the same trade-off already made for the type/payload columns.
type ReadEvent struct {
	Sequence    int64
	ClientTime  string // raw events.client_time text, already UTC and timeLayout-formatted
	WriteTime   string // raw events.write_time text, already UTC and timeLayout-formatted
	Type        string
	Identifiers json.RawMessage
	Metadata    json.RawMessage
	Payload     string
}

func scanEvent(rows *sql.Rows) (ReadEvent, error) {
	var seq int64
	var clientTime, writeTime, typ, payload string
	var idsJSON, mdJSON []byte
	if err := rows.Scan(&seq, &clientTime, &writeTime, &typ, &payload, &idsJSON, &mdJSON); err != nil {
		return ReadEvent{}, err
	}
	return ReadEvent{
		Sequence:    seq,
		ClientTime:  clientTime,
		WriteTime:   writeTime,
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
// internal/api's /read handler drives this with Next()/Event()/Err(),
// writing each event as it comes out of SQLite; the single underlying
// query keeps the read transaction short-lived, to support live projection
// rebuilds, never held open across pages.
type EventIterator struct {
	rows    *sql.Rows
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
