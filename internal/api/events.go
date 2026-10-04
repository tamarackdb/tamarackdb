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

// StoreHeader carries the store ID in a response that depends on the
// store: QUERY /events, GET /projections/{type}/{id}, and POST /write. A
// read takes it in the same SQLite snapshot as the events or projection
// returned, and a write in the same SQLite transaction as what it wrote.
const StoreHeader = "X-Tamarackdb-Store"

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
// streamed as each event is scanned (see handleReadEvents), so a failure partway
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

// handleReadEvents implements QUERY /events: the read runs on the read pool
// and sees committed events only, and the response carries the store ID in
// the X-Tamarackdb-Store header.
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
	w.Header().Set(StoreHeader, it.StoreID())
	// A read holds a read connection, and pins its SQLite snapshot, until
	// the page is fully sent. A client that stops reading would hold both
	// forever: each line gets readStallTimeout to go out, renewed on every
	// line, so a page that keeps moving is never cut, and a stalled one
	// frees its connection.
	rc := http.NewResponseController(w)
	renew := func() { _ = rc.SetWriteDeadline(time.Now().Add(readStallTimeout)) }
	defer func() {
		// Send what's still buffered under the deadline too, then clear
		// it, so it never carries over to the next request on the
		// connection.
		renew()
		_ = rc.Flush()
		_ = rc.SetWriteDeadline(time.Time{})
	}()
	if err := streamEvents(w, it, renew); err != nil {
		s.handleErr(w, r, err)
	}
}

// readStallTimeout is how long a line of a QUERY /events page may take to
// go out before the server gives up on the client. A variable, not a
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

	// Reuse dcb.AppendCondition's own non-negativity rule for
	// afterSequence rather than reimplementing the same check by hand.
	if req.AfterSequence != nil {
		if err := (dcb.AppendCondition{AfterSequence: req.AfterSequence}).Validate(); err != nil {
			return store.ReadFilter{}, err
		}
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
	for it.Next() {
		beforeWrite()
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
	beforeWrite()
	return nw.WriteValue(readTrailer{HasMore: it.HasMore()})
}

// appendedEvent is one event's Sequence Position and time in a POST /write
// response.
type appendedEvent struct {
	Sequence int64  `json:"sequence"`
	Time     string `json:"time"`
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
