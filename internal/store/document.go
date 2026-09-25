package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/tamarackdb/tamarackdb/internal/document"
)

// GetDocument reads a document's payload by type+id from the read pool,
// outside any transaction: it sees committed documents only. found is
// false when no document exists at that type+id.
func (s *Store) GetDocument(ctx context.Context, typ, id string) (payload string, found bool, err error) {
	return getDocument(ctx, s.readDB, typ, id)
}

func getDocument(ctx context.Context, q querier, typ, id string) (payload string, found bool, err error) {
	err = q.QueryRowContext(ctx,
		"SELECT payload FROM documents WHERE type = ? AND id = ?", typ, id).Scan(&payload)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, wrapf("read document", err)
	}
	return payload, true, nil
}

// WriteDocuments creates, replaces, or deletes documents in a transaction
// of its own, outside any Tx. It's meant for a projection rebuild, while
// the server is paused.
func (s *Store) WriteDocuments(ctx context.Context, documents []document.Data) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit
	if err := tx.WriteDocuments(ctx, documents); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteDocumentsByType removes every document of typ. It's meant for a
// projection rebuild, so it has no per-document condition: it never
// reports a conflict, only a database error.
func (s *Store) DeleteDocumentsByType(ctx context.Context, typ string) error {
	_, err := s.writeDB.ExecContext(ctx, "DELETE FROM documents WHERE type = ?", typ)
	return wrapf("delete documents by type", err)
}

// DeleteAllDocuments removes every document, of every type: the same
// rebuild-time operation as DeleteDocumentsByType, widened to the whole
// store.
func (s *Store) DeleteAllDocuments(ctx context.Context) error {
	_, err := s.writeDB.ExecContext(ctx, "DELETE FROM documents")
	return wrapf("delete all documents", err)
}

// writeDocuments applies each document inside the caller's transaction: a
// non-nil payload creates or replaces the document, a nil payload deletes
// it. Deleting a document that doesn't exist does nothing.
func writeDocuments(ctx context.Context, tx *sql.Tx, documents []document.Data) error {
	for _, d := range documents {
		if d.Payload == nil {
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM documents WHERE type = ? AND id = ?", d.Type, d.ID); err != nil {
				return wrapf("delete document", err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO documents (type, id, payload) VALUES (?, ?, ?)
ON CONFLICT (type, id) DO UPDATE SET payload = excluded.payload`,
			d.Type, d.ID, *d.Payload); err != nil {
			return wrapf("write document", err)
		}
	}
	return nil
}
