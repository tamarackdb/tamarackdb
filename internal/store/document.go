package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/tamarackdb/tamarackdb/internal/document"
)

// DocumentStatus is GetDocument's result: whether a document's metadata
// exists at all, and if so, whether its payload has caught up with it.
type DocumentStatus int

const (
	// DocumentNotFound means no document exists at this type+id: no
	// metadata row in the events file.
	DocumentNotFound DocumentStatus = iota
	// DocumentNotReady means the metadata exists, but the payload for its
	// current version isn't in tamarackdb-documents.sqlite yet (or was
	// never written): the caller should retry later, not treat this as
	// absent.
	DocumentNotReady
	// DocumentFound means both the metadata and a matching-version payload
	// exist; Data is populated.
	DocumentFound
)

// DocumentWriteResult reports what happened to one document.Data passed
// to Append: the version its metadata now carries (the version deleted,
// for a deletion, since there's no new one to report), and whether its
// payload write to tamarackdb-documents.sqlite succeeded. PayloadWritten
// is false only when the metadata transaction already committed but the
// best-effort payload step failed (see the design doc's accepted-risk
// rationale); it never means the whole document write was rolled back.
type DocumentWriteResult struct {
	Type           string
	ID             string
	Version        int64
	PayloadWritten bool
}

// GetDocument reads a document by type+id. Metadata (the version) is
// read from the events file's readDB; the payload is read from
// tamarackdb-documents.sqlite separately, and only trusted when its
// version matches the metadata's. A mismatch (or an outright missing
// payload row) means the payload write for the current version hasn't
// landed yet, reported as DocumentNotReady rather than served stale.
func (s *Store) GetDocument(ctx context.Context, typ, id string) (document.Data, DocumentStatus, error) {
	if s.docReadDB == nil {
		return document.Data{}, DocumentNotFound, ErrDocumentsNotOpen
	}

	var metaVersion int64
	err := s.readDB.QueryRowContext(ctx,
		"SELECT version FROM documents WHERE type = ? AND id = ?", typ, id).Scan(&metaVersion)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return document.Data{}, DocumentNotFound, nil
	case err != nil:
		return document.Data{}, DocumentNotFound, wrapf("read document metadata", err)
	}

	var payload string
	var payloadVersion int64
	err = s.docReadDB.QueryRowContext(ctx,
		"SELECT payload, version FROM documents_payload WHERE type = ? AND id = ?", typ, id).Scan(&payload, &payloadVersion)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return document.Data{}, DocumentNotReady, nil
	case err != nil:
		return document.Data{}, DocumentNotFound, wrapf("read document payload", err)
	}
	if payloadVersion != metaVersion {
		return document.Data{}, DocumentNotReady, nil
	}

	return document.Data{Type: typ, ID: id, Payload: &payload, Version: &metaVersion}, DocumentFound, nil
}

// DeleteDocumentsByType removes every document of typ from both files:
// its metadata (events file) and its payload
// (tamarackdb-documents.sqlite). Unversioned by design (see the design
// doc's rebuild rationale), so it never reports a conflict, only a
// database error.
func (s *Store) DeleteDocumentsByType(ctx context.Context, typ string) error {
	if s.docWriteDB == nil {
		return ErrDocumentsNotOpen
	}
	if _, err := s.writeDB.ExecContext(ctx, "DELETE FROM documents WHERE type = ?", typ); err != nil {
		return wrapf("delete document metadata by type", err)
	}
	if _, err := s.docWriteDB.ExecContext(ctx, "DELETE FROM documents_payload WHERE type = ?", typ); err != nil {
		return wrapf("delete document payload by type", err)
	}
	return nil
}

// DeleteAllDocuments removes every document, of every type, from both
// files. It's the same rebuild-time operation as DeleteDocumentsByType,
// widened to the whole store: a shortcut for a total rebuild that
// touches every projection at once, instead of one DeleteDocumentsByType
// call per type. Unversioned by design, for the same reason.
func (s *Store) DeleteAllDocuments(ctx context.Context) error {
	if s.docWriteDB == nil {
		return ErrDocumentsNotOpen
	}
	if _, err := s.writeDB.ExecContext(ctx, "DELETE FROM documents"); err != nil {
		return wrapf("delete document metadata", err)
	}
	if _, err := s.docWriteDB.ExecContext(ctx, "DELETE FROM documents_payload"); err != nil {
		return wrapf("delete document payload", err)
	}
	return nil
}

// applyDocumentMetadata runs the one query matching d's shape (create,
// update, or delete, see document.Data's own doc comment) against the
// documents table, inside the caller's still-open events-file
// transaction. It returns the version to report for d: the new version
// for a create/update, or the version just deleted. ErrConcurrencyConflict
// covers every way d's expectation didn't hold: wrong version, or a
// create colliding with a document that already exists.
func applyDocumentMetadata(ctx context.Context, tx *sql.Tx, d document.Data) (int64, error) {
	switch {
	case d.Payload != nil && d.Version == nil:
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO documents (type, id, version) VALUES (?, ?, 1)", d.Type, d.ID); err != nil {
			if isConstraintViolation(err) {
				return 0, ErrConcurrencyConflict
			}
			return 0, wrapf("insert document", err)
		}
		return 1, nil

	case d.Payload != nil:
		newVersion := *d.Version + 1
		res, err := tx.ExecContext(ctx,
			"UPDATE documents SET version = ? WHERE type = ? AND id = ? AND version = ?",
			newVersion, d.Type, d.ID, *d.Version)
		if err != nil {
			return 0, wrapf("update document", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, wrapf("update document rows affected", err)
		}
		if n == 0 {
			return 0, ErrConcurrencyConflict
		}
		return newVersion, nil

	default: // d.Payload == nil: deletion, d.Version != nil (document.Data.Validate already enforced this)
		res, err := tx.ExecContext(ctx,
			"DELETE FROM documents WHERE type = ? AND id = ? AND version = ?", d.Type, d.ID, *d.Version)
		if err != nil {
			return 0, wrapf("delete document", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, wrapf("delete document rows affected", err)
		}
		if n == 0 {
			return 0, ErrConcurrencyConflict
		}
		return *d.Version, nil
	}
}

// writeDocumentPayload writes (or deletes) d's payload in
// tamarackdb-documents.sqlite, run only after the events-file
// transaction that confirmed newVersion has already committed. Its own
// error is never fatal to the caller's Append: see Append's doc comment.
func (s *Store) writeDocumentPayload(ctx context.Context, d document.Data, newVersion int64) error {
	if d.Payload != nil {
		_, err := s.docWriteDB.ExecContext(ctx, `
INSERT INTO documents_payload (type, id, payload, version) VALUES (?, ?, ?, ?)
ON CONFLICT (type, id) DO UPDATE SET payload = excluded.payload, version = excluded.version`,
			d.Type, d.ID, *d.Payload, newVersion)
		return wrapf("write document payload", err)
	}
	_, err := s.docWriteDB.ExecContext(ctx, "DELETE FROM documents_payload WHERE type = ? AND id = ?", d.Type, d.ID)
	return wrapf("delete document payload", err)
}
