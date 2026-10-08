package tx

import (
	"context"
	"fmt"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// Key names one projection.
type Key struct {
	Type string
	ID   string
}

func (k Key) String() string { return k.Type + "/" + k.ID }

// Projection is one create or replace: the whole new payload of a
// projection.
type Projection struct {
	Key
	Payload string
}

// Writes is one write of projections in a transaction: projections to
// create, to replace, and to delete. None carries a version: the
// transaction knows the version of each projection it read.
type Writes struct {
	Create  []Projection
	Replace []Projection
	Delete  []Key
}

// Len returns the number of projections across the three lists.
func (w Writes) Len() int { return len(w.Create) + len(w.Replace) + len(w.Delete) }

// projectionState is what a transaction knows of one projection: how it
// was when first read, and how it is now in the transaction. A projection
// created without a read is taken as absent when first read, and the
// commit checks it still is.
type projectionState struct {
	readFound   bool   // it existed when first read
	readVersion string // its version then, if it existed
	unread      bool   // created without a read
	found       bool   // it exists now, in the transaction
	payload     string // its payload now, if it exists
	written     bool   // the transaction wrote it at least once
}

// GetProjection reads a projection. The first read in a transaction reads
// the store, and remembers the version read, or that the projection
// doesn't exist. A later read returns the projection as the transaction
// left it. found is false when the projection doesn't exist, or the
// transaction deleted it. As in ReadEvents, the read runs with ctx
// without its cancellation: a client that leaves MUST NOT end its
// transaction.
func (r *Registry) GetProjection(ctx context.Context, id string, key Key) (payload string, found bool, err error) {
	ctx = context.WithoutCancel(ctx)
	err = r.do(id, func(t *transaction) error {
		if t.open != nil {
			return designError("projection read while a condition is open: write the events of the last read first, or an empty list")
		}
		p, ok := t.projections[key]
		if !ok {
			read, err := r.st.GetProjection(ctx, key.Type, key.ID)
			if err != nil {
				return err
			}
			p = &projectionState{
				readFound:   read.Found,
				readVersion: read.Version,
				found:       read.Found,
				payload:     read.Payload,
			}
			t.projections[key] = p
			t.touched = append(t.touched, key)
		}
		payload, found = p.payload, p.found
		return nil
	})
	return payload, found, err
}

// WriteProjections creates, replaces, and deletes projections. A create
// needs no read: an application that creates a projection knows it
// doesn't exist, and the commit checks it. A replace or a delete needs a
// read, or a create earlier in the transaction, since the commit checks
// the version read. Deleting a projection that doesn't exist does
// nothing. It returns the time of the write, by the server's clock, for a
// response shaped like WriteEvents'.
func (r *Registry) WriteProjections(id string, w Writes) (time.Time, error) {
	var now time.Time
	err := r.do(id, func(t *transaction) error {
		if t.open != nil {
			return designError("projections written while a condition is open: write the events of the last read first, or an empty list")
		}
		if w.Len() == 0 {
			return designError("a write of projections must create, replace, or delete at least one projection")
		}
		seen := make(map[Key]bool, w.Len())
		once := func(k Key) error {
			if seen[k] {
				return designError(fmt.Sprintf("projection %s appears twice in one write", k))
			}
			seen[k] = true
			return nil
		}
		readFirst := func(k Key) error {
			if t.projections[k] == nil {
				return designError(fmt.Sprintf("projection %s replaced or deleted without being read in this transaction: read it first", k))
			}
			return nil
		}
		for _, p := range w.Create {
			if err := once(p.Key); err != nil {
				return err
			}
			if s := t.projections[p.Key]; s != nil && s.found {
				return designError(fmt.Sprintf("projection %s created while it exists in this transaction: replace it", p.Key))
			}
		}
		for _, p := range w.Replace {
			if err := once(p.Key); err != nil {
				return err
			}
			if err := readFirst(p.Key); err != nil {
				return err
			}
			if !t.projections[p.Key].found {
				return designError(fmt.Sprintf("projection %s replaced while it doesn't exist in this transaction: create it", p.Key))
			}
		}
		for _, k := range w.Delete {
			if err := once(k); err != nil {
				return err
			}
			if err := readFirst(k); err != nil {
				return err
			}
		}
		// A projection written again counts once.
		n := t.written
		for k := range seen {
			if s := t.projections[k]; s == nil || !s.written {
				n++
			}
		}
		if n > r.cfg.MaxProjectionsPerTx {
			return tooLarge(fmt.Sprintf("write %d projections", n), "maxProjectionsPerTx", r.cfg.MaxProjectionsPerTx)
		}
		t.written = n
		for _, p := range w.Create {
			s := t.projections[p.Key]
			if s == nil {
				s = &projectionState{unread: true}
				t.projections[p.Key] = s
				t.touched = append(t.touched, p.Key)
			}
			s.found, s.payload, s.written = true, p.Payload, true
		}
		for _, p := range w.Replace {
			s := t.projections[p.Key]
			s.payload, s.written = p.Payload, true
		}
		for _, k := range w.Delete {
			s := t.projections[k]
			s.found, s.payload, s.written = false, "", true
		}
		now = dcb.Now()
		return nil
	})
	return now, err
}

// netProjections turns what the transaction did to each projection into
// one store operation: create, replace, or delete, at the version read.
// A projection only read, or back where it started from absent, needs
// none.
func (t *transaction) netProjections() (projection.Writes, opKeys) {
	var w projection.Writes
	var keys opKeys
	for _, k := range t.touched {
		s := t.projections[k]
		if !s.written {
			continue
		}
		switch {
		case !s.readFound && s.found:
			payload := s.payload
			w.Create = append(w.Create, projection.Create{Type: k.Type, ID: k.ID, Payload: &payload})
			keys.create = append(keys.create, k)
			keys.unread = append(keys.unread, s.unread)
		case s.readFound && s.found:
			payload := s.payload
			w.Replace = append(w.Replace, projection.Replace{Type: k.Type, ID: k.ID, Version: s.readVersion, Payload: &payload})
			keys.replace = append(keys.replace, k)
		case s.readFound && !s.found:
			w.Delete = append(w.Delete, projection.Delete{Type: k.Type, ID: k.ID, Version: s.readVersion})
			keys.delete = append(keys.delete, k)
		}
	}
	return w, keys
}

// opKeys maps each store operation of netProjections back to its
// projection, to name it in a conflict. unread says, for each create,
// whether its projection was created without a read.
type opKeys struct {
	create, replace, delete []Key
	unread                  []bool
}

func (k opKeys) conflict(pe *store.ProjectionConflictError) error {
	switch pe.Op {
	case "create":
		if k.unread[pe.Index] {
			return &ConflictError{Message: fmt.Sprintf("projection %s already exists", k.create[pe.Index])}
		}
		return &ConflictError{Message: fmt.Sprintf("projection %s was created by another write since it was read", k.create[pe.Index])}
	case "replace":
		return &ConflictError{Message: fmt.Sprintf("projection %s no longer has the version read", k.replace[pe.Index])}
	default:
		return &ConflictError{Message: fmt.Sprintf("projection %s no longer has the version read", k.delete[pe.Index])}
	}
}
