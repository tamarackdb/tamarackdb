package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// Append writes events in a single SQLite transaction (BEGIN IMMEDIATE, via
// the write pool's _txlock=immediate DSN), optionally checking condition
// first. events is assumed already validated by the caller (dcb.EventData
// .Validate, the 100-events-per-append cap): this package doesn't
// re-validate request shape, only concurrency and persistence.
//
// BEGIN IMMEDIATE takes SQLite's write lock before the condition check
// runs, so the check and the INSERTs execute under one continuously held
// lock: no other writer can commit conflicting rows between the check and
// the inserts. In production this overlaps with, rather than substitutes
// for, internal/queue's FIFO guarantee that only one writer is ever
// mid-transaction at a time; BEGIN IMMEDIATE remains as defense in depth
// and keeps this package correct even when Append is called directly, with
// no queue manager in front of it at all, as cmd/tamarackdb-demo and this package's
// own tests do.
func (s *Store) Append(ctx context.Context, events []dcb.EventData, condition *dcb.AppendCondition) ([]dcb.Event, error) {
	if len(events) == 0 {
		return nil, nil
	}
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrapf("begin append", err)
	}
	defer tx.Rollback() // no-op after Commit

	if condition != nil && (condition.FailIfEventsMatch != nil || condition.AfterSequence != nil) {
		after := int64(0)
		if condition.AfterSequence != nil {
			after = *condition.AfterSequence
		}
		holds, decided := resolveWithoutQuery(condition.FailIfEventsMatch, after, s.peekLastAssigned())
		if !decided {
			holds, err = checkFailIfEventsMatchSQL(ctx, tx, *condition.FailIfEventsMatch, after)
			if err != nil {
				return nil, wrapf("check append condition", err)
			}
		}
		if !holds {
			return nil, ErrConcurrencyConflict
		}
	}

	// The counter is only consulted, and only advanced, now that the
	// condition (if any) has been confirmed to hold: a failed condition
	// must leave no gap in the sequence.
	start := s.reserveSequences(len(events))
	base := time.Now().UTC()
	result := make([]dcb.Event, len(events))
	for i, ed := range events {
		result[i] = dcb.Event{
			Sequence:  start + int64(i),
			Time:      base.Add(time.Duration(i) * time.Microsecond),
			EventData: ed,
		}
	}

	if err := insertEventsBatch(ctx, tx, result); err != nil {
		return nil, err
	}
	if err := insertIdentifiersBatch(ctx, tx, result); err != nil {
		return nil, err
	}
	if err := insertMetadataBatch(ctx, tx, result); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, wrapf("commit append", err)
	}
	return result, nil
}

// peekLastAssigned returns the highest sequence number the in-memory
// counter has assigned so far (0 if nothing has ever been appended).
// Does not advance the counter.
func (s *Store) peekLastAssigned() int64 {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	return s.nextSeq - 1
}

// reserveSequences advances the counter by n and returns the first of the
// n sequence numbers reserved. Callers must only call this once the write
// is confirmed to actually happen — never speculatively.
func (s *Store) reserveSequences(n int) int64 {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	start := s.nextSeq
	s.nextSeq += int64(n)
	return start
}

// resolveWithoutQuery reports whether an Append Condition holds using only
// lastAssigned (the highest sequence the in-memory counter has assigned so
// far), with no SQL against events needed at all. decided is false only
// when failIfEventsMatch is non-nil and at least one event has been
// committed since after (lastAssigned > after): the caller must then run
// the real SELECT via checkFailIfEventsMatchSQL.
func resolveWithoutQuery(failIfEventsMatch *dcb.Query, after, lastAssigned int64) (holds, decided bool) {
	if failIfEventsMatch == nil {
		// A bare afterSequence condition means "does any event exist
		// after `after` at all" — always answerable from the counter
		// alone, no SELECT ever needed for this case.
		return lastAssigned <= after, true
	}
	if after == lastAssigned {
		// Nothing has been appended since the read that produced
		// `after`: failIfEventsMatch cannot match zero candidate events,
		// whatever it is.
		return true, true
	}
	return false, false
}

