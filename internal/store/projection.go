package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/tamarackdb/tamarackdb/internal/projection"
)

// ProjectionRead is GetProjection's result.
type ProjectionRead struct {
	Found   bool // false when no projection exists at that type+id
	Version string
	Payload string
}

// GetProjection reads a projection's version and payload by type+id from
// the read pool: it sees committed projections only.
func (s *Store) GetProjection(ctx context.Context, typ, id string) (ProjectionRead, error) {
	version, payload, found, err := getProjection(ctx, s.readDB, typ, id)
	if err != nil {
		return ProjectionRead{}, err
	}
	return ProjectionRead{Found: found, Version: version, Payload: payload}, nil
}

func getProjection(ctx context.Context, q querier, typ, id string) (version, payload string, found bool, err error) {
	err = q.QueryRowContext(ctx,
		"SELECT version, payload FROM projections WHERE type = ? AND id = ?", typ, id).Scan(&version, &payload)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", false, nil
	case err != nil:
		return "", "", false, wrapf("read projection", err)
	}
	return version, payload, true, nil
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

// Versions holds the version each written projection got, in the order of
// the Create and Replace lists it was written from. Deletes have none.
type Versions struct {
	Create  []string
	Replace []string
}

// writeProjections applies w inside the caller's transaction, and returns
// the new version of every created and replaced projection. Each write is
// conditional: a create needs the type+id to be free, a replace or a
// delete needs the stored version to be the one given. The first write
// that doesn't hold returns a *ProjectionConflictError; the caller's
// transaction is then expected to roll back.
func writeProjections(ctx context.Context, tx *sql.Tx, w projection.Writes) (Versions, error) {
	versions := Versions{Create: make([]string, len(w.Create)), Replace: make([]string, len(w.Replace))}
	for i, c := range w.Create {
		version := uuid.NewString()
		res, err := tx.ExecContext(ctx, `
INSERT INTO projections (type, id, version, payload) VALUES (?, ?, ?, ?)
ON CONFLICT (type, id) DO NOTHING`,
			c.Type, c.ID, version, *c.Payload)
		if err := checkWritten(res, err, "create", i); err != nil {
			return Versions{}, err
		}
		versions.Create[i] = version
	}
	for i, r := range w.Replace {
		version := uuid.NewString()
		res, err := tx.ExecContext(ctx,
			"UPDATE projections SET version = ?, payload = ? WHERE type = ? AND id = ? AND version = ?",
			version, *r.Payload, r.Type, r.ID, r.Version)
		if err := checkWritten(res, err, "replace", i); err != nil {
			return Versions{}, err
		}
		versions.Replace[i] = version
	}
	for i, d := range w.Delete {
		res, err := tx.ExecContext(ctx,
			"DELETE FROM projections WHERE type = ? AND id = ? AND version = ?",
			d.Type, d.ID, d.Version)
		if err := checkWritten(res, err, "delete", i); err != nil {
			return Versions{}, err
		}
	}
	return versions, nil
}

// checkWritten turns the outcome of one conditional projection write into
// an error: the statement's own error, or a *ProjectionConflictError when
// it touched no row.
func checkWritten(res sql.Result, err error, op string, index int) error {
	if err != nil {
		return wrapf(op+" projection", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return wrapf(op+" projection", err)
	}
	if n == 0 {
		return &ProjectionConflictError{Op: op, Index: index}
	}
	return nil
}
