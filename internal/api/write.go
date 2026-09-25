package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/document"
)

// timeLayout mirrors dcb.Event's own wire format exactly (ATOM/RFC3339,
// fixed microsecond precision, UTC). It is duplicated here because dcb keeps
// its layout unexported and appendedEvent is intentionally its own,
// smaller shape (sequence+time only), not a reuse of dcb.Event's
// MarshalJSON.
const timeLayout = "2006-01-02T15:04:05.000000Z07:00"

type writeRequest struct {
	Events    []dcb.EventData      `json:"events"`
	Condition *dcb.AppendCondition `json:"condition,omitempty"`
	Documents []document.Data      `json:"documents,omitempty"`
}

type writeResponse struct {
	Events []appendedEvent `json:"events"`
}

type appendedEvent struct {
	Sequence int64  `json:"sequence"`
	Time     string `json:"time"`
}

// oversizeError is returned by validateWriteRequest's per-event and
// per-document size checks (dcb.EventData.Size() against
// Options.MaxEventSize, or a document payload's byte length against
// Options.MaxDocumentSize). kind names which one, for the message.
type oversizeError struct {
	kind             string // "event" or "document"
	index, size, max int
}

func (e *oversizeError) Error() string {
	return fmt.Sprintf("%s at index %d is %d bytes, exceeding the configured maximum of %d bytes", e.kind, e.index, e.size, e.max)
}

func (s *Server) handleWrite(w http.ResponseWriter, r *http.Request) {
	var req writeRequest
	if err := decodeJSON(r, &req); err != nil {
		s.handleErr(w, r, err)
		return
	}
	if err := validateWriteRequest(req, s.opts.MaxEventSize, s.opts.MaxDocumentSize, s.opts.MaxDocumentsPerWrite); err != nil {
		s.handleErr(w, r, err)
		return
	}

	ticket, ok := s.joinWriteQueue(w, r)
	if !ok {
		return
	}
	defer ticket.Done()

	events, err := s.st.Append(r.Context(), req.Events, req.Condition, req.Documents)
	if err != nil {
		s.handleErr(w, r, err) // store.ErrConcurrencyConflict -> 409, etc.
		return
	}

	resp := writeResponse{Events: make([]appendedEvent, len(events))}
	for i, ev := range events {
		resp.Events[i] = appendedEvent{Sequence: ev.Sequence, Time: ev.Time.UTC().Format(timeLayout)}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// validateWriteRequest checks request-shape/HTTP-layer rules then, per
// event and in order, dcb.EventData.Validate() (fail-fast on the first
// domain violation) and the configured size limit, then condition.Validate()
// if a condition was given, then per document, document.Data.Validate()
// and its own size limit, rejecting a repeated type+id pair within the
// same request rather than leaving its outcome to transaction execution
// order (see internal/store/document.go).
//
// At least one event or document is required, since a request with
// neither is meaningless: a documents-only call, no events and no
// condition, is how a rebuild materializes several projections at once.
func validateWriteRequest(req writeRequest, maxEventSize, maxDocumentSize, maxDocumentsPerWrite int) error {
	if len(req.Events) == 0 && len(req.Documents) == 0 {
		return &dcb.ValidationError{Err: errEmptyWrite, Message: "write request must carry at least one event or document"}
	}
	if len(req.Events) > dcb.MaxEventsPerWrite {
		return &dcb.ValidationError{Err: errTooManyEvents, Message: fmt.Sprintf(
			"write carries %d events, more than the maximum of %d", len(req.Events), dcb.MaxEventsPerWrite)}
	}
	for i, ev := range req.Events {
		if err := ev.Validate(); err != nil {
			return err
		}
		if size := ev.Size(); size > maxEventSize {
			return &oversizeError{kind: "event", index: i, size: size, max: maxEventSize}
		}
	}
	if req.Condition != nil {
		if err := req.Condition.Validate(); err != nil {
			return err
		}
	}

	if len(req.Documents) > maxDocumentsPerWrite {
		return &dcb.ValidationError{Err: errTooManyDocuments, Message: fmt.Sprintf(
			"write carries %d documents, more than the maximum of %d", len(req.Documents), maxDocumentsPerWrite)}
	}
	seen := make(map[[2]string]struct{}, len(req.Documents))
	for i, d := range req.Documents {
		if err := d.Validate(); err != nil {
			return err
		}
		key := [2]string{d.Type, d.ID}
		if _, dup := seen[key]; dup {
			return &dcb.ValidationError{Err: errDuplicateDocumentKey, Message: fmt.Sprintf(
				"document at index %d has the same type and id as an earlier entry in this request", i)}
		}
		seen[key] = struct{}{}
		if d.Payload != nil {
			if size := len(*d.Payload); size > maxDocumentSize {
				return &oversizeError{kind: "document", index: i, size: size, max: maxDocumentSize}
			}
		}
	}
	return nil
}