// checkFailIfEventsMatchSQL runs the real SELECT EXISTS check for
// failIfEventsMatch against events.sequence > after. Reached only when
// resolveWithoutQuery couldn't decide on its own.
func checkFailIfEventsMatchSQL(ctx context.Context, tx *sql.Tx, q dcb.Query, after int64) (holds bool, err error) {
	var b strings.Builder
	args := []any{after}
	b.WriteString("SELECT EXISTS (SELECT 1 FROM events WHERE events.sequence > ?")
	if where, whereArgs := queryToSQL(q); where != "" {
		b.WriteString(" AND ")
		b.WriteString(where)
		args = append(args, whereArgs...)
	}
	b.WriteString(" LIMIT 1)")

	var matches bool
	if err := tx.QueryRowContext(ctx, b.String(), args...).Scan(&matches); err != nil {
		return false, err
	}
	return !matches, nil
}

// maxBatchVariables caps how many bound parameters a single multi-row
// INSERT uses. It stays comfortably under SQLite's SQLITE_MAX_VARIABLE_NUMBER
// (32766 by default for modernc.org/sqlite) so a large caller-supplied batch,
// such as tamarackdb-backup importing a page of thousands of events, is
// split into several statements instead of failing with "too many SQL
// variables".
const maxBatchVariables = 32000

// execBatchInsert runs insertPrefix (an "INSERT INTO ... VALUES " clause)
// against rows, each holding colsPerRow values, splitting rows across
// multiple statements so no single one exceeds maxBatchVariables bound
// parameters.
func execBatchInsert(ctx context.Context, tx *sql.Tx, insertPrefix string, colsPerRow int, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}

	rowsPerChunk := maxBatchVariables / colsPerRow
	placeholder := "(" + strings.Repeat("?, ", colsPerRow-1) + "?)"

	for start := 0; start < len(rows); start += rowsPerChunk {
		end := min(start+rowsPerChunk, len(rows))
		chunk := rows[start:end]

		var b strings.Builder
		b.WriteString(insertPrefix)
		args := make([]any, 0, len(chunk)*colsPerRow)
		for i, row := range chunk {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(placeholder)
			args = append(args, row...)
		}
		if _, err := tx.ExecContext(ctx, b.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

// insertEventsBatch writes every row's events table entry, across as many
// multi-row INSERTs as maxBatchVariables requires.
func insertEventsBatch(ctx context.Context, tx *sql.Tx, rows []dcb.Event) error {
	args := make([][]any, len(rows))
	for i, ev := range rows {
		args[i] = []any{ev.Sequence, ev.Time.Format(timeLayout), ev.Type, ev.Payload}
	}
	err := execBatchInsert(ctx, tx, "INSERT INTO events (sequence, time, type, payload) VALUES ", 4, args)
	return wrapf("insert events", err)
}

// insertIdentifiersBatch writes every event's identifiers, across as many
// multi-row INSERTs as maxBatchVariables requires, skipping entirely when
// the batch carries none.
func insertIdentifiersBatch(ctx context.Context, tx *sql.Tx, rows []dcb.Event) error {
	var args [][]any
	for _, ev := range rows {
		for _, id := range ev.Identifiers {
			args = append(args, []any{ev.Sequence, id.Name, id.Value})
		}
	}
	err := execBatchInsert(ctx, tx, "INSERT INTO identifiers (event_sequence, name, value) VALUES ", 3, args)
	return wrapf("insert identifiers", err)
}

// insertMetadataBatch writes every event's metadata, across as many
// multi-row INSERTs as maxBatchVariables requires, skipping entirely when
// the batch carries none.
func insertMetadataBatch(ctx context.Context, tx *sql.Tx, rows []dcb.Event) error {
	var args [][]any
	for _, ev := range rows {
		for _, md := range ev.Metadata {
			args = append(args, []any{ev.Sequence, md.Name, md.Value})
		}
	}
	err := execBatchInsert(ctx, tx, "INSERT INTO metadata (event_sequence, name, value) VALUES ", 3, args)
	return wrapf("insert metadata", err)
}
