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
	"github.com/tamarackdb/tamarackdb/internal/tx"
	"github.com/tamarackdb/tamarackdb/internal/writer"
)

// errorEnvelope is the exact wire shape for error responses:
// {"error": "...", "message": "..."}. message is omitted when it wouldn't
// add anything, as for WriteQueueFull or ShuttingDown.
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
	var ce *store.ConditionConflictError
	var txe *tx.ConflictError

	// If the request's own context is already Done, the connection may
	// already be gone, and any response now is best-effort at best.
	// Checking r.Context().Err() directly is more reliable than
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
		// Covers dcb.EventInput.Validate(), dcb.Query.Validate(),
		// request-shape decode errors
		// (see decodeJSON, which wraps those as *dcb.ValidationError
		// too), and every API-layer-invented rule (limit, projection
		// count cap, duplicate projection key)
		// that isn't really a dcb domain rule but reuses this same 400
		// vehicle.
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
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, "ConcurrencyException", ce.Error())
	case errors.As(err, &txe):
		writeError(w, http.StatusConflict, "ConcurrencyException", txe.Error())
	case errors.Is(err, tx.ErrNotFound):
		writeError(w, http.StatusNotFound, "TransactionNotFound", "")
	case errors.Is(err, queue.ErrFull):
		writeError(w, http.StatusServiceUnavailable, "WriteQueueFull", "")
	case errors.Is(err, writer.ErrClosed), errors.Is(err, queue.ErrClosed):
		// The server is shutting down: a request waiting in the FIFO, or
		// arriving after it closed, gets no turn.
		writeError(w, http.StatusServiceUnavailable, "ShuttingDown", "")
	default:
		// Everything else: any unexpected error, including fatal storage
		// errors.
		s.internalErrors.Add(1)
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

// withBodyLimit caps every request body at Options.MaxRequestBodySize, so
// a client can't make the server read an unbounded body into memory
// before the other limits are checked.
func (s *Server) withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, int64(s.opts.MaxRequestBodySize))
		next.ServeHTTP(w, r)
	})
}

// bodyTooLargeError is returned when a request body goes past the
// server's body limit.
type bodyTooLargeError struct{ limit int64 }

func (e *bodyTooLargeError) Error() string {
	return fmt.Sprintf("request body exceeds maxRequestBodySize (%d bytes)", e.limit)
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
// (limit, the per-write counts) or request-shape concerns dcb has no
// opinion about (a projection written twice in one request).
var (
	errTooManyProjections     = errors.New("api: request exceeds the maximum projections per write")
	errDuplicateProjectionKey = errors.New("api: request carries the same projection type+id more than once")
	errNegativeLimit          = errors.New("api: limit must be non-negative")
	errZeroLimit              = errors.New("api: limit must be greater than zero")
	errLimitExceedsMax        = errors.New("api: limit exceeds the configured maximum")
	errTrailingData           = errors.New("api: request body holds more than one JSON value")
)

// prefixed puts path in front of a validation error's message, so it names
// the item it's about ("events[3]: ..."). Any other error is returned
// as-is.
func prefixed(path string, err error) error {
	var ve *dcb.ValidationError
	if errors.As(err, &ve) {
		return &dcb.ValidationError{Err: ve.Err, Message: path + ": " + ve.Message}
	}
	var pe *projection.ValidationError
	if errors.As(err, &pe) {
		return &projection.ValidationError{Err: pe.Err, Message: path + ": " + pe.Message}
	}
	return err
}
