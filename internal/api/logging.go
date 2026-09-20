package api

import (
	"log"
	"net/http"
	"time"
)

// withLogging wraps next (the whole routed mux, auth included) and logs one
// line per request: method, path, resulting status code, and duration,
// tagged with a severity level. A request whose level falls below
// s.logThreshold is not logged at all. It never logs request or response
// bodies, so /read conditions and /append events never reach the log.
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
	status int
	bytes  int
	level  level
}

func (sw *statusWriter) WriteHeader(status int) {
	sw.status = status
	sw.ResponseWriter.WriteHeader(status)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	n, err := sw.ResponseWriter.Write(b)
	sw.bytes += n
	return n, err
}
