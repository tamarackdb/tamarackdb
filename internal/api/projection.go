package api

import (
	"fmt"
	"net/http"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

type projectionsRequest struct {
	Projections []projection.Data `json:"projections"`
}

// handleGetProjection implements GET /projections/{type}/{id}: 404 when no
// projection exists, or 200 with the payload as the response body. With a
// ticket, the read runs inside the transaction and sees projections written
// earlier in it; a 404 is an ordinary answer there, not a failure, and the
// transaction goes on. Without a ticket, it sees committed projections only.
// The payload is returned as-is: its own format (JSON, XML, plain text) is
// up to the writing application, the store never parses it.
func (s *Server) handleGetProjection(w http.ResponseWriter, r *http.Request) {
	typ, id := r.PathValue("type"), r.PathValue("id")

	var payload string
	var found bool
	var err error
	if ticket, ok := ticketFrom(r); ok {
		defer s.trackWrite()()
		err = s.doInTx(w, ticket, func(tx *store.Tx) error {
			payload, found, err = tx.GetProjection(r.Context(), typ, id)
			return err
		})
	} else {
		s.readHTTPOpen.Add(1)
		defer s.readHTTPOpen.Add(-1)
		payload, found, err = s.st.GetProjection(r.Context(), typ, id)
	}
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "ProjectionNotFound", "")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(payload))
}

// handleWriteProjections implements POST /projections: it creates, replaces,
// or deletes projections. With a ticket, the write runs inside the
// transaction, and any failure rolls it back. Without a ticket, it's
// accepted only while the server is paused, for a projection rebuild, and
// commits on its own; outside a pause it gets 409 NotPaused.
func (s *Server) handleWriteProjections(w http.ResponseWriter, r *http.Request) {
	defer s.trackWrite()()

	write := func(writeFn func([]projection.Data) error) error {
		var req projectionsRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if err := validateProjectionsRequest(req, s.opts.MaxProjectionSize, s.opts.MaxProjectionsPerRequest); err != nil {
			return err
		}
		return writeFn(req.Projections)
	}

	var err error
	if ticket, ok := ticketFrom(r); ok {
		err = s.doInTx(w, ticket, func(tx *store.Tx) error {
			return write(func(docs []projection.Data) error { return tx.WriteProjections(r.Context(), docs) })
		})
	} else {
		err = s.tm.RunPaused(func() error {
			return write(func(docs []projection.Data) error { return s.st.WriteProjections(r.Context(), docs) })
		})
	}
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateProjectionsRequest checks request-shape rules (between 1 and
// maxProjectionsPerRequest projections), then, per projection,
// projection.Data.Validate() and its size limit, rejecting a repeated
// type+id pair within the same request rather than leaving its outcome to
// write order.
func validateProjectionsRequest(req projectionsRequest, maxProjectionSize, maxProjectionsPerRequest int) error {
	if len(req.Projections) == 0 {
		return &dcb.ValidationError{Err: errNoProjections, Message: "request must carry at least one projection"}
	}
	if len(req.Projections) > maxProjectionsPerRequest {
		return &dcb.ValidationError{Err: errTooManyProjections, Message: fmt.Sprintf(
			"request carries %d projections, more than the maximum of %d", len(req.Projections), maxProjectionsPerRequest)}
	}
	seen := make(map[[2]string]struct{}, len(req.Projections))
	for i, d := range req.Projections {
		if err := d.Validate(); err != nil {
			return err
		}
		key := [2]string{d.Type, d.ID}
		if _, dup := seen[key]; dup {
			return &dcb.ValidationError{Err: errDuplicateProjectionKey, Message: fmt.Sprintf(
				"projection at index %d has the same type and id as an earlier entry in this request", i)}
		}
		seen[key] = struct{}{}
		if d.Payload != nil {
			if size := len(*d.Payload); size > maxProjectionSize {
				return &oversizeError{kind: "projection", index: i, size: size, max: maxProjectionSize}
			}
		}
	}
	return nil
}

// handleDeleteProjectionsByType implements DELETE /projections/{type}: a bulk
// delete of every projection of that type, for a projection rebuild. It's
// accepted only while the server is paused.
func (s *Server) handleDeleteProjectionsByType(w http.ResponseWriter, r *http.Request) {
	defer s.trackWrite()()
	if err := s.tm.RunPaused(func() error {
		return s.st.DeleteProjectionsByType(r.Context(), r.PathValue("type"))
	}); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteAllProjections implements DELETE /projections: the same bulk
// delete as handleDeleteProjectionsByType, widened to every type at once.
func (s *Server) handleDeleteAllProjections(w http.ResponseWriter, r *http.Request) {
	defer s.trackWrite()()
	if err := s.tm.RunPaused(func() error {
		return s.st.DeleteAllProjections(r.Context())
	}); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
