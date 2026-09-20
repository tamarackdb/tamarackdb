// Package document defines the shape of a TamarackDB document: a
// versioned projection (type+id, opaque payload) written atomically
// alongside events through POST /write, and read through
// GET /documents/{type}/{id}. It does not reuse dcb's types: a document
// has no matching predicate and no condition, just a key and a version.
package document

import (
	"errors"
	"fmt"
)

// Data is one document as carried in a /write request's documents
// field: an upsert or a deletion, always versioned.
//
// Payload nil (absent, or JSON null) means "delete this document";
// non-nil means upsert.
//
// Version nil means the app never read this document and knows it's
// new: the store creates it at version 1. Version non-nil is the
// version the app read; an upsert lands at Version+1, a deletion
// requires the current version to match exactly — either way, a
// mismatch is a conflict, not a silent no-op.
type Data struct {
	Type    string  `json:"type"`
	ID      string  `json:"id"`
	Payload *string `json:"payload,omitempty"`
	Version *int64  `json:"version,omitempty"`
}

// Validate checks the domain rules that apply to a single document
// regardless of the rest of the write request: non-empty Type/ID, a
// positive Version when present, and Version required whenever Payload
// is nil — a deletion always needs a known version; there is no
// unversioned deletion of a single document outside DELETE
// /documents/<type>.
func (d Data) Validate() error {
	if d.Type == "" {
		return &ValidationError{Err: ErrMissingType, Message: "document is missing its type"}
	}
	if d.ID == "" {
		return &ValidationError{Err: ErrMissingID, Message: "document is missing its id"}
	}
	if d.Version != nil && *d.Version < 1 {
		return &ValidationError{Err: ErrInvalidVersion, Message: fmt.Sprintf(
			"document version must be at least 1 when present, got %d", *d.Version)}
	}
	if d.Payload == nil && d.Version == nil {
		return &ValidationError{Err: ErrDeleteWithoutVersion, Message: "deleting a document requires a version"}
	}
	return nil
}

// ValidationError describes a domain-rule violation from this package.
// internal/api maps every *ValidationError, regardless of which
// sentinel it wraps, to a 400 {"error":"InvalidRequest","message":...}
// response, the same treatment dcb.ValidationError gets for events.
type ValidationError struct {
	Err     error
	Message string
}

func (e *ValidationError) Error() string { return e.Message }
func (e *ValidationError) Unwrap() error { return e.Err }

var (
	ErrMissingType          = errors.New("document is missing its type")
	ErrMissingID            = errors.New("document is missing its id")
	ErrInvalidVersion       = errors.New("document version must be at least 1 when present")
	ErrDeleteWithoutVersion = errors.New("deleting a document requires a version")
)
