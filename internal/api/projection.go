package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
)

// VersionHeader carries a projection's version in a
// GET /projections/{type}/{id} response.
const VersionHeader = "X-Tamarackdb-Version"

// projectionsResponse is the projections part of a POST /write response:
// the new version of every created and replaced projection, in request
// order.
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
// version in the X-Tamarackdb-Version header. It sees committed projections
// only, and both the 200 and the 404 carry the store ID in the
// X-Tamarackdb-Store header.
// The payload is returned as-is: its own format (JSON, XML, plain text) is
// up to the writing application, the store never parses it.
func (s *Server) handleGetProjection(w http.ResponseWriter, r *http.Request) {
	typ, id := r.PathValue("type"), r.PathValue("id")
	p, err := s.st.GetProjection(r.Context(), typ, id)
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.Header().Set(StoreHeader, p.StoreID)
	if !p.Found {
		writeError(w, http.StatusNotFound, "ProjectionNotFound", "")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set(VersionHeader, p.Version)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(p.Payload))
}

// validateProjections checks each projection: its own Validate(), then
// maxProjectionSize, rejecting a type+id pair that appears more than once
// across the three lists. A message names the projection as
// path + op[i], where path is where the lists sit in the request body.
func validateProjections(w projection.Writes, maxProjectionSize int, path string) error {
	seen := make(map[[2]string]string, w.Len())
	check := func(op string, i int, typ, id string, payload *string, validate func() error) error {
		at := fmt.Sprintf("%s%s[%d]", path, op, i)
		if err := validate(); err != nil {
			return prefixed(at, err)
		}
		key := [2]string{typ, id}
		if first, dup := seen[key]; dup {
			return &dcb.ValidationError{Err: errDuplicateProjectionKey, Message: fmt.Sprintf(
				"%s has the same type and id as %s", at, first)}
		}
		seen[key] = at
		// Measured like an event: every string the projection carries
		// counts, not just its payload, so type and id are bounded too.
		size := len(typ) + len(id)
		if payload != nil {
			size += len(*payload)
		}
		if size > maxProjectionSize {
			return &oversizeError{at: at, setting: "maxProjectionSize", size: size, max: maxProjectionSize}
		}
		return nil
	}
	for i, c := range w.Create {
		if err := check("create", i, c.Type, c.ID, c.Payload, c.Validate); err != nil {
			return err
		}
	}
	for i, rp := range w.Replace {
		if err := check("replace", i, rp.Type, rp.ID, rp.Payload, rp.Validate); err != nil {
			return err
		}
	}
	for i, d := range w.Delete {
		if err := check("delete", i, d.Type, d.ID, nil, d.Validate); err != nil {
			return err
		}
	}
	return nil
}

// handleDeleteProjectionsByType implements DELETE /projections/{type}: a bulk
// delete of every projection of that type, for a projection rebuild. It
// waits for its turn in the FIFO.
func (s *Server) handleDeleteProjectionsByType(w http.ResponseWriter, r *http.Request) {
	if err := s.wr.RunInTurn(r.Context(), func(ctx context.Context) error {
		return s.st.DeleteProjectionsByType(ctx, r.PathValue("type"))
	}); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteAllProjections implements DELETE /projections: the same bulk
// delete as handleDeleteProjectionsByType, widened to every type at once.
func (s *Server) handleDeleteAllProjections(w http.ResponseWriter, r *http.Request) {
	if err := s.wr.RunInTurn(r.Context(), func(ctx context.Context) error {
		return s.st.DeleteAllProjections(ctx)
	}); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
