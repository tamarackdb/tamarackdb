package api

import (
	"encoding/json"
	"errors"
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
	// necessarily preserve %w all the way through. A body that couldn't be
	// read to its end means the same: nobody reads the answer.
	if r.Context().Err() != nil || errors.Is(err, errClientGone) {
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
	case errors.Is(err, tx.ErrBusy):
		writeError(w, http.StatusConflict, "TransactionBusy", "")
	case errors.Is(err, tx.ErrPaused):
		writeError(w, http.StatusServiceUnavailable, "Paused", "")
	case errors.Is(err, tx.ErrTooManyTransactions):
		writeError(w, http.StatusServiceUnavailable, "TooManyTransactions", "")
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
