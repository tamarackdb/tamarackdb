package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/ndjson"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// readRequest is the exact wire shape of QUERY /events's JSON body.
// Query's own UnmarshalJSON (dispatched automatically by encoding/json on
// this named field) handles "all", "none", and an array of QueryItem.
type readRequest struct {
	Query         dcb.Query `json:"query"`
	AfterSequence *int64    `json:"afterSequence,omitempty"`
	Limit         *int      `json:"limit,omitempty"`
	Backwards     bool      `json:"backwards,omitempty"`
}

// readTrailer is the last NDJSON line of every /events response:
// {"hasMore":true|false}. Defined here, not in internal/ndjson, since
// hasMore is a TamarackDB wire concept, not something a generic NDJSON
// writer should know about. Its absence is meaningful: a page that fails
// to go out to its end, such as on a dropped connection, has no trailer
// line.
type readTrailer struct {
	HasMore bool `json:"hasMore"`
}

// appendReadEvent appends one committed event as a read returns it: the
// bytes json.Marshal gives for the same dcb.Event, built by hand. A page
// holds up to maxEventsPerPage events, and encoding/json's reflection, with
// its check of every json.RawMessage, cost about as much as reading the
// page from SQLite. Identifiers and Metadata are copied as stored: the
// store wrote them with json.Marshal, in the shape a read returns (see
// store.ReadEvent).
func appendReadEvent(dst []byte, ev store.ReadEvent) []byte {
	dst = append(dst, `{"sequence":`...)
	dst = strconv.AppendInt(dst, ev.Sequence, 10)
	dst = append(dst, `,"time":`...)
	dst = ndjson.AppendString(dst, ev.Time)
	dst = append(dst, `,"type":`...)
	dst = ndjson.AppendString(dst, ev.Type)
	dst = append(dst, `,"identifiers":`...)
	dst = append(dst, ev.Identifiers...)
	dst = append(dst, `,"metadata":`...)
	dst = append(dst, ev.Metadata...)
	dst = append(dst, `,"payload":`...)
	dst = ndjson.AppendString(dst, ev.Payload)
	return append(dst, '}')
}

// writeReadEvents writes events, one line each, with beforeWrite run
// before each line.
func writeReadEvents(nw *ndjson.Writer, events []store.ReadEvent, beforeWrite func()) error {
	for _, ev := range events {
		beforeWrite()
		if err := nw.WriteAppend(func(dst []byte) []byte { return appendReadEvent(dst, ev) }); err != nil {
			// The client is almost certainly gone (a broken pipe from a
			// dropped connection): nothing left to write to.
			return err
		}
	}
	return nil
}

// handleReadEvents implements QUERY /events: the read runs on the read pool
// and sees committed events only.
func (s *Server) handleReadEvents(w http.ResponseWriter, r *http.Request) {
	filter, err := s.parseReadRequest(w, r)
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	page, err := s.st.Read(r.Context(), filter)
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	renew, done := s.stallDeadline(w)
	defer done()
	if err := writePage(w, page, renew); err != nil {
		s.handleErr(w, r, err)
	}
}

// stallDeadline bounds how long each line of a read may take to go out.
// The read connection is already back in its pool, but a client that
// stops reading would still hold its HTTP connection, and the page in
// memory, forever: each line gets readStallTimeout to go out, renewed by
// calling renew before each one, so a read that keeps moving is never
// cut, and a stalled one is dropped. The caller defers done.
func (s *Server) stallDeadline(w http.ResponseWriter) (renew, done func()) {
	rc := http.NewResponseController(w)
	renew = func() { _ = rc.SetWriteDeadline(time.Now().Add(readStallTimeout)) }
	done = func() {
		// Send what's still buffered under the deadline too, then clear
		// it, so it never carries over to the next request on the
		// connection.
		renew()
		_ = rc.Flush()
		_ = rc.SetWriteDeadline(time.Time{})
	}
	return renew, done
}

// readStallTimeout is how long a line of a read may take to go out before
// the server gives up on the client. A variable, not a constant, so tests
// can shorten it.
var readStallTimeout = 30 * time.Second

