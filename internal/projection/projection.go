// Package projection defines the shape of a TamarackDB projection: the
// current state computed from events by a projector, identified by
// type+id with an opaque payload, written atomically alongside events, and
// read through GET /projections/{type}/{id}. It does not reuse
// dcb's types: a projection has no matching predicate and no condition,
// just a key and a payload.
package projection

import "errors"

// Data is one projection as carried in a write request's projections
// field: an upsert or a deletion.
//
// Delete true means "delete this projection"; deleting a projection that
// doesn't exist does nothing. Otherwise Payload creates the projection, or
// replaces it if it exists. Payload is a pointer so that a missing or null
// payload can be told apart from an empty string and rejected: a key that
// went missing on the client must never delete data.
type Data struct {
	Type    string  `json:"type"`
	ID      string  `json:"id"`
	Payload *string `json:"payload,omitempty"`
	Delete  bool    `json:"delete,omitempty"`
}

// Validate checks the domain rules that apply to a single projection
// regardless of the rest of the write request: a non-empty Type and ID,
// and a payload exactly when the projection isn't being deleted.
func (d Data) Validate() error {
	if d.Type == "" {
		return &ValidationError{Err: ErrMissingType, Message: "projection is missing its type"}
	}
	if d.ID == "" {
		return &ValidationError{Err: ErrMissingID, Message: "projection is missing its id"}
	}
	if d.Delete && d.Payload != nil {
		return &ValidationError{Err: ErrPayloadOnDelete, Message: "projection has both delete and a payload"}
	}
	if !d.Delete && d.Payload == nil {
		return &ValidationError{Err: ErrMissingPayload, Message: "projection is missing its payload"}
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
	ErrMissingType     = errors.New("projection is missing its type")
	ErrMissingID       = errors.New("projection is missing its id")
	ErrMissingPayload  = errors.New("projection is missing its payload")
	ErrPayloadOnDelete = errors.New("projection has both delete and a payload")
)
