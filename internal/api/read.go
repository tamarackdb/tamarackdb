package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/ndjson"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// readRequest is the exact wire shape of QUERY /read's JSON body.
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

// readHeader is the first NDJSON line of every /read response:
// {"hasMore":true|false}. Defined here, not in internal/ndjson, since
// hasMore is a TamarackDB wire concept, not something a generic NDJSON
// writer should know about.
type readHeader struct {
	HasMore bool `json:"hasMore"`
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
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

	if req.Time != nil {
		from, before, err := parseTimeRange(req.Time)
		if err != nil {
			s.handleErr(w, r, err)
			return
		}
		filter.TimeFrom, filter.TimeBefore = from, before
	}

	it, err := s.st.Read(r.Context(), filter)
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	defer it.Close()

	nw := ndjson.NewWriter()
	for it.Next() {
		if err := nw.WriteValue(it.Event()); err != nil {
			s.handleErr(w, r, err) // effectively unreachable: dcb.Event.MarshalJSON never errors
			return
		}
	}
	if err := it.Err(); err != nil {
		// Still pre-flush: nothing has touched w yet, so a mid-page
		// store error still gets a clean error response instead of a
		// broken, half-written NDJSON stream.
		s.handleErr(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	_ = nw.Flush(w, readHeader{HasMore: it.HasMore()}) // best-effort past this point
}

// parseTimeRange parses time.from/time.before (RFC3339Nano, matching
// dcb.Event's own parsing) and enforces the one additional semantic rule
// that calls for hand-written validation: a consistent
// time.from/time.before range — from must be earlier than before when
// both are present.
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
