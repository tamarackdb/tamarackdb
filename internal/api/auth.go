package api

import (
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
// means routing itself counts as "handler logic" too — auth runs strictly
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

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, bearerPrefix) {
		return "", false
	}
	return strings.TrimPrefix(h, bearerPrefix), true
}

// constantTimeEqual compares the presented token against the configured
// one in constant time, to avoid a timing side-channel on this
// security-sensitive string compare.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
