package api

import (
	"bytes"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// logBuffer is a bytes.Buffer safe to read while another goroutine logs
// into it (a request served in the background).
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *logBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// captureLog redirects the standard logger's output for the duration of the
// test and returns the buffer withLogging writes into.
func captureLog(t *testing.T) *logBuffer {
	t.Helper()
	buf := &logBuffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

// provokeConflict writes one event, then makes a 409 ConcurrencyException
// write against it.
func provokeConflict(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	appendCommitted(t, srv, `{"events":[{"type":"t","identifiers":{"userId":"123"},"metadata":{},"payload":""}]}`)
	return doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","payload":""}],"conditions":[`+
		userCondition(currentStore(t, srv), 0)+`]}`)
}

func TestAccessLogLevelPerOutcome(t *testing.T) {
	tests := []struct {
		name  string
		level string
		serve func(t *testing.T) *httptest.ResponseRecorder
	}{
		{"success", "DEBUG", func(t *testing.T) *httptest.ResponseRecorder {
			srv, _, _ := newTestServer(t)
			return doRequest(t, srv, "GET", "/health", "")
		}},
		{"ProjectionNotFound", "DEBUG", func(t *testing.T) *httptest.ResponseRecorder {
			srv, _, _ := newTestServer(t)
			return doRequest(t, srv, "GET", "/projections/user-profile/123", "")
		}},
		{"ConcurrencyException", "DEBUG", func(t *testing.T) *httptest.ResponseRecorder {
			srv, _, _ := newTestServer(t)
			return provokeConflict(t, srv)
		}},
		{"InvalidRequest", "INFO", func(t *testing.T) *httptest.ResponseRecorder {
			srv, _, _ := newTestServer(t)
			return doRequest(t, srv, "QUERY", "/events", `{"query":[]}`)
		}},
		{"PayloadTooLarge", "INFO", func(t *testing.T) *httptest.ResponseRecorder {
			srv, _, _ := newTestServer(t)
			body := `{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":"` + strings.Repeat("x", 70000) + `"}]}`
			return doRequest(t, srv, "POST", "/write", body)
		}},
		{"Unauthorized", "INFO", func(t *testing.T) *httptest.ResponseRecorder {
			srv, _, _ := newTestServer(t)
			req := httptest.NewRequest("GET", "/health", nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			return rec
		}},
		{"WriteQueueFull", "WARNING", func(t *testing.T) *httptest.ResponseRecorder {
			srv, tm, _ := newTestServerWith(t, testOptions{maxQueued: 1})
			release := holdTurn(t, tm)
			queued := make(chan struct{})
			go func() { doRequest(t, srv, "DELETE", "/projections", ""); close(queued) }()
			waitQueued(t, tm, 1)
			rec := doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","payload":""}]}`)
			release()
			<-queued
			return rec
		}},
		{"ShuttingDown", "INFO", func(t *testing.T) *httptest.ResponseRecorder {
			srv, tm, _ := newTestServer(t)
			tm.Close() // the FIFO now turns every write away
			return doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","payload":""}]}`)
		}},
		{"InternalError", "ERROR", func(t *testing.T) *httptest.ResponseRecorder {
			srv, _, st := newTestServer(t)
			st.Close() // the write gets its turn, then fails to open the SQLite transaction
			return doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","payload":""}]}`)
		}},
		{"Unavailable", "ERROR", func(t *testing.T) *httptest.ResponseRecorder {
			srv, _, st := newTestServer(t)
			st.Close()
			return doRequest(t, srv, "GET", "/health", "")
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := captureLog(t)
			rec := tt.serve(t)
			if tt.name != "success" && tt.name != "Unauthorized" && errorCode(t, rec) != tt.name {
				t.Fatalf("error = %q, want %q (body %s)", errorCode(t, rec), tt.name, rec.Body.String())
			}
			// Setup requests log their own lines around the request under
			// test: find its line by level and status code.
			want := "[" + tt.level + "] "
			found := false
			for _, line := range strings.Split(buf.String(), "\n") {
				if strings.Contains(line, want) && strings.Contains(line, fmt.Sprintf(" %d ", rec.Code)) {
					found = true
				}
			}
			if !found {
				t.Errorf("log output = %q, want a [%s] line with status %d", buf.String(), tt.level, rec.Code)
			}
		})
	}
}

func TestAccessLogBelowThresholdIsSuppressed(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{logLevel: "error"})
	buf := captureLog(t)

	doRequest(t, srv, "GET", "/health", "")               // success, DEBUG
	doRequest(t, srv, "QUERY", "/events", `{"query":[]}`) // 400, INFO
	provokeConflict(t, srv)                               // 409, DEBUG

	if buf.Len() != 0 {
		t.Errorf("log output = %q, want empty (everything above is below the \"error\" threshold)", buf.String())
	}
}

func TestAccessLogAtOrAboveThresholdIsLogged(t *testing.T) {
	srv, tm, _ := newTestServerWith(t, testOptions{logLevel: "warning", maxQueued: 1})
	release := holdTurn(t, tm)
	queued := make(chan int, 1)
	go func() { queued <- doRequest(t, srv, "DELETE", "/projections", "").Code }()
	waitQueued(t, tm, 1)
	buf := captureLog(t)

	doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","payload":""}]}`) // 503 WriteQueueFull, WARNING
	release()
	<-queued                // 204, DEBUG
	provokeConflict(t, srv) // 200 then 409, DEBUG

	out := buf.String()
	if !strings.Contains(out, "[WARNING]") {
		t.Errorf("log output = %q, want it to contain [WARNING]", out)
	}
	if got := strings.Count(out, "\n"); got != 1 {
		t.Errorf("log output has %d lines, want exactly 1 (only the WriteQueueFull one): %q", got, out)
	}
}
