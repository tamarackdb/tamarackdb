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

// Projection is one upsert: the whole new payload of a projection.
type Projection struct {
	Key
	Payload string
}

// projectionState is what a transaction knows of one projection: how it
// was when first read, and how it is now in the transaction.
type projectionState struct {
	readFound   bool   // it existed when first read
	readVersion string // its version then, if it existed
	found       bool   // it exists now, in the transaction
	payload     string // its payload now, if it exists
	written     bool   // the transaction wrote it at least once
}

// GetProjection reads a projection. The first read in a transaction reads
// the store, and remembers the version read, or that the projection
// doesn't exist. A later read returns the projection as the transaction
// left it. found is false when the projection doesn't exist, or the
// transaction deleted it.
func (r *Registry) GetProjection(ctx context.Context, id string, key Key) (payload string, found bool, err error) {
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

// WriteProjections upserts and deletes projections the transaction has
// read. Deleting a projection that doesn't exist does nothing. It returns
// the time of the write, by the server's clock, for a response shaped
// like WriteEvents'.
func (r *Registry) WriteProjections(id string, upsert []Projection, del []Key) (time.Time, error) {
	var now time.Time
	err := r.do(id, func(t *transaction) error {
		if t.open != nil {
			return designError("projections written while a condition is open: write the events of the last read first, or an empty list")
		}
		if len(upsert) == 0 && len(del) == 0 {
			return designError("a write of projections must upsert or delete at least one projection")
		}
		seen := make(map[Key]bool, len(upsert)+len(del))
		check := func(k Key) error {
			if seen[k] {
				return designError(fmt.Sprintf("projection %s appears twice in one write", k))
			}
			seen[k] = true
			if t.projections[k] == nil {
				return designError(fmt.Sprintf("projection %s written without being read in this transaction: read it first", k))
			}
			return nil
		}
		for _, p := range upsert {
			if err := check(p.Key); err != nil {
				return err
			}
		}
		for _, k := range del {
			if err := check(k); err != nil {
				return err
			}
		}
		// A projection written again counts once.
		n := t.written
		for k := range seen {
			if !t.projections[k].written {
				n++
			}
		}
		if n > r.cfg.MaxProjectionsPerTx {
			return tooLarge(fmt.Sprintf("write %d projections", n), "maxProjectionsPerTx", r.cfg.MaxProjectionsPerTx)
		}
		t.written = n
		for _, p := range upsert {
			s := t.projections[p.Key]
			s.found, s.payload, s.written = true, p.Payload, true
		}
		for _, k := range del {
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
// projection, to name it in a conflict.
type opKeys struct {
	create, replace, delete []Key
}

func (k opKeys) conflict(pe *store.ProjectionConflictError) error {
	switch pe.Op {
	case "create":
		return &ConflictError{Message: fmt.Sprintf("projection %s was created by another write since it was read", k.create[pe.Index])}
	case "replace":
		return &ConflictError{Message: fmt.Sprintf("projection %s no longer has the version read", k.replace[pe.Index])}
	default:
		return &ConflictError{Message: fmt.Sprintf("projection %s no longer has the version read", k.delete[pe.Index])}
	}
}
