package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// timeLayout mirrors dcb.Event's own wire format exactly (ATOM/RFC3339,
// fixed microsecond precision, UTC) — duplicated here because dcb keeps
// its layout unexported and appendResponse is intentionally its own,
// smaller shape (sequence+time only), not a reuse of dcb.Event's
// MarshalJSON.
const timeLayout = "2006-01-02T15:04:05.000000Z07:00"

type appendRequest struct {
	Events    []dcb.EventData      `json:"events"`
	Condition *dcb.AppendCondition `json:"condition,omitempty"`
}

type appendResponse struct {
	Events []appendedEvent `json:"events"`
}

type appendedEvent struct {
	Sequence int64  `json:"sequence"`
	Time     string `json:"time"`
}

// oversizeError is returned by validateAppendRequest's per-event size
// check (dcb.EventData.Size() against Options.MaxEventSize). This rule
// lives at the HTTP layer, not in internal/dcb, since the limit itself is
// configuration, not a dcb domain rule.
type oversizeError struct {
	index, size, max int
}

func (e *oversizeError) Error() string {
	return fmt.Sprintf("event at index %d is %d bytes, exceeding the configured maximum of %d bytes", e.index, e.size, e.max)
}

func (s *Server) handleAppend(w http.ResponseWriter, r *http.Request) {
	var req appendRequest
	if err := decodeJSON(r, &req); err != nil {
		s.handleErr(w, r, err)
		return
	}
	if err := validateAppendRequest(req, s.opts.MaxEventSize); err != nil {
		s.handleErr(w, r, err)
		return
	}

	// No condition key in the request => the zero-value
	// dcb.AppendCondition{} (both fields nil). This matches
	// internal/gatekeeper's own "no invariant to protect" handling
	// exactly.
	condition := dcb.AppendCondition{}
	if req.Condition != nil {
		condition = *req.Condition
	}

	res, err := s.gk.Acquire(r.Context(), condition, req.Events)
	if err != nil {
		s.handleErr(w, r, err) // ctx cancelled/timed out while queued, or gatekeeper.ErrClosed
		return
	}
	defer res.Release()

	events, err := s.st.Append(r.Context(), req.Events, req.Condition)
	if err != nil {
		s.handleErr(w, r, err) // store.ErrConcurrencyConflict -> 409, etc.
		return
	}

	resp := appendResponse{Events: make([]appendedEvent, len(events))}
	for i, ev := range events {
		resp.Events[i] = appendedEvent{Sequence: ev.Sequence, Time: ev.Time.UTC().Format(timeLayout)}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// validateAppendRequest checks request-shape/HTTP-layer rules (non-empty,
// <= dcb.MaxEventsPerAppend) then, per event and in order,
// dcb.EventData.Validate() (fail-fast on the first domain violation) and
// the configured size limit — checked independently of Validate, since
// size isn't a dcb domain rule. Finally, condition.Validate() if a
// condition was given.
func validateAppendRequest(req appendRequest, maxEventSize int) error {
	if len(req.Events) == 0 {
		// Not explicitly forbidden, but a zero-event append
		// is meaningless and never legitimately needed by a real client;
		// rejecting it early avoids running it through the
		// gatekeeper/store pipeline at all.
		return &dcb.ValidationError{Err: errEmptyAppend, Message: "events must be a non-empty array"}
	}
	if len(req.Events) > dcb.MaxEventsPerAppend {
		return &dcb.ValidationError{Err: errTooManyEvents, Message: fmt.Sprintf(
			"append carries %d events, more than the maximum of %d", len(req.Events), dcb.MaxEventsPerAppend)}
	}
	for i, ev := range req.Events {
		if err := ev.Validate(); err != nil {
			return err
		}
		if size := ev.Size(); size > maxEventSize {
			return &oversizeError{index: i, size: size, max: maxEventSize}
		}
	}
	if req.Condition != nil {
		if err := req.Condition.Validate(); err != nil {
			return err
		}
	}
	return nil
}
