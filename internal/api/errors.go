package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
	"github.com/tamarackdb/tamarackdb/internal/txn"
)

// errorEnvelope is the exact wire shape for error responses:
// {"error": "...", "message": "..."}. message is omitted
// when it wouldn't add anything, matching the ConcurrencyException
// example, which carries no "message" key at all.
type errorEnvelope struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// writeError writes the standard error envelope. Every handler funnels
// every error response through this (directly, or via handleErr). No
// handler builds error JSON by hand.
func writeError(w http.ResponseWriter, status int, code, message string) {
	if sw, ok := w.(*statusWriter); ok {
		if lvl, ok := codeLevel[code]; ok {
			sw.level = lvl
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: code, Message: message})
}

// handleErr maps err to the right HTTP status/envelope and writes it, or
// writes nothing at all if the client is already gone or the response has
// already started (a read cut short partway through its NDJSON stream).
func (s *Server) handleErr(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		return
	}
	if store.IsFatal(err) && s.opts.OnFatalStorageError != nil {
		s.opts.OnFatalStorageError(err)
	}
	var pe *store.ProjectionConflictError
	if errors.Is(err, store.ErrConcurrencyConflict) && !errors.As(err, &pe) {
		s.failedTotal.Add(1) // Append Conditions only, not projection versions
	}

	// If the request's own context is already Done, the connection may
	// already be gone, and any response now is best-effort at best.
	// Checking r.Context().Err() directly is more robust than
	// pattern-matching on err's exact shape, since err may have been
	// wrapped by database/sql or the SQLite driver in ways that don't
	// necessarily preserve %w all the way through.
	if r.Context().Err() != nil {
		return
	}
	if sw, ok := w.(*statusWriter); ok && sw.wroteHeader {
		return
	}

	var ve *dcb.ValidationError
	var de *projection.ValidationError
	var oe *oversizeError
	var be *bodyTooLargeError
	switch {
	case errors.As(err, &ve):
		// Covers dcb.EventData.Validate(), dcb.Query.Validate(),
		// dcb.AppendCondition.Validate(), request-shape decode errors
		// (see decodeJSON, which wraps those as *dcb.ValidationError
		// too), and every API-layer-invented rule (limit, event/projection
		// count caps, duplicate projection key, missing ticket) that isn't
		// really a dcb domain rule but reuses this same 400 vehicle.
		writeError(w, http.StatusBadRequest, "InvalidRequest", ve.Message)
	case errors.As(err, &de):
		// The projection types' own Validate() rules (missing type, id,
		// version or payload): same 400 treatment, distinct type since
		// internal/projection doesn't depend on internal/dcb.
		writeError(w, http.StatusBadRequest, "InvalidRequest", de.Message)
	case errors.As(err, &oe):
		writeError(w, http.StatusRequestEntityTooLarge, "PayloadTooLarge", oe.Error())
	case errors.As(err, &be):
		writeError(w, http.StatusRequestEntityTooLarge, "PayloadTooLarge", be.Error())
	case errors.As(err, &pe):
		writeError(w, http.StatusConflict, "ConcurrencyException", pe.Error())
	case errors.Is(err, store.ErrConcurrencyConflict):
		writeError(w, http.StatusConflict, "ConcurrencyException", "")
	case errors.Is(err, txn.ErrNotPaused):
		// 409, not 503: the server isn't in the state the call requires.
		// A 503 would suggest a temporary outage worth retrying.
		writeError(w, http.StatusConflict, "NotPaused", "")
	case errors.Is(err, txn.ErrTicketNotActive):
		writeError(w, http.StatusGone, "TicketNotActive", "")
	case errors.Is(err, txn.ErrPaused):
		writeError(w, http.StatusServiceUnavailable, "Paused", "")
	case errors.Is(err, queue.ErrFull):
		writeError(w, http.StatusServiceUnavailable, "TransactionQueueFull", "")
	case errors.Is(err, txn.ErrClosed), errors.Is(err, queue.ErrClosed):
		// The server is shutting down: a request waiting in the FIFO, or
		// arriving after it closed, gets no turn.
		writeError(w, http.StatusServiceUnavailable, "ShuttingDown", "")
	default:
		// Everything else: any unexpected error, including fatal storage
		// errors.
		writeError(w, http.StatusInternalServerError, "InternalError", "an unexpected error occurred")
	}
}

// decodeJSON decodes r's JSON body into v and wraps any failure as a
// *dcb.ValidationError. A failure can be invalid JSON syntax, a wrong
// top-level shape, or an error from v's own UnmarshalJSON (e.g.
// dcb.Query's or dcb.IdentifierSet's, which return plain errors, not
// *dcb.ValidationError, since shape parsing is encoding/json plumbing,
// not a dcb domain rule). This lets handleErr's single case handle
// "malformed body" and "domain validation failure" identically, both as
// 400 InvalidRequest.
func decodeJSON(r *http.Request, v any) error {
	return decode(json.NewDecoder(r.Body), v)
}

