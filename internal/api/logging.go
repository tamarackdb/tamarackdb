package api

import (
	"log"
	"net/http"
	"time"
)

// withLogging wraps next (the whole routed mux, auth included) and logs one
// line per request: method, path, resulting status code, and duration. It
// never logs request or response bodies, so /read conditions and /append
// events never reach the log.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		elapsedMs := float64(time.Since(start)) / float64(time.Millisecond)
		log.Printf("tamarackdb: %s %s %d %.2fms", r.Method, r.URL.Path, sw.status, elapsedMs)
	})
}

// statusWriter captures the status code passed to WriteHeader so withLogging
// can report it; handlers that never call WriteHeader (200 OK) keep the
// default set above.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(status int) {
	sw.status = status
	sw.ResponseWriter.WriteHeader(status)
}
