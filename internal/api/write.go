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

// writeRequest is POST /write's body: everything one write carries. Every
// key is optional, and a missing one is an empty list. The body is decoded
// strictly, so a misspelled key gets 400 instead of being dropped.
type writeRequest struct {
	Events      []dcb.EventData       `json:"events"`
	Conditions  []dcb.AppendCondition `json:"conditions"`
	Projections projection.Writes     `json:"projections"`
}

// writeResponse is POST /write's response: each event's Sequence Position
// and time, and the new version of each created and replaced projection,
// in request order. The store ID goes in the X-Tamarackdb-Store header.
type writeResponse struct {
	Events      []appendedEvent     `json:"events"`
	Projections projectionsResponse `json:"projections"`
}

// handleWrite implements POST /write: it checks every Append Condition,
// then appends the events and writes the projections, all in one SQLite
// transaction of its own, or nothing at all. The body is read and
// validated before the request joins the FIFO: a client sending its body
// slowly would otherwise hold the turn, and every request behind it, for
// as long as it likes.
func (s *Server) handleWrite(w http.ResponseWriter, r *http.Request) {
	defer s.trackWrite()()

	var req writeRequest
	err := decodeJSONStrict(r, &req)
	if err == nil {
		err = s.validateWriteRequest(req)
	}
	var result store.AppendResult
	if err == nil {
		if len(req.Events) == 0 && len(req.Conditions) == 0 && req.Projections.Len() == 0 {
			// Nothing to check or write: Append returns the store ID at
			// once, without the write connection, so there's no turn to
			// wait for.
			result, err = s.st.Append(r.Context(), nil, nil, projection.Writes{})
		} else {
			result, err = s.tm.Write(r.Context(), req.Events, req.Conditions, req.Projections)
		}
	}
	if err != nil {
		var pe *store.ProjectionConflictError
		if errors.As(err, &pe) {
			err = &nestedError{path: "projections.", err: err}
		}
		s.handleErr(w, r, err)
		return
	}

	resp := writeResponse{
		Events: make([]appendedEvent, len(result.Events)),
		Projections: projectionsResponse{
			Create:  toProjectionVersions(result.Versions.Create),
			Replace: toProjectionVersions(result.Versions.Replace),
		},
	}
	for i, ev := range result.Events {
		resp.Events[i] = appendedEvent{Sequence: ev.Sequence, Time: ev.Time.UTC().Format(dcb.TimeLayout)}
	}
	w.Header().Set(StoreHeader, result.StoreID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// validateWriteRequest checks the counts against MaxEventsPerWrite and
// MaxProjectionsPerWrite, then the events, the conditions, and the
// projections. Every message names the setting to raise, or the item at
// fault as the request spells it.
func (s *Server) validateWriteRequest(req writeRequest) error {
	if n := len(req.Events); n > s.opts.MaxEventsPerWrite {
		return &dcb.ValidationError{Err: errTooManyEvents, Message: fmt.Sprintf(
			"request carries %d events, more than maxEventsPerWrite (%d)", n, s.opts.MaxEventsPerWrite)}
	}
	// A transaction usually has one condition per decision, each with
	// events of its own, so the event limit bounds conditions too.
	if n := len(req.Conditions); n > s.opts.MaxEventsPerWrite {
		return &dcb.ValidationError{Err: errTooManyConditions, Message: fmt.Sprintf(
			"request carries %d conditions, more than maxEventsPerWrite (%d)", n, s.opts.MaxEventsPerWrite)}
	}
	if n := req.Projections.Len(); n > s.opts.MaxProjectionsPerWrite {
		return &dcb.ValidationError{Err: errTooManyProjections, Message: fmt.Sprintf(
			"request carries %d projections, more than maxProjectionsPerWrite (%d)", n, s.opts.MaxProjectionsPerWrite)}
	}
	if err := validateEvents(req.Events, s.opts.MaxEventSize); err != nil {
		return err
	}
	for i, c := range req.Conditions {
		at := fmt.Sprintf("conditions[%d]", i)
		if err := c.Validate(); err != nil {
			return prefixed(at, err)
		}
		if err := c.ValidateStore(); err != nil {
			return prefixed(at, err)
		}
	}
	return validateProjections(req.Projections, s.opts.MaxProjectionSize, "projections.")
}

// trackWrite counts a request on the write side for GET /debug. Callers
// defer the returned function.
func (s *Server) trackWrite() func() {
	s.writeHTTPOpen.Add(1)
	return func() { s.writeHTTPOpen.Add(-1) }
}
