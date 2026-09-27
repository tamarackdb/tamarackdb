package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/tamarackdb/tamarackdb/internal/projection"
)

// GetProjection reads a projection's payload by type+id from the read pool,
// outside any transaction: it sees committed projections only. found is
// false when no projection exists at that type+id.
func (s *Store) GetProjection(ctx context.Context, typ, id string) (payload string, found bool, err error) {
	return getProjection(ctx, s.readDB, typ, id)
}

func getProjection(ctx context.Context, q querier, typ, id string) (payload string, found bool, err error) {
	err = q.QueryRowContext(ctx,
		"SELECT payload FROM projections WHERE type = ? AND id = ?", typ, id).Scan(&payload)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, wrapf("read projection", err)
	}
	return payload, true, nil
}

// WriteProjections creates, replaces, or deletes projections in a transaction
// of its own, outside any Tx. It's meant for a projection rebuild, while
// the server is paused.
func (s *Store) WriteProjections(ctx context.Context, projections []projection.Data) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit
	if err := tx.WriteProjections(ctx, projections); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteProjectionsByType removes every projection of typ. It's meant for a
// projection rebuild, so it has no per-projection condition: it never
// reports a conflict, only a database error.
func (s *Store) DeleteProjectionsByType(ctx context.Context, typ string) error {
	_, err := s.writeDB.ExecContext(ctx, "DELETE FROM projections WHERE type = ?", typ)
	return wrapf("delete projections by type", err)
}

// DeleteAllProjections removes every projection, of every type: the same
// rebuild-time operation as DeleteProjectionsByType, widened to the whole
// store.
func (s *Store) DeleteAllProjections(ctx context.Context) error {
	_, err := s.writeDB.ExecContext(ctx, "DELETE FROM projections")
	return wrapf("delete all projections", err)
}

// writeProjections applies each projection inside the caller's transaction: a
// non-nil payload creates or replaces the projection, a nil payload deletes
// it. Deleting a projection that doesn't exist does nothing.
func writeProjections(ctx context.Context, tx *sql.Tx, projections []projection.Data) error {
	for _, d := range projections {
		if d.Payload == nil {
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM projections WHERE type = ? AND id = ?", d.Type, d.ID); err != nil {
				return wrapf("delete projection", err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO projections (type, id, payload) VALUES (?, ?, ?)
ON CONFLICT (type, id) DO UPDATE SET payload = excluded.payload`,
			d.Type, d.ID, *d.Payload); err != nil {
			return wrapf("write projection", err)
		}
	}
	return nil
}
