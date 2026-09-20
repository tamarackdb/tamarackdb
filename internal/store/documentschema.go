package store

import (
	"context"
	"database/sql"
	"fmt"
)

// documentsSchemaVersion tracks tamarackdb-documents.sqlite's own
// PRAGMA user_version, independent of schemaVersion (the events file's
// version): the two files are never migrated together.
const documentsSchemaVersion = 1

const documentsSchemaDDL = `
CREATE TABLE documents_payload (
    type    TEXT NOT NULL,
    id      TEXT NOT NULL,
    payload TEXT NOT NULL,
    version INTEGER NOT NULL,
    PRIMARY KEY (type, id)
) WITHOUT ROWID;
`

func ensureDocumentsSchema(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return wrapf("read documents schema version", err)
	}
	switch version {
	case documentsSchemaVersion:
		return nil
	case 0: // brand-new file: SQLite's own default for user_version
		return createDocumentsSchema(ctx, db)
	default:
		return &SchemaVersionError{Found: version, Want: documentsSchemaVersion}
	}
}

func createDocumentsSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrapf("begin documents schema creation", err)
	}
	defer tx.Rollback() // no-op after Commit

	if _, err := tx.ExecContext(ctx, documentsSchemaDDL); err != nil {
		return wrapf("create documents schema", err)
	}
	// PRAGMA doesn't accept bound parameters; documentsSchemaVersion is a
	// compile-time constant, never untrusted input.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", documentsSchemaVersion)); err != nil {
		return wrapf("stamp documents schema version", err)
	}
	return wrapf("commit documents schema creation", tx.Commit())
}
