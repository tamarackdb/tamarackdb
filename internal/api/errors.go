package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/store"
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
// every error response through this (directly, or via handleErr) — no
// handler builds error JSON by hand.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: code, Message: message})
}

// handleErr maps err to the right HTTP status/envelope and writes it, or
// writes nothing at all if the client is already gone.
func (s *Server) handleErr(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		return
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

	var ve *dcb.ValidationError
	var oe *oversizeError
	switch {
	case errors.As(err, &ve):
		// Covers dcb.EventData.Validate(), dcb.Query.Validate(),
		// dcb.AppendCondition.Validate(), and request-shape decode
		// errors (see decodeJSON, which wraps those as
		// *dcb.ValidationError too, so they land in this same case).
		writeError(w, http.StatusBadRequest, "InvalidRequest", ve.Message)
	case errors.As(err, &oe):
		writeError(w, http.StatusRequestEntityTooLarge, "PayloadTooLarge", oe.Error())
	case errors.Is(err, store.ErrConcurrencyConflict):
		s.failedTotal.Add(1)
		writeError(w, http.StatusConflict, "ConcurrencyException", "")
	default:
		// Everything else: gatekeeper.ErrClosed, and any other
		// unexpected error, including fatal storage errors.
		if store.IsFatal(err) && s.opts.OnFatalStorageError != nil {
			s.opts.OnFatalStorageError(err)
		}
		writeError(w, http.StatusInternalServerError, "InternalError", "an unexpected error occurred")
	}
}

// decodeJSON decodes r's JSON body into v, wrapping any failure — invalid
// JSON syntax, wrong top-level shape, or a failure from v's own
// UnmarshalJSON (e.g. dcb.Query's or dcb.IdentifierSet's, which return
// plain errors, not *dcb.ValidationError, since shape parsing is
// encoding/json plumbing, not a dcb domain rule) — as a
// *dcb.ValidationError. This lets handleErr's single case handle
// "malformed body" and "domain validation failure" identically, both as
// 400 InvalidRequest.
func decodeJSON(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return &dcb.ValidationError{
			Err:     fmt.Errorf("invalid request body: %w", err),
			Message: "request body is not valid JSON for this endpoint: " + err.Error(),
		}
	}
	return nil
}

// api-layer validation sentinels: rules with no dcb.Validate() equivalent
// to reuse, because they concern purely HTTP-layer/configured concepts
// (limit, event size) or request-shape concerns dcb has no opinion about
// (an empty events array).
var (
	errEmptyAppend      = errors.New("api: append request carries no events")
	errTooManyEvents    = errors.New("api: append request exceeds the maximum events per append")
	errNegativeLimit    = errors.New("api: limit must be non-negative")
	errZeroLimit        = errors.New("api: limit must be greater than zero")
	errLimitExceedsMax  = errors.New("api: limit exceeds the configured maximum")
	errInvalidTimeRange = errors.New("api: time.from must be earlier than time.before")
)
