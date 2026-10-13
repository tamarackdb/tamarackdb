package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

const bearerPrefix = "Bearer "

// withAuth wraps next (the whole routed mux), rejecting any request
// without a Bearer token matching s.opts.AuthToken with 401 before next
// ever runs: a request without a valid token is rejected with 401
// Unauthorized before reaching any handler logic. Wrapping the entire mux,
// rather than each route individually,
// means routing itself counts as "handler logic" too: auth runs strictly
// before it, with no per-route exception.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok || !constantTimeEqual(token, s.opts.AuthToken) {
			writeError(w, http.StatusUnauthorized, "Unauthorized", "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken returns the token of r's Authorization header. The scheme
// name is case-insensitive (RFC 7235): "bearer" and "BEARER" are accepted.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < len(bearerPrefix) || !strings.EqualFold(h[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}
	return h[len(bearerPrefix):], true
}

// constantTimeEqual compares the presented token against the configured
// one in constant time, to avoid a timing side-channel on this
// security-sensitive string compare. It compares their SHA-256 digests,
// which have a fixed length: subtle.ConstantTimeCompare returns at once
// on strings of different lengths, which would reveal the token's length.
func constantTimeEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