// parseReadRequest decodes and validates a QUERY /events body into a
// store.ReadFilter.
func (s *Server) parseReadRequest(w http.ResponseWriter, r *http.Request) (store.ReadFilter, error) {
	var req readRequest
	if err := decodeJSONStrict(w, r, &req); err != nil {
		return store.ReadFilter{}, err
	}
	if err := req.Query.Validate(); err != nil {
		return store.ReadFilter{}, err
	}

	if req.AfterSequence != nil && *req.AfterSequence < 0 {
		return store.ReadFilter{}, &dcb.ValidationError{Err: dcb.ErrNegativeAfterSequence, Message: "afterSequence must be a non-negative integer"}
	}
	if req.AfterSequence != nil && req.Backwards {
		// A backwards read gives the latest events, and has no page after
		// the first.
		return store.ReadFilter{}, &dcb.ValidationError{Err: errBackwardsAfterSequence, Message: "afterSequence can't be used with backwards"}
	}

	filter := store.ReadFilter{Query: req.Query, AfterSequence: req.AfterSequence, Limit: s.opts.DefaultEventsPerPage, Backwards: req.Backwards}

	if req.Limit != nil {
		if err := validateLimit(*req.Limit); err != nil {
			return store.ReadFilter{}, err
		}
		if *req.Limit > s.opts.MaxEventsPerPage {
			return store.ReadFilter{}, &dcb.ValidationError{Err: errLimitExceedsMax, Message: fmt.Sprintf(
				"limit %d exceeds maxEventsPerPage (%d)", *req.Limit, s.opts.MaxEventsPerPage)}
		}
		filter.Limit = *req.Limit
	}
	return filter, nil
}

// validateLimit checks a read's limit, given: at least 1. A zero limit is
// rejected rather than silently read as the default, or as no limit.
func validateLimit(limit int) error {
	switch {
	case limit < 0:
		return &dcb.ValidationError{Err: errNegativeLimit, Message: "limit must be a non-negative integer"}
	case limit == 0:
		return &dcb.ValidationError{Err: errZeroLimit, Message: "limit must be greater than zero"}
	}
	return nil
}

// writePage writes page as NDJSON, its trailer last. beforeWrite, if
// non-nil, runs before each write, to renew a deadline. The page is
// already read, so an error of the store has already had its response:
// the only failure left is a client that goes away, which ends the
// response early, with no trailer. It's returned so the caller still sees
// it (handleErr writes nothing once the response has started).
func writePage(w http.ResponseWriter, page store.Page, beforeWrite func()) error {
	if beforeWrite == nil {
		beforeWrite = func() {}
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	beforeWrite()
	w.WriteHeader(http.StatusOK)

	nw := ndjson.NewWriter(w)
	if err := writeReadEvents(nw, page.Events, beforeWrite); err != nil {
		return err // no trailer: signals a cut-short page, see readTrailer
	}
	beforeWrite()
	return nw.WriteValue(readTrailer{HasMore: page.HasMore})
}

// oversizeError is returned by the per-event and per-projection size checks
// (dcb.EventData.Size() against Options.MaxEventSize, or a projection's
// size against Options.MaxProjectionSize). at names the item by its place
// in the request body, and setting the limit it went over, so the message
// says which setting to raise.
type oversizeError struct {
	at, setting string
	size, max   int
}

func (e *oversizeError) Error() string {
	return fmt.Sprintf("%s is %d bytes, more than %s (%d)", e.at, e.size, e.setting, e.max)
}

// validateEvents checks each event, in order: dcb.EventInput.Validate()
// (fail-fast on the first domain violation), then maxEventSize. A message
// names the event as events[i].
func validateEvents(events []dcb.EventInput, maxEventSize int) error {
	for i, ev := range events {
		at := fmt.Sprintf("events[%d]", i)
		if err := ev.Validate(); err != nil {
			return prefixed(at, err)
		}
		if size := ev.Data().Size(); size > maxEventSize {
			return &oversizeError{at: at, setting: "maxEventSize", size: size, max: maxEventSize}
		}
	}
	return nil
}

// eventData returns the events to append, once validateEvents has passed.
func eventData(events []dcb.EventInput) []dcb.EventData {
	out := make([]dcb.EventData, len(events))
	for i, ev := range events {
		out[i] = ev.Data()
	}
	return out
}
