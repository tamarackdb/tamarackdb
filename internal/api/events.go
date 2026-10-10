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
}

// readTrailer is the last NDJSON line of every /events response:
// {"hasMore":true|false}. Defined here, not in internal/ndjson, since
// hasMore is a TamarackDB wire concept, not something a generic NDJSON
// writer should know about. Its absence is meaningful: the response is
// streamed as each event is scanned (see handleReadEvents), so a failure
// partway through a page ends the response with no trailer line, the same
// signal a client already handles for a dropped connection.
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

// writeReadEvents writes the events of it, one line each, with beforeWrite
// run before each line. It doesn't close it.
func writeReadEvents(nw *ndjson.Writer, it *store.EventIterator, beforeWrite func()) error {
	for it.Next() {
		beforeWrite()
		ev := it.Event()
		if err := nw.WriteAppend(func(dst []byte) []byte { return appendReadEvent(dst, ev) }); err != nil {
			// The client is almost certainly gone (a broken pipe from a
			// dropped connection): nothing left to write to.
			return err
		}
	}
	return it.Err()
}

// handleReadEvents implements QUERY /events: the read runs on the read pool
// and sees committed events only.
func (s *Server) handleReadEvents(w http.ResponseWriter, r *http.Request) {
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
	renew, done := s.stallDeadline(w)
	defer done()
	if err := streamEvents(w, it, renew); err != nil {
		s.handleErr(w, r, err)
	}
}

// stallDeadline bounds how long each line of a streamed read may take to
// go out. A read holds a read connection, and pins its SQLite snapshot,
// until it's fully sent. A client that stops reading would hold both
// forever: each line gets readStallTimeout to go out, renewed by calling
// renew before each one, so a read that keeps moving is never cut, and a
// stalled one frees its connection. The caller defers done.
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

// readStallTimeout is how long a line of a streamed read may take to go
// out before the server gives up on the client. A variable, not a
// constant, so tests can shorten it.
var readStallTimeout = 30 * time.Second

// parseReadRequest decodes and validates a QUERY /events body into a
// store.ReadFilter.
func (s *Server) parseReadRequest(r *http.Request) (store.ReadFilter, error) {
	var req readRequest
	if err := decodeJSONStrict(r, &req); err != nil {
		return store.ReadFilter{}, err
	}
	if err := req.Query.Validate(); err != nil {
		return store.ReadFilter{}, err
	}

	if req.AfterSequence != nil && *req.AfterSequence < 0 {
		return store.ReadFilter{}, &dcb.ValidationError{Err: dcb.ErrNegativeAfterSequence, Message: "afterSequence must be a non-negative integer"}
	}

	filter := store.ReadFilter{Query: req.Query, AfterSequence: req.AfterSequence, Limit: s.opts.DefaultEventsPerPage}

	if req.Limit != nil {
		switch {
		case *req.Limit < 0:
			return store.ReadFilter{}, &dcb.ValidationError{Err: errNegativeLimit, Message: "limit must be a non-negative integer"}
		case *req.Limit == 0:
			// store.ReadFilter.Limit must be >= 1, and a zero-size page
			// can never determine hasMore meaningfully, so it's rejected
			// rather than silently falling back to the default.
			return store.ReadFilter{}, &dcb.ValidationError{Err: errZeroLimit, Message: "limit must be greater than zero"}
		case *req.Limit > s.opts.MaxEventsPerPage:
			return store.ReadFilter{}, &dcb.ValidationError{Err: errLimitExceedsMax, Message: fmt.Sprintf(
				"limit %d exceeds maxEventsPerPage (%d)", *req.Limit, s.opts.MaxEventsPerPage)}
		}
		filter.Limit = *req.Limit
	}
	return filter, nil
}

// streamEvents writes it as an NDJSON page, closing it before returning.
// beforeWrite, if non-nil, runs before each write, to renew a deadline.
// The first line commits to 200 and starts sending bytes before the page
// is known to succeed: a failure past that point can no longer produce a
// clean error response (see readTrailer's doc comment). It can only end
// the response early, with no trailer, and be returned so the caller
// still sees it (handleErr writes nothing once the response has started).
func streamEvents(w http.ResponseWriter, it *store.EventIterator, beforeWrite func()) error {
	defer it.Close()
	if beforeWrite == nil {
		beforeWrite = func() {}
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	beforeWrite()
	w.WriteHeader(http.StatusOK)

	nw := ndjson.NewWriter(w)
	if err := writeReadEvents(nw, it, beforeWrite); err != nil {
		return err // no trailer: signals a cut-short page, see readTrailer
	}
	beforeWrite()
	return nw.WriteValue(readTrailer{HasMore: it.HasMore()})
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
