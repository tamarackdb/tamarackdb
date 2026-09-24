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
	ClientTime    *readTimeRange `json:"clientTime,omitempty"`
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
	ClientTime  string          `json:"clientTime"`
	WriteTime   string          `json:"writeTime"`
	Type        string          `json:"type"`
	Identifiers json.RawMessage `json:"identifiers"`
	Metadata    json.RawMessage `json:"metadata"`
	Payload     string          `json:"payload"`
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	s.readHTTPOpen.Add(1)
	defer s.readHTTPOpen.Add(-1)

	var req readRequest
	if err := decodeJSON(r, &req); err != nil {
		s.handleErr(w, r, err)
		return
	}
	if err := req.Query.Validate(); err != nil {
		s.handleErr(w, r, err)
		return
	}

	// Reuse dcb.AppendCondition's own non-negativity rule for
	// afterSequence rather than reimplementing the same check by hand.
	if req.AfterSequence != nil {
		if err := (dcb.AppendCondition{AfterSequence: req.AfterSequence}).Validate(); err != nil {
			s.handleErr(w, r, err)
			return
		}
	}

	filter := store.ReadFilter{Query: req.Query, AfterSequence: req.AfterSequence, Limit: s.opts.DefaultLimit}

	if req.Limit != nil {
		switch {
		case *req.Limit < 0:
			s.handleErr(w, r, &dcb.ValidationError{Err: errNegativeLimit, Message: "limit must be a non-negative integer"})
			return
		case *req.Limit == 0:
			// store.ReadFilter.Limit must be >= 1, and a zero-size page
			// can never determine hasMore meaningfully, so it's rejected
			// rather than silently falling back to the default.
			s.handleErr(w, r, &dcb.ValidationError{Err: errZeroLimit, Message: "limit must be greater than zero"})
			return
		case *req.Limit > s.opts.MaxLimit:
			s.handleErr(w, r, &dcb.ValidationError{Err: errLimitExceedsMax, Message: fmt.Sprintf(
				"limit %d exceeds the configured maximum of %d", *req.Limit, s.opts.MaxLimit)})
			return
		}
		filter.Limit = *req.Limit
	}

	if req.ClientTime != nil {
		from, before, err := parseTimeRange(req.ClientTime)
		if err != nil {
			s.handleErr(w, r, err)
			return
		}
		filter.ClientTimeFrom, filter.ClientTimeBefore = from, before
	}

	it, err := s.st.Read(r.Context(), filter)
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	defer it.Close()

	// From here on, the response is streamed: the first WriteValue call
	// below commits to 200 and starts sending bytes, before the page is
	// known to succeed. A failure past this point can no longer produce a
	// clean error response (see readTrailer's doc comment); it can only
	// end the response early, with the read-side effects of a failure
	// (below) preserved even though nothing more is written to w.
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	nw := ndjson.NewWriter(w)
	for it.Next() {
		ev := it.Event()
		wire := readEventWire{
			Sequence:    ev.Sequence,
			ClientTime:  ev.ClientTime,
			WriteTime:   ev.WriteTime,
			Type:        ev.Type,
			Identifiers: ev.Identifiers,
			Metadata:    ev.Metadata,
			Payload:     ev.Payload,
		}
		if err := nw.WriteValue(wire); err != nil {
			// The client is almost certainly gone (a broken pipe from a
			// dropped connection): nothing left to write to, and nothing
			// useful to report back.
			return
		}
	}
	if err := it.Err(); err != nil {
		if store.IsFatal(err) && s.opts.OnFatalStorageError != nil {
			s.opts.OnFatalStorageError(err)
		}
		return // no trailer: signals a cut-short page, see readTrailer
	}

	_ = nw.WriteValue(readTrailer{HasMore: it.HasMore()}) // best-effort: the client may already be gone
}

// parseTimeRange parses clientTime.from/clientTime.before (RFC3339Nano,
// matching dcb.Event's own parsing) and enforces the one additional
// semantic rule that calls for hand-written validation: a consistent
// clientTime.from/clientTime.before range: from must be earlier than
// before when both are present. A bound may carry any offset: it is
// converted to UTC before being compared against client_time, which is
// always stored in UTC.
func parseTimeRange(tr *readTimeRange) (from, before *time.Time, err error) {
	if tr.From != nil {
		t, perr := time.Parse(time.RFC3339Nano, *tr.From)
		if perr != nil {
			return nil, nil, &dcb.ValidationError{Err: perr, Message: fmt.Sprintf("clientTime.from is not a valid RFC3339 timestamp: %q", *tr.From)}
		}
		from = &t
	}
	if tr.Before != nil {
		t, perr := time.Parse(time.RFC3339Nano, *tr.Before)
		if perr != nil {
			return nil, nil, &dcb.ValidationError{Err: perr, Message: fmt.Sprintf("clientTime.before is not a valid RFC3339 timestamp: %q", *tr.Before)}
		}
		before = &t
	}
	if from != nil && before != nil && !from.Before(*before) {
		return nil, nil, &dcb.ValidationError{Err: errInvalidTimeRange, Message: "clientTime.from must be earlier than clientTime.before"}
	}
	return from, before, nil
}
