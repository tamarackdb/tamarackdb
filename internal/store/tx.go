package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/document"
)

// Tx is one write transaction on the write connection. Everything done
// through it (reading events, appending events, reading and writing
// documents) sees what earlier calls on the same Tx wrote, and becomes
// visible to readers outside the Tx only once Commit succeeds.
//
// A Tx is not safe for concurrent use: its caller runs one call at a
// time. Since the write pool holds a single connection, a second Begin
// waits until this Tx ends.
type Tx struct {
	s  *Store
	tx *sql.Tx

	// savedNextSeq is the Sequence Position counter's value when the Tx
	// began. Rollback puts it back, so a rolled-back transaction leaves no
	// gap in the sequence.
	savedNextSeq int64
	done         bool
}

// Begin opens a write transaction with BEGIN IMMEDIATE (the write pool's
// _txlock=immediate DSN): SQLite's write lock is held from this moment,
// reads included, until Commit or Rollback.
//
// ctx governs the whole transaction, not just this call: database/sql
// rolls the transaction back if ctx is cancelled. It must outlive every
// call made with the Tx.
func (s *Store) Begin(ctx context.Context) (*Tx, error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrapf("begin", err)
	}
	s.seqMu.Lock()
	saved := s.nextSeq
	s.seqMu.Unlock()
	return &Tx{s: s, tx: tx, savedNextSeq: saved}, nil
}

// Read runs a paginated DCB read inside the transaction: it sees committed
// events and every event appended earlier on this Tx. The returned
// *EventIterator must be closed before the next call on this Tx.
func (t *Tx) Read(ctx context.Context, f ReadFilter) (*EventIterator, error) {
	return readEvents(ctx, t.tx, f)
}

// Append checks condition, then appends events, inside the transaction.
// It returns each event with its final Sequence Position and time: the
// transaction either commits them as they are, or rolls them back
// entirely. ErrConcurrencyConflict leaves the Tx open; ending it is the
// caller's decision.
func (t *Tx) Append(ctx context.Context, events []dcb.EventData, condition *dcb.AppendCondition) ([]dcb.Event, error) {
	return t.s.appendEvents(ctx, t.tx, events, condition)
}

// GetDocument reads a document inside the transaction: it sees documents
// written earlier on this Tx.
func (t *Tx) GetDocument(ctx context.Context, typ, id string) (payload string, found bool, err error) {
	return getDocument(ctx, t.tx, typ, id)
}

// WriteDocuments creates, replaces, or deletes documents inside the
// transaction (see document.Data).
func (t *Tx) WriteDocuments(ctx context.Context, documents []document.Data) error {
	return writeDocuments(ctx, t.tx, documents)
}

// Commit makes every event and document written on this Tx durable
// together. If the commit fails, the counter goes back to where it was
// when the Tx began, as for a rollback.
func (t *Tx) Commit() error {
	if t.done {
		return sql.ErrTxDone
	}
	t.done = true
	if err := t.tx.Commit(); err != nil {
		t.restoreSeq()
		return wrapf("commit", err)
	}
	return nil
}

// Rollback discards everything written on this Tx and puts the Sequence
// Position counter back. It's a no-op once the Tx has ended, so a deferred
// Rollback is always safe.
func (t *Tx) Rollback() error {
	if t.done {
		return nil
	}
	t.done = true
	t.restoreSeq()
	err := t.tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		// database/sql already rolled back, because Begin's ctx was
		// cancelled.
		return nil
	}
	return wrapf("rollback", err)
}

func (t *Tx) restoreSeq() {
	t.s.seqMu.Lock()
	t.s.nextSeq = t.savedNextSeq
	t.s.seqMu.Unlock()
}
