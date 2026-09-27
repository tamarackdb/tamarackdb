package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// VersionHeader carries a projection's version in a
// GET /projections/{type}/{id} response.
const VersionHeader = "X-Tamarackdb-Version"

// projectionsResponse is POST /projections's response: the new version of
// every created and replaced projection, in request order.
type projectionsResponse struct {
	Create  []projectionVersion `json:"create"`
	Replace []projectionVersion `json:"replace"`
}

type projectionVersion struct {
	Version string `json:"version"`
}

func toProjectionVersions(versions []string) []projectionVersion {
	out := make([]projectionVersion, len(versions))
	for i, v := range versions {
		out[i] = projectionVersion{Version: v}
	}
	return out
}

// handleGetProjection implements GET /projections/{type}/{id}: 404 when no
// projection exists, or 200 with the payload as the response body and the
// version in the X-Tamarackdb-Version header. With a
// ticket, the read runs inside the transaction and sees projections written
// earlier in it; a 404 is an ordinary answer there, not a failure, and the
// transaction goes on. Without a ticket, it sees committed projections only.
// The payload is returned as-is: its own format (JSON, XML, plain text) is
// up to the writing application, the store never parses it.
func (s *Server) handleGetProjection(w http.ResponseWriter, r *http.Request) {
	typ, id := r.PathValue("type"), r.PathValue("id")

	var version, payload string
	var found bool
	var err error
	if ticket, ok := ticketFrom(r); ok {
		defer s.trackWrite()()
		err = s.doInTx(w, ticket, func(tx *store.Tx) error {
			version, payload, found, err = tx.GetProjection(r.Context(), typ, id)
			return err
		})
	} else {
		s.readHTTPOpen.Add(1)
		defer s.readHTTPOpen.Add(-1)
		version, payload, found, err = s.st.GetProjection(r.Context(), typ, id)
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
	w.Header().Set(VersionHeader, version)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(payload))
}

// handleWriteProjections implements POST /projections: it creates,
// replaces, and deletes projections, and responds with the new versions.
// With a ticket, the write runs inside the transaction, and any failure,
// a version conflict included, rolls it back. Without a ticket, it's
// accepted only while the server is paused, for a projection rebuild, and
// commits on its own; outside a pause it gets 409 NotPaused.
func (s *Server) handleWriteProjections(w http.ResponseWriter, r *http.Request) {
	defer s.trackWrite()()

	parse := func() (projection.Writes, error) {
		var req projection.Writes
		if err := decodeJSONStrict(r, &req); err != nil {
			return projection.Writes{}, err
		}
		err := validateProjectionsRequest(req, s.opts.MaxProjectionSize, s.opts.MaxProjectionsPerRequest)
		return req, err
	}

	var versions store.Versions
	var err error
	if ticket, ok := ticketFrom(r); ok {
		// Inside the transaction, so a malformed body rolls it back like
		// any other failed call.
		err = s.doInTx(w, ticket, func(tx *store.Tx) error {
			req, err := parse()
			if err != nil {
				return err
			}
			versions, err = tx.WriteProjections(r.Context(), req)
			return err
		})
	} else {
		// The body is read before RunPaused: the pause can't end while
		// RunPaused runs, so a client sending its body slowly would
		// otherwise hold off POST /resume for as long as it likes.
		var req projection.Writes
		if req, err = parse(); err == nil {
			err = s.tm.RunPaused(func() error {
				var err error
				versions, err = s.st.WriteProjections(r.Context(), req)
				return err
			})
		}
	}
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(projectionsResponse{
		Create:  toProjectionVersions(versions.Create),
		Replace: toProjectionVersions(versions.Replace),
	})
}

// validateProjectionsRequest checks request-shape rules (at least one of
// create, replace, delete, and at most maxProjectionsPerRequest
// projections across them), then, per projection, its own Validate() and
// its size limit, rejecting a type+id pair that appears more than once
// across the three lists. Every message names the list and index.
func validateProjectionsRequest(req projection.Writes, maxProjectionSize, maxProjectionsPerRequest int) error {
	// Every key is optional, but a body with none of them most likely
	// misspells them all. Empty lists are fine: a command whose event
	// handlers changed no projection can still send its usual call.
	if req.Create == nil && req.Replace == nil && req.Delete == nil {
		return &dcb.ValidationError{Err: errNoProjectionWrites, Message: "request carries none of create, replace, delete"}
	}
	if n := req.Len(); n > maxProjectionsPerRequest {
		return &dcb.ValidationError{Err: errTooManyProjections, Message: fmt.Sprintf(
			"request carries %d projections, more than the maximum of %d", n, maxProjectionsPerRequest)}
	}
	seen := make(map[[2]string]string, req.Len())
	check := func(op string, i int, typ, id string, payload *string, validate func() error) error {
		if err := validate(); err != nil {
			var ve *projection.ValidationError
			if errors.As(err, &ve) {
				return &projection.ValidationError{Err: ve.Err, Message: fmt.Sprintf("%s[%d]: %s", op, i, ve.Message)}
			}
			return err
		}
		key := [2]string{typ, id}
		at := fmt.Sprintf("%s[%d]", op, i)
		if first, dup := seen[key]; dup {
			return &dcb.ValidationError{Err: errDuplicateProjectionKey, Message: fmt.Sprintf(
				"%s has the same type and id as %s", at, first)}
		}
		seen[key] = at
		if payload != nil {
			if size := len(*payload); size > maxProjectionSize {
				return &oversizeError{kind: "projection in " + op, index: i, size: size, max: maxProjectionSize}
			}
		}
		return nil
	}
	for i, c := range req.Create {
		if err := check("create", i, c.Type, c.ID, c.Payload, c.Validate); err != nil {
			return err
		}
	}
	for i, rp := range req.Replace {
		if err := check("replace", i, rp.Type, rp.ID, rp.Payload, rp.Validate); err != nil {
			return err
		}
	}
	for i, d := range req.Delete {
		if err := check("delete", i, d.Type, d.ID, nil, d.Validate); err != nil {
			return err
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
