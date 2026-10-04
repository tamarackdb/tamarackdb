// Package projection defines the shape of a TamarackDB projection: the
// current state computed from events by a projector, identified by
// type+id with an opaque payload, written atomically alongside events, and
// read through GET /projections/{type}/{id}. It does not reuse dcb's
// types: a projection has no matching predicate and no condition, just a
// key, a version, and a payload.
package projection

import "errors"

// Writes is the projections object of a POST /write request, and what the
// store writes: projections to create, to replace, and to delete. The same
// type+id appears at most once across the three lists, so the order
// between them doesn't matter.
//
// Every projection carries a version: a random UUID, new on each write. A
// replace or a delete must carry the version it read, and fails if the
// stored projection has moved on since; a create fails if the projection
// already exists. A nil list means the key was absent from the request.
type Writes struct {
	Create  []Create  `json:"create"`
	Replace []Replace `json:"replace"`
	Delete  []Delete  `json:"delete"`
}

// Len returns the number of projections across the three lists.
func (w Writes) Len() int { return len(w.Create) + len(w.Replace) + len(w.Delete) }

// Create is a projection to create. It must not exist yet.
//
// Payload is a pointer so that a missing or null payload can be told
// apart from an empty string and rejected: a key that went missing on the
// client must never be written as an empty payload.
type Create struct {
	Type    string  `json:"type"`
	ID      string  `json:"id"`
	Payload *string `json:"payload"`
}

// Replace is a projection to replace. Version is the one the client read;
// the write fails if the stored projection no longer has it.
type Replace struct {
	Type    string  `json:"type"`
	ID      string  `json:"id"`
	Version string  `json:"version"`
	Payload *string `json:"payload"`
}

// Key names a projection to delete in a transaction, where the server
// knows the version read.
type Key struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (k Key) Validate() error { return validateKey(k.Type, k.ID) }

// Delete is a projection to delete. Version is the one the client read;
// the delete fails if the stored projection no longer has it, or no longer
// exists.
type Delete struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Version string `json:"version"`
}

// Validate checks that c has a type, an id, and a payload.
func (c Create) Validate() error {
	if err := validateKey(c.Type, c.ID); err != nil {
		return err
	}
	return validatePayload(c.Payload)
}

// Validate checks that r has a type, an id, a version, and a payload.
func (r Replace) Validate() error {
	if err := validateKey(r.Type, r.ID); err != nil {
		return err
	}
	if err := validateVersion(r.Version); err != nil {
		return err
	}
	return validatePayload(r.Payload)
}

// Validate checks that d has a type, an id, and a version.
func (d Delete) Validate() error {
	if err := validateKey(d.Type, d.ID); err != nil {
		return err
	}
	return validateVersion(d.Version)
}

func validateKey(typ, id string) error {
	if typ == "" {
		return &ValidationError{Err: ErrMissingType, Message: "projection is missing its type"}
	}
	if id == "" {
		return &ValidationError{Err: ErrMissingID, Message: "projection is missing its id"}
	}
	return nil
}

func validateVersion(version string) error {
	if version == "" {
		return &ValidationError{Err: ErrMissingVersion, Message: "projection is missing its version"}
	}
	return nil
}

func validatePayload(payload *string) error {
	if payload == nil {
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
	ErrMissingType    = errors.New("projection is missing its type")
	ErrMissingID      = errors.New("projection is missing its id")
	ErrMissingPayload = errors.New("projection is missing its payload")
	ErrMissingVersion = errors.New("projection is missing its version")
)
