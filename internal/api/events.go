package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/ndjson"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// readRequest is the exact wire shape of QUERY /events's JSON body.
// Query's own UnmarshalJSON (dispatched automatically by encoding/json on
// this named field) handles "*" vs. an array of QueryItem.
type readRequest struct {
	Query         dcb.Query      `json:"query"`
	AfterSequence *int64         `json:"afterSequence,omitempty"`
	Time          *readTimeRange `json:"time,omitempty"`
	Limit         *int           `json:"limit,omitempty"`
}

type readTimeRange struct {
	From   *string `json:"from,omitempty"`
	Before *string `json:"before,omitempty"`
}

// readTrailer is the last NDJSON line of every /events response:
// {"hasMore":true|false}. Defined here, not in internal/ndjson, since
// hasMore is a TamarackDB wire concept, not something a generic NDJSON
// writer should know about. Its absence is meaningful: the response is
// streamed as each event is scanned (see handleEvents), so a failure partway
// through a page simply ends the response with no trailer line, the same
// signal a client already has to handle for a plain dropped connection
// (see docs/content/docs/guides/integration.md's Pagination section).
type readTrailer struct {
	HasMore bool `json:"hasMore"`
}

// readEventWire is the exact wire shape of one NDJSON body line, mirroring
// dcb.Event.MarshalJSON's field names/order. Identifiers and Metadata are
// json.RawMessage, not dcb.IdentifierSet/MetadataSet: store.ReadEvent already
// carries them as the raw bytes stored in the events table, in the same
// compact shape this struct emits, so this passes them straight through
// instead of decoding and re-encoding data that never needs to change shape.
type readEventWire struct {
	Sequence    int64           `json:"sequence"`
	Time        string          `json:"time"`
	Type        string          `json:"type"`
	Identifiers json.RawMessage `json:"identifiers"`
	Metadata    json.RawMessage `json:"metadata"`
	Payload     string          `json:"payload"`
}

// handleReadEvents implements QUERY /events. With a ticket, the read runs
// inside the transaction and sees the events it appended; any failure,
// a malformed body included, rolls the transaction back. Without a
// ticket, the read runs on the read pool and sees committed events only.
func (s *Server) handleReadEvents(w http.ResponseWriter, r *http.Request) {
	if ticket, ok := ticketFrom(r); ok {
		defer s.trackWrite()()
		err := s.tm.Do(ticket, func(tx *store.Tx) error {
			filter, err := s.parseReadRequest(r)
			if err != nil {
				return err
			}
			it, err := tx.Read(r.Context(), filter)
			if err != nil {
				return err
			}
			return streamEvents(w, it)
		})
		if err != nil {
			s.handleErr(w, r, err)
		}
		return
	}

	s.readHTTPOpen.Add(1)
	defer s.readHTTPOpen.Add(-1)
	filter, err := s.parseReadRequest(r)
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	it, err := s.st.Read(r.Context(), filter)
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	if err := streamEvents(w, it); err != nil {
		s.handleErr(w, r, err)
	}
}

// parseReadRequest decodes and validates a QUERY /events body into a
// store.ReadFilter.
func (s *Server) parseReadRequest(r *http.Request) (store.ReadFilter, error) {
	var req readRequest
	if err := decodeJSON(r, &req); err != nil {
		return store.ReadFilter{}, err
	}
	if err := req.Query.Validate(); err != nil {
		return store.ReadFilter{}, err
	}

	// Reuse dcb.AppendCondition's own non-negativity rule for
	// afterSequence rather than reimplementing the same check by hand.
	if req.AfterSequence != nil {
		if err := (dcb.AppendCondition{AfterSequence: req.AfterSequence}).Validate(); err != nil {
			return store.ReadFilter{}, err
		}
	}

	filter := store.ReadFilter{Query: req.Query, AfterSequence: req.AfterSequence, Limit: s.opts.DefaultLimit}

	if req.Limit != nil {
		switch {
		case *req.Limit < 0:
			return store.ReadFilter{}, &dcb.ValidationError{Err: errNegativeLimit, Message: "limit must be a non-negative integer"}
		case *req.Limit == 0:
			// store.ReadFilter.Limit must be >= 1, and a zero-size page
			// can never determine hasMore meaningfully, so it's rejected
			// rather than silently falling back to the default.
			return store.ReadFilter{}, &dcb.ValidationError{Err: errZeroLimit, Message: "limit must be greater than zero"}
		case *req.Limit > s.opts.MaxLimit:
			return store.ReadFilter{}, &dcb.ValidationError{Err: errLimitExceedsMax, Message: fmt.Sprintf(
				"limit %d exceeds the configured maximum of %d", *req.Limit, s.opts.MaxLimit)}
		}
		filter.Limit = *req.Limit
	}

	if req.Time != nil {
		from, before, err := parseTimeRange(req.Time)
		if err != nil {
			return store.ReadFilter{}, err
		}
		filter.TimeFrom, filter.TimeBefore = from, before
	}
	return filter, nil
}