// decodeJSONStrict is decodeJSON that also rejects unknown keys, at every
// level of v. It's for a body whose keys are all optional, where a
// misspelled key would otherwise drop data without a word.
func decodeJSONStrict(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return decode(dec, v)
}

// decode reads exactly one JSON value into v. Anything after it, other
// than whitespace, is rejected: a body with two values, or with trailing
// text, is malformed rather than silently cut short.
func decode(dec *json.Decoder, v any) error {
	if err := dec.Decode(v); err != nil {
		if tooLarge := bodyTooLarge(err); tooLarge != nil {
			return tooLarge
		}
		return &dcb.ValidationError{
			Err:     fmt.Errorf("invalid request body: %w", err),
			Message: "request body is not valid JSON for this endpoint: " + err.Error(),
		}
	}
	switch _, err := dec.Token(); {
	case err == io.EOF:
		return nil
	case bodyTooLarge(err) != nil:
		return bodyTooLarge(err)
	default:
		return &dcb.ValidationError{Err: errTrailingData, Message: "request body must hold a single JSON value"}
	}
}

// Inputs to MaxRequestBody.
const (
	// jsonEscapeFactor is the most a JSON string can grow once escaped:
	// a control character, written \u0000, takes 6 bytes for 1. The size
	// limits count decoded bytes, but the body limit counts raw ones.
	jsonEscapeFactor = 6

	// itemFraming covers what the size limits don't count in each event
	// or projection: JSON keys and punctuation, and a projection's
	// version (a 36-byte UUID).
	itemFraming = 4 << 10 // 4 KiB

	// bodyMargin covers the rest of a body: an Append Condition, or a
	// QUERY /events query. No size limit bounds the strings of a query, so
	// the body limit guarantees room for one of up to bodyMargin bytes.
	bodyMargin = 1 << 20 // 1 MiB
)

// MaxRequestBody returns the largest request body a Server built with opts
// reads, in bytes. It's derived from the configured limits, so it never
// turns away a body they allow, however its strings are escaped: the
// largest valid POST /events or POST /projections content, times
// jsonEscapeFactor, plus framing and a margin. It keeps a client from
// making the server read an unbounded body into memory before those
// limits are checked.
func MaxRequestBody(opts Options) int64 {
	events := int64(dcb.MaxEventsPerWrite) * int64(opts.MaxEventSize)
	projections := int64(opts.MaxProjectionsPerRequest) * int64(opts.MaxProjectionSize)
	items := int64(max(dcb.MaxEventsPerWrite, opts.MaxProjectionsPerRequest))
	return jsonEscapeFactor*max(events, projections) + items*itemFraming + bodyMargin
}

// withBodyLimit caps every request body at s.maxRequestBody.
func (s *Server) withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, s.maxRequestBody)
		next.ServeHTTP(w, r)
	})
}

// bodyTooLargeError is returned when a request body goes past the
// server's body limit.
type bodyTooLargeError struct{ limit int64 }

func (e *bodyTooLargeError) Error() string {
	return fmt.Sprintf("request body exceeds the maximum of %d bytes", e.limit)
}

// bodyTooLarge returns a *bodyTooLargeError if err comes from reading past
// the body limit, or nil otherwise.
func bodyTooLarge(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return &bodyTooLargeError{limit: mbe.Limit}
	}
	return nil
}

// api-layer validation sentinels: rules with no dcb.Validate() equivalent
// to reuse, because they concern purely HTTP-layer/configured concepts
// (limit, event size, the ticket header) or request-shape concerns dcb has
// no opinion about (a request missing its events field).
var (
	errMissingTicket          = errors.New("api: missing ticket header")
	errMissingEvents          = errors.New("api: request is missing its events field")
	errNoProjectionWrites     = errors.New("api: request carries none of create, replace, delete")
	errTooManyEvents          = errors.New("api: request exceeds the maximum events per call")
	errTooManyProjections     = errors.New("api: request exceeds the maximum projections per call")
	errDuplicateProjectionKey = errors.New("api: request carries the same projection type+id more than once")
	errNegativeLimit          = errors.New("api: limit must be non-negative")
	errZeroLimit              = errors.New("api: limit must be greater than zero")
	errLimitExceedsMax        = errors.New("api: limit exceeds the configured maximum")
	errInvalidTimeRange       = errors.New("api: time.from must be earlier than time.before")
	errTrailingData           = errors.New("api: request body holds more than one JSON value")
)

// errMissingTicketValidation is the 400 for a call that requires a ticket
// and carries none.
var errMissingTicketValidation = &dcb.ValidationError{Err: errMissingTicket, Message: "missing " + TicketHeader + " header"}
