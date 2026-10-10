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
//
// Each list runs one statement, prepared once: a rebuild writes hundreds
// of projections per call, in the FIFO's turn, and parsing the SQL again
// for each one cost more than the write itself.
func writeProjections(ctx context.Context, tx *sql.Tx, w projection.Writes) (Versions, error) {
	versions := Versions{Create: make([]string, len(w.Create)), Replace: make([]string, len(w.Replace))}
	err := execEach(ctx, tx, `
INSERT INTO projections (type, id, version, payload) VALUES (?, ?, ?, ?)
ON CONFLICT (type, id) DO NOTHING`, "create", len(w.Create), func(i int) []any {
		versions.Create[i] = uuid.NewString()
		c := w.Create[i]
		return []any{c.Type, c.ID, versions.Create[i], *c.Payload}
	})
	if err != nil {
		return Versions{}, err
	}
	err = execEach(ctx, tx,
		"UPDATE projections SET version = ?, payload = ? WHERE type = ? AND id = ? AND version = ?",
		"replace", len(w.Replace), func(i int) []any {
			versions.Replace[i] = uuid.NewString()
			r := w.Replace[i]
			return []any{versions.Replace[i], *r.Payload, r.Type, r.ID, r.Version}
		})
	if err != nil {
		return Versions{}, err
	}
	err = execEach(ctx, tx,
		"DELETE FROM projections WHERE type = ? AND id = ? AND version = ?",
		"delete", len(w.Delete), func(i int) []any {
			d := w.Delete[i]
			return []any{d.Type, d.ID, d.Version}
		})
	if err != nil {
		return Versions{}, err
	}
	return versions, nil
}

// execEach prepares query once, then runs it n times, with the arguments
// args(i) returns for the i-th projection of the op list. Each run must
// touch a row (see checkWritten). It prepares nothing for an empty list.
func execEach(ctx context.Context, tx *sql.Tx, query, op string, n int, args func(i int) []any) error {
	if n == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return wrapf("prepare "+op+" projection", err)
	}
	defer stmt.Close()
	for i := range n {
		res, err := stmt.ExecContext(ctx, args(i)...)
		if err := checkWritten(res, err, op, i); err != nil {
			return err
		}
	}
	return nil
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
