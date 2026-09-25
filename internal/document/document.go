// Package document defines the shape of a TamarackDB document: a
// projection (type+id, opaque payload) written atomically alongside
// events, and read through GET /documents/{type}/{id}. It does not reuse
// dcb's types: a document has no matching predicate and no condition,
// just a key and a payload.
package document

import "errors"

// Data is one document as carried in a write request's documents
// field: an upsert or a deletion.
//
// Payload nil (absent, or JSON null) means "delete this document";
// deleting a document that doesn't exist does nothing. Non-nil means
// create the document, or replace it if it exists.
type Data struct {
	Type    string  `json:"type"`
	ID      string  `json:"id"`
	Payload *string `json:"payload,omitempty"`
}

// Validate checks the domain rules that apply to a single document
// regardless of the rest of the write request: a non-empty Type and ID.
func (d Data) Validate() error {
	if d.Type == "" {
		return &ValidationError{Err: ErrMissingType, Message: "document is missing its type"}
	}
	if d.ID == "" {
		return &ValidationError{Err: ErrMissingID, Message: "document is missing its id"}
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
	ErrMissingType = errors.New("document is missing its type")
	ErrMissingID   = errors.New("document is missing its id")
)