// streamEvents writes it as an NDJSON page, closing it before returning.
// The first line commits to 200 and starts sending bytes before the page
// is known to succeed: a failure past that point can no longer produce a
// clean error response (see readTrailer's doc comment). It can only end
// the response early, with no trailer, and be returned so the caller
// still sees it (handleErr writes nothing once the response has started).
func streamEvents(w http.ResponseWriter, it *store.EventIterator) error {
	defer it.Close()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	nw := ndjson.NewWriter(w)
	for it.Next() {
		ev := it.Event()
		wire := readEventWire{
			Sequence:    ev.Sequence,
			Time:        ev.Time,
			Type:        ev.Type,
			Identifiers: ev.Identifiers,
			Metadata:    ev.Metadata,
			Payload:     ev.Payload,
		}
		if err := nw.WriteValue(wire); err != nil {
			// The client is almost certainly gone (a broken pipe from a
			// dropped connection): nothing left to write to.
			return err
		}
	}
	if err := it.Err(); err != nil {
		return err // no trailer: signals a cut-short page, see readTrailer
	}
	return nw.WriteValue(readTrailer{HasMore: it.HasMore()})
}

// parseTimeRange parses time.from/time.before (RFC3339Nano, matching
// dcb.Event's own parsing) and enforces the one additional semantic rule
// that calls for hand-written validation: a consistent time.from/
// time.before range: from must be earlier than before when both are
// present. A bound may carry any offset: it is converted to UTC before
// being compared against the stored time, which is always UTC.
func parseTimeRange(tr *readTimeRange) (from, before *time.Time, err error) {
	if tr.From != nil {
		t, perr := time.Parse(time.RFC3339Nano, *tr.From)
		if perr != nil {
			return nil, nil, &dcb.ValidationError{Err: perr, Message: fmt.Sprintf("time.from is not a valid RFC3339 timestamp: %q", *tr.From)}
		}
		from = &t
	}
	if tr.Before != nil {
		t, perr := time.Parse(time.RFC3339Nano, *tr.Before)
		if perr != nil {
			return nil, nil, &dcb.ValidationError{Err: perr, Message: fmt.Sprintf("time.before is not a valid RFC3339 timestamp: %q", *tr.Before)}
		}
		before = &t
	}
	if from != nil && before != nil && !from.Before(*before) {
		return nil, nil, &dcb.ValidationError{Err: errInvalidTimeRange, Message: "time.from must be earlier than time.before"}
	}
	return from, before, nil
}

// timeLayout mirrors dcb.Event's own wire format exactly (ATOM/RFC3339,
// fixed microsecond precision, UTC). It is duplicated here because dcb
// keeps its layout unexported and appendedEvent is intentionally its own,
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

// oversizeError is returned by the per-event and per-document size checks
// (dcb.EventData.Size() against Options.MaxEventSize, or a document
// payload's byte length against Options.MaxDocumentSize). kind names
// which one, for the message.
type oversizeError struct {
	kind             string // "event" or "document"
	index, size, max int
}

func (e *oversizeError) Error() string {
	return fmt.Sprintf("%s at index %d is %d bytes, exceeding the configured maximum of %d bytes", e.kind, e.index, e.size, e.max)
}

// handleAppendEvents implements POST /events: it checks the optional
// Append Condition, then appends the events, inside the transaction. The
// ticket is required. Any failure, a failed condition included, rolls the
// transaction back.
func (s *Server) handleAppendEvents(w http.ResponseWriter, r *http.Request) {
	defer s.trackWrite()()
	ticket, ok := s.requireTicket(w, r)
	if !ok {
		return
	}

	var resp appendResponse
	err := s.tm.Do(ticket, func(tx *store.Tx) error {
		var req appendRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if err := validateAppendRequest(req, s.opts.MaxEventSize); err != nil {
			return err
		}
		events, err := tx.Append(r.Context(), req.Events, req.Condition)
		if err != nil {
			return err // store.ErrConcurrencyConflict -> 409, etc.
		}
		resp.Events = make([]appendedEvent, len(events))
		for i, ev := range events {
			resp.Events[i] = appendedEvent{Sequence: ev.Sequence, Time: ev.Time.UTC().Format(timeLayout)}
		}
		return nil
	})
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// validateAppendRequest checks request-shape rules (between 1 and
// dcb.MaxEventsPerWrite events), then, per event and in order,
// dcb.EventData.Validate() (fail-fast on the first domain violation) and
// the configured size limit, then condition.Validate() if a condition was
// given.
func validateAppendRequest(req appendRequest, maxEventSize int) error {
	if len(req.Events) == 0 {
		return &dcb.ValidationError{Err: errNoEvents, Message: "request must carry at least one event"}
	}
	if len(req.Events) > dcb.MaxEventsPerWrite {
		return &dcb.ValidationError{Err: errTooManyEvents, Message: fmt.Sprintf(
			"request carries %d events, more than the maximum of %d", len(req.Events), dcb.MaxEventsPerWrite)}
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
	return nil
}
