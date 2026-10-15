package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// decodeJSONStrict reads r's body in full, then decodes it into v,
// rejecting unknown keys at every level of v: a misspelled key would
// otherwise drop data without a word. It wraps any decoding failure as a
// *dcb.ValidationError. A failure can be invalid JSON syntax, a wrong
// top-level shape, an unknown key, or an error from v's own UnmarshalJSON
// (e.g. dcb.Query's or dcb.IdentifierSet's, which return plain errors,
// not *dcb.ValidationError, since shape parsing is encoding/json
// plumbing, not a dcb domain rule). This lets handleErr's single case
// handle "malformed body" and "domain validation failure" identically,
// both as 400 InvalidRequest. A body that can't be read gets
// errClientGone or a *bodyTooLargeError instead (see readBody).
func decodeJSONStrict(w http.ResponseWriter, r *http.Request, v any) error {
	body, err := readBody(w, r)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return decode(dec, v)
}

// errClientGone is returned when a request body can't be read to its end:
// the client left while sending it, broke its framing, or took longer than
// the server's ReadTimeout. Either way the connection is no longer usable,
// so nothing is answered.
var errClientGone = errors.New("api: the request body could not be read to its end")

// readBody reads r's body in full, before any of it is decoded. A request
// is accepted only once its body is read: a client that leaves before has
// asked for nothing, so a request on a transaction MUST then leave the
// transaction as it is, and count no design error. Reading first, then
// decoding, is what tells the two failures apart: the JSON decoder
// returns io.ErrUnexpectedEOF both for a body cut short by the client
// leaving and for a complete body holding truncated JSON, which is a
// design error. r.Context() can't tell either: over HTTP/1.1, net/http
// only watches the connection once the body has been read.
//
// A body that can't be read to its end closes the connection at once.
// Otherwise, net/http would answer 200 with an empty body once the
// handler returns, and a slow client could take it for a success.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if tooLarge := bodyTooLarge(err); tooLarge != nil {
			return nil, tooLarge
		}
		if conn, _, hijackErr := http.NewResponseController(w).Hijack(); hijackErr == nil {
			_ = conn.Close()
		}
		return nil, fmt.Errorf("%w: %w", errClientGone, err)
	}
	return body, nil
}

// decode reads exactly one JSON value into v. Anything after it, other
// than whitespace, is rejected: a body with two values, or with trailing
// text, is malformed rather than silently cut short.
func decode(dec *json.Decoder, v any) error {
	if err := dec.Decode(v); err != nil {
		return &dcb.ValidationError{
			Err:     fmt.Errorf("invalid request body: %w", err),
			Message: "request body is not valid JSON for this endpoint: " + err.Error(),
		}
	}
	if _, err := dec.Token(); err != io.EOF {
		return &dcb.ValidationError{Err: errTrailingData, Message: "request body must hold a single JSON value"}
	}
	return nil
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
