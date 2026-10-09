package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// PausedAt returns when the pause began, and whether the store is paused.
// It reads memory only.
func (s *Store) PausedAt() (time.Time, bool) {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	return s.pausedAt, !s.pausedAt.IsZero()
}

// SetPause records a pause that began at at, and returns the last
// Sequence Position as it stands at that moment. It runs on the write connection, so the caller MUST call it in its turn in
// the FIFO: no write can then come between the pause and the sequence it
// returns.
func (s *Store) SetPause(ctx context.Context, at time.Time) (lastSequence int64, err error) {
	at = at.UTC().Truncate(time.Microsecond)
	if _, err := s.writeDB.ExecContext(ctx, "UPDATE store SET paused_at = ?", at.Format(dcb.TimeLayout)); err != nil {
		return 0, wrapf("set pause", err)
	}
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.pausedAt = at
	return s.nextSeq - 1, nil
}

// ClearPause ends the pause. The caller MUST call it in its turn in the
// FIFO, like SetPause.
func (s *Store) ClearPause(ctx context.Context) error {
	if _, err := s.writeDB.ExecContext(ctx, "UPDATE store SET paused_at = NULL"); err != nil {
		return wrapf("clear pause", err)
	}
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.pausedAt = time.Time{}
	return nil
}

func readPausedAt(ctx context.Context, q querier) (time.Time, error) {
	var at sql.NullString
	if err := q.QueryRowContext(ctx, "SELECT paused_at FROM store").Scan(&at); err != nil {
		return time.Time{}, wrapf("read pause", err)
	}
	if !at.Valid {
		return time.Time{}, nil
	}
	t, err := time.Parse(dcb.TimeLayout, at.String)
	return t, wrapf("parse pause time", err)
}
