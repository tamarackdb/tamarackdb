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
// the inserts, regardless of whether internal/gatekeeper ran first. This
// holds even if the gatekeeper (a separate, in-memory optimization for
// reducing contention/providing fairness before SQL work starts) granted
// two reservations it believed non-conflicting.
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
		conflict, err := checkCondition(ctx, tx, *condition)
		if err != nil {
			return nil, wrapf("check append condition", err)
		}
		if conflict {
			return nil, ErrConcurrencyConflict
		}
	}

	base := time.Now().UTC()
	result := make([]dcb.Event, len(events))
	for i, ed := range events {
		t := base.Add(time.Duration(i) * time.Microsecond)
		res, err := tx.ExecContext(ctx,
			"INSERT INTO events (time, type, payload) VALUES (?, ?, ?)",
			t.Format(timeLayout), ed.Type, ed.Payload)
		if err != nil {
			return nil, wrapf("insert event", err)
		}
		seq, err := res.LastInsertId()
		if err != nil {
			return nil, wrapf("read assigned sequence", err)
		}
		for _, id := range ed.Identifiers {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO identifiers (event_sequence, name, value) VALUES (?, ?, ?)",
				seq, id.Name, id.Value); err != nil {
				return nil, wrapf("insert identifier", err)
			}
		}
		for _, md := range ed.Metadata {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO metadata (event_sequence, name, value) VALUES (?, ?, ?)",
				seq, md.Name, md.Value); err != nil {
				return nil, wrapf("insert metadata", err)
			}
		}
		result[i] = dcb.Event{Sequence: seq, Time: t, EventData: ed}
	}

	if err := tx.Commit(); err != nil {
		return nil, wrapf("commit append", err)
	}
	return result, nil
}

// checkCondition implements the interpretation decided for internal/store:
// an AfterSequence-only condition (FailIfEventsMatch nil) behaves as if
// FailIfEventsMatch were dcb.QueryAll(): "does any event exist after
// afterSequence at all". This is a store-level interpretation only; it
// does not change dcb.AppendCondition's own semantics.
func checkCondition(ctx context.Context, tx *sql.Tx, cond dcb.AppendCondition) (bool, error) {
	effective := dcb.QueryAll()
	if cond.FailIfEventsMatch != nil {
		effective = *cond.FailIfEventsMatch
	}
	after := int64(0)
	if cond.AfterSequence != nil {
		after = *cond.AfterSequence
	}

	var b strings.Builder
	args := []any{after}
	b.WriteString("SELECT EXISTS (SELECT 1 FROM events WHERE events.sequence > ?")
	if where, whereArgs := queryToSQL(effective); where != "" {
		b.WriteString(" AND ")
		b.WriteString(where)
		args = append(args, whereArgs...)
	}
	b.WriteString(" LIMIT 1)")

	var conflict bool
	err := tx.QueryRowContext(ctx, b.String(), args...).Scan(&conflict)
	return conflict, err
}
