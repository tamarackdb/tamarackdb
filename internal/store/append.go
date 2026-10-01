package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
)

// AppendResult is what Append wrote.
type AppendResult struct {
	StoreID  string      // the store ID the write happened on
	Events   []dcb.Event // each event with its Sequence Position and time
	Versions Versions    // the new version of each created and replaced projection
}

// Append writes events and projections in one transaction of its own, if
// every one of conditions holds: Begin, the condition checks, the event
// inserts, Tx.WriteProjections, then Commit. Events and projections
// commit together, or not at all. The conditions are checked first,
// against the events committed before this write, and are checked even
// when there is nothing else to write. The first one that doesn't hold
// returns a *ConditionConflictError.
//
// events and projections are assumed already validated by the caller
// (dcb.EventData.Validate, projection.Data.Validate, the per-call caps),
// and conditions too (dcb.AppendCondition.Validate and ValidateStore):
// this package doesn't re-validate request shape, only concurrency and
// persistence.
func (s *Store) Append(ctx context.Context, events []dcb.EventData, conditions []dcb.AppendCondition, projections projection.Writes) (AppendResult, error) {
	if len(events) == 0 && len(conditions) == 0 && projections.Len() == 0 {
		return AppendResult{StoreID: s.currentStoreID()}, nil
	}

	tx, err := s.Begin(ctx)
	if err != nil {
		return AppendResult{}, err
	}
	defer tx.Rollback() // no-op after Commit

	// Reset runs on the same single write connection, and updates the
	// store ID before it frees it: the ID can't change until this
	// transaction ends.
	storeID := s.currentStoreID()
	for i, c := range conditions {
		if c.AfterSequence != nil && c.Store != storeID {
			return AppendResult{}, &ConditionConflictError{Index: i, StoreChanged: true}
		}
		holds, err := s.checkCondition(ctx, tx.tx, c)
		if err != nil {
			return AppendResult{}, err
		}
		if !holds {
			return AppendResult{}, &ConditionConflictError{Index: i}
		}
	}

	appended, err := s.insertEvents(ctx, tx.tx, events)
	if err != nil {
		return AppendResult{}, err
	}
	versions, err := tx.WriteProjections(ctx, projections)
	if err != nil {
		return AppendResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppendResult{}, err
	}
	return AppendResult{StoreID: storeID, Events: appended, Versions: versions}, nil
}

// appendEvents checks condition, then inserts events, inside tx. The
// check runs against every event visible to tx: committed ones, and the
// ones appended earlier in the same transaction. The write lock is already
// held (BEGIN IMMEDIATE), so no other writer can commit conflicting rows
// between the check and the inserts. With no events, the condition isn't
// checked at all.
func (s *Store) appendEvents(ctx context.Context, tx *sql.Tx, events []dcb.EventData, condition *dcb.AppendCondition) ([]dcb.Event, error) {
	if len(events) == 0 {
		return nil, nil
	}
	if condition != nil {
		holds, err := s.checkCondition(ctx, tx, *condition)
		if err != nil {
			return nil, err
		}
		if !holds {
			return nil, ErrConcurrencyConflict
		}
	}
	return s.insertEvents(ctx, tx, events)
}

// checkCondition reports whether c holds against every event visible to
// tx. It ignores c.Store: checking it is up to the caller.
func (s *Store) checkCondition(ctx context.Context, tx *sql.Tx, c dcb.AppendCondition) (bool, error) {
	if c.FailIfEventsMatch == nil && c.AfterSequence == nil {
		return true, nil
	}
	after := int64(0)
	if c.AfterSequence != nil {
		after = *c.AfterSequence
	}
	holds, decided := resolveWithoutQuery(c.FailIfEventsMatch, after, s.peekLastAssigned())
	if decided {
		return holds, nil
	}
	holds, err := checkFailIfEventsMatchSQL(ctx, tx, *c.FailIfEventsMatch, after)
	if err != nil {
		return false, wrapf("check append condition", err)
	}
	return holds, nil
}

// insertEvents gives events their Sequence Positions and time, and
// inserts them inside tx. The caller must only call it once the write is
// confirmed to happen: every condition has already been checked.
func (s *Store) insertEvents(ctx context.Context, tx *sql.Tx, events []dcb.EventData) ([]dcb.Event, error) {
	if len(events) == 0 {
		return nil, nil
	}

	// The counter is only consulted, and only advanced, now that the
	// conditions have been confirmed to hold: a failed condition must
	// leave no gap in the sequence.
	start := s.reserveSequences(len(events))
	// One time for the whole append: every event in it is appended at the
	// same moment. Order within the append comes from Sequence. Truncated
	// to the stored microsecond precision, so the value returned here is
	// exactly the one a read returns later.
	now := time.Now().UTC().Truncate(time.Microsecond)
	result := make([]dcb.Event, len(events))
	for i, ed := range events {
		result[i] = dcb.Event{
			Sequence:  start + int64(i),
			Time:      now,
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
	return result, nil
}

// currentStoreID returns the store ID (see storeid.go).
func (s *Store) currentStoreID() string {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	return s.storeID
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
// is confirmed to actually happen, never speculatively.
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
		// after `after` at all". It is always answerable from the counter
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
