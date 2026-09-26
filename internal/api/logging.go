package api

import (
	"log"
	"net/http"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/txn"
)

// withLogging wraps next (the whole routed mux, auth included) and logs one
// line per request: method, path, resulting status code, and duration,
// tagged with a severity level. A request whose level falls below
// s.logThreshold is not logged at all. It never logs request or response
// bodies, so queries, conditions, and events never reach the log.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK, level: levelDebug}
		next.ServeHTTP(sw, r)
		if sw.level < s.logThreshold {
			return
		}
		elapsedMs := float64(time.Since(start)) / float64(time.Millisecond)
		log.Printf("tamarackdb-server: [%s] %s %s %d %dB %.2fms", sw.level, r.Method, r.URL.Path, sw.status, sw.bytes, elapsedMs)
	})
}

// statusWriter captures the status code passed to WriteHeader, the total
// bytes written, and the access-log level for this response, so
// withLogging can report them; handlers that never call WriteHeader
// (200 OK) keep the defaults set above.
type statusWriter struct {
	http.ResponseWriter
	status      int
	bytes       int
	level       level
	wroteHeader bool
}

func (sw *statusWriter) WriteHeader(status int) {
	sw.status = status
	sw.wroteHeader = true
	sw.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.ResponseController reach the underlying connection, to
// set deadlines (see doInTx).
func (sw *statusWriter) Unwrap() http.ResponseWriter {
	return sw.ResponseWriter
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	sw.wroteHeader = true
	n, err := sw.ResponseWriter.Write(b)
	sw.bytes += n
	return n, err
}

// ExpiryLogger returns the function internal/txn calls when it rolls back
// an expired transaction (txn.Config.OnExpire). No request is there to
// log it, so it writes its own line, at warning level, when logLevel lets
// warnings through. logLevel must be one of the four level names.
//
// The line carries the ticket, the only place one is ever logged: the
// transaction has already ended, so the ticket can't be used any more,
// and it lets a client that logged its ticket find which command expired.
func ExpiryLogger(logLevel string) func(ticket string, limit txn.Limit, lasted time.Duration) {
	threshold, _ := parseLevel(logLevel)
	return func(ticket string, limit txn.Limit, lasted time.Duration) {
		if levelWarning < threshold {
			return
		}
		log.Printf("tamarackdb-server: [%s] transaction %s expired: %s reached after %.2fs", levelWarning, ticket, limit, lasted.Seconds())
	}
}
