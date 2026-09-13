package store

import (
	"context"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// Import writes pre-sequenced events (typically received from another
// TamarackDB instance's /read endpoint) directly into storage, in one
// transaction, bypassing sequence reservation and append-condition checks
// entirely. Unlike Append, callers are responsible for supplying events
// whose Sequence values are already assigned by their origin. A duplicate
// sequence fails naturally on the events table's primary key: no
// application-level dedup check is added here.
func (s *Store) Import(ctx context.Context, events []dcb.Event) error {
	if len(events) == 0 {
		return nil
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return wrapf("begin import", err)
	}
	defer tx.Rollback() // no-op after Commit

	if err := insertEventsBatch(ctx, tx, events); err != nil {
		return err
	}
	if err := insertIdentifiersBatch(ctx, tx, events); err != nil {
		return err
	}
	if err := insertMetadataBatch(ctx, tx, events); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return wrapf("commit import", err)
	}

	// events is not assumed to arrive sorted, so scan for the true
	// maximum rather than trusting the last element. Advancing nextSeq
	// only after the commit succeeds keeps a failed import from bumping
	// the counter for rows that were never actually persisted.
	var highest int64
	for _, ev := range events {
		if ev.Sequence > highest {
			highest = ev.Sequence
		}
	}
	s.seqMu.Lock()
	if highest+1 > s.nextSeq {
		s.nextSeq = highest + 1
	}
	s.seqMu.Unlock()

	return nil
}

// LastSequence returns the highest sequence number currently known to this
// Store, or 0 if it is empty.
func (s *Store) LastSequence() int64 {
	return s.peekLastAssigned()
}
