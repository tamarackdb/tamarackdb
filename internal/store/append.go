package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
)

// AppendResult is what Append wrote.
type AppendResult struct {
	Events   []dcb.Event // each event with its Sequence Position and time
	Versions Versions    // the new version of each created and replaced projection
}

// Append writes events and projections in one SQLite transaction of its
// own, if every one of conditions holds: the condition checks, the
// projection writes, the event inserts, then the commit. Events and
// projections commit together, or not at all. The conditions are checked
// first, against the events committed before this write, and are checked
// even when there is nothing else to write. The first one that doesn't
// hold returns a *ConditionConflictError, and the first projection write
// that doesn't hold a *ProjectionConflictError.
//
// Each event keeps the time it carries: the caller sets it, from the
// server's clock (dcb.Now). An event with a zero time is refused before
// anything is written.
//
// events, conditions, and projections are assumed already validated by
// the caller (dcb.EventData.Validate, dcb.Query.Validate, the projection
// types' Validate, the per-call caps): this package doesn't
// re-validate request shape, only concurrency and persistence.
//
// ctx governs the whole transaction: database/sql rolls it back if ctx is
// cancelled before the commit.
func (s *Store) Append(ctx context.Context, events []dcb.PendingEvent, conditions []dcb.AppendCondition, projections projection.Writes) (AppendResult, error) {
	for i, e := range events {
		if e.Time.IsZero() {
			return AppendResult{}, fmt.Errorf("store: Append: events[%d] has no time", i)
		}
	}
	if len(events) == 0 && len(conditions) == 0 && projections.Len() == 0 {
		return AppendResult{}, nil
	}

	// BEGIN IMMEDIATE (the write pool's _txlock=immediate DSN): SQLite's
	// write lock is held from here, so no other writer can commit between
	// the condition checks and the inserts.
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return AppendResult{}, wrapf("begin", err)
	}
	defer tx.Rollback() // no-op after Commit

	for i, c := range conditions {
		holds, err := s.checkCondition(ctx, tx, c)
		if err != nil {
			return AppendResult{}, err
		}
		if !holds {
			return AppendResult{}, &ConditionConflictError{Index: i}
		}
	}

	// Projections before events: a projection conflict then ends the
	// write before any Sequence Position is reserved.
	versions, err := writeProjections(ctx, tx, projections)
	if err != nil {
		return AppendResult{}, err
	}

	// From here on, the Sequence Positions are reserved: unless the write
	// commits, the counter goes back, whether the write fails or panics, so
	// it leaves no gap in the sequence. Nothing else can reserve any
	// meanwhile: this transaction holds the only write connection.
	start := s.reserveSequences(len(events))
	committed := false
	defer func() {
		if !committed {
			s.releaseSequences(start)
		}
	}()
	if afterReserve != nil {
		afterReserve()
	}
	appended, err := insertEvents(ctx, tx, events, start)
	if err != nil {
		return AppendResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppendResult{}, wrapf("commit", err)
	}
	committed = true
	return AppendResult{Events: appended, Versions: versions}, nil
}

// afterReserve, when set, runs in Append right after it reserves its
// Sequence Positions. It's nil outside tests: a variable, so a test can
// make a write panic there.
var afterReserve func()

// checkCondition reports whether c holds against every event visible to
// tx.
func (s *Store) checkCondition(ctx context.Context, tx *sql.Tx, c dcb.AppendCondition) (bool, error) {
	holds, decided := resolveWithoutQuery(c.FailIfEventsMatch, c.AfterSequence, s.peekLastAssigned())
	if decided {
		return holds, nil
	}
	holds, err := checkFailIfEventsMatchSQL(ctx, tx, c.FailIfEventsMatch, c.AfterSequence)
	if err != nil {
		return false, wrapf("check append condition", err)
	}
	return holds, nil
}

// insertEvents gives events the Sequence Positions from start on, and
// inserts them inside tx with the time each one carries. The caller must
// only call it once the write is confirmed to happen: every condition has
// already been checked.
func insertEvents(ctx context.Context, tx *sql.Tx, events []dcb.PendingEvent, start int64) ([]dcb.Event, error) {
	if len(events) == 0 {
		return nil, nil
	}

	result := make([]dcb.Event, len(events))
	for i, e := range events {
		result[i] = dcb.Event{
			Sequence:  start + int64(i),
			Time:      e.Time,
			EventData: e.EventData,
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
// n sequence numbers reserved. Callers must only call this once the
// conditions hold, never speculatively, and call releaseSequences if the
// write then fails.
func (s *Store) reserveSequences(n int) int64 {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	start := s.nextSeq
	s.nextSeq += int64(n)
	return start
}

// releaseSequences puts the counter back to start, for a write that
// reserved sequences from start on and then failed.
func (s *Store) releaseSequences(start int64) {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.nextSeq = start
}

// resolveWithoutQuery reports whether an Append Condition holds using only
// lastAssigned (the highest sequence the in-memory counter has assigned so
// far), with no SQL against events needed at all. decided is false only
// when failIfEventsMatch is a query other than "none" and at least one
// event has been committed since after (lastAssigned > after): the caller
// must then run the real SELECT via checkFailIfEventsMatchSQL.
func resolveWithoutQuery(failIfEventsMatch dcb.Query, after, lastAssigned int64) (holds, decided bool) {
	if failIfEventsMatch.None() {
		// No event can match "none": a decision that rests on no event
		// always holds.
		return true, true
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
// multi-row INSERTs as maxBatchVariables requires. identifiers/metadata are
// stored alongside each row in the same compact wire shape the HTTP API
// uses ({"name":"value"} / {"name":["v1","v2"]}), via IdentifierSet/
// MetadataSet's own MarshalJSON.
func insertEventsBatch(ctx context.Context, tx *sql.Tx, rows []dcb.Event) error {
	args := make([][]any, len(rows))
	for i, ev := range rows {
		idsJSON, err := json.Marshal(ev.Identifiers)
		if err != nil {
			return wrapf("marshal identifiers", err)
		}
		mdJSON, err := json.Marshal(ev.Metadata)
		if err != nil {
			return wrapf("marshal metadata", err)
		}
		args[i] = []any{ev.Sequence, ev.Time.UTC().Format(dcb.TimeLayout), ev.Type, ev.Payload, string(idsJSON), string(mdJSON)}
	}
	err := execBatchInsert(ctx, tx,
		"INSERT INTO events (sequence, time, type, payload, identifiers, metadata) VALUES ", 6, args)
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
