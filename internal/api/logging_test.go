package api

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// logBuffer is a bytes.Buffer safe to read while another goroutine logs
// into it (the deadline timer, for an expired transaction).
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

const conflictBody = `{"events":[{"type":"t","identifiers":{"userId":"999"},"metadata":{},"payload":""}],
	"condition":{"failIfEventsMatch":[{"identifiers":[{"name":"userId","value":"123"}]}],"afterSequence":0}}`

// provokeConflict commits one event, then makes a 409 ConcurrencyException
// append against it.
func provokeConflict(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	appendCommitted(t, srv, `{"events":[{"type":"t","identifiers":{"userId":"123"},"metadata":{},"payload":""}]}`)
	return doTicketRequest(t, srv, "POST", "/events", begin(t, srv), conflictBody)
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
		{"DocumentNotFound", "DEBUG", func(t *testing.T) *httptest.ResponseRecorder {
			srv, _, _ := newTestServer(t)
			return doRequest(t, srv, "GET", "/documents/user-profile/123", "")
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
			return doTicketRequest(t, srv, "POST", "/events", begin(t, srv), body)
		}},
		{"Unauthorized", "INFO", func(t *testing.T) *httptest.ResponseRecorder {
			srv, _, _ := newTestServer(t)
			req := httptest.NewRequest("GET", "/health", nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			return rec
		}},
		{"NotPaused", "INFO", func(t *testing.T) *httptest.ResponseRecorder {
			srv, _, _ := newTestServer(t)
			return doRequest(t, srv, "DELETE", "/documents", "")
		}},
		{"TransactionNotActive", "INFO", func(t *testing.T) *httptest.ResponseRecorder {
			srv, _, _ := newTestServer(t)
			return doTicketRequest(t, srv, "POST", "/commit", "not-a-ticket", "")
		}},
		{"TransactionQueueFull", "WARNING", func(t *testing.T) *httptest.ResponseRecorder {
			srv, tm, _ := newTestServerWith(t, testOptions{maxQueued: 1})
			holder := begin(t, srv)
			queued := make(chan string, 1)
			go func() {
				ticket, _ := tm.Begin(context.Background())
				queued <- ticket
			}()
			time.Sleep(50 * time.Millisecond)
			rec := doRequest(t, srv, "POST", "/begin", "")
			commit(t, srv, holder)
			if ticket := <-queued; ticket != "" {
				tm.Rollback(ticket)
			}
			return rec
		}},
		{"TransactionWaitTimeout", "WARNING", func(t *testing.T) *httptest.ResponseRecorder {
			srv, _, _ := newTestServerWith(t, testOptions{maxWait: 20 * time.Millisecond})
			holder := begin(t, srv)
			defer commit(t, srv, holder)
			return doRequest(t, srv, "POST", "/begin", "")
		}},
		{"Paused", "WARNING", func(t *testing.T) *httptest.ResponseRecorder {
			srv, _, _ := newTestServer(t)
			pause(t, srv)
			return doRequest(t, srv, "POST", "/begin", "")
		}},
		{"InternalError", "ERROR", func(t *testing.T) *httptest.ResponseRecorder {
			srv, tm, _ := newTestServer(t)
			tm.Close() // Begin now fails with a closed FIFO, handleErr's default case
			return doRequest(t, srv, "POST", "/begin", "")
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
	srv, _, _ := newTestServerWith(t, testOptions{logLevel: "warning"})
	buf := captureLog(t)

	pause(t, srv)
	doRequest(t, srv, "POST", "/begin", "") // 503 Paused, WARNING
	doRequest(t, srv, "POST", "/resume", "")
	provokeConflict(t, srv) // 409, DEBUG, below "warning"

	out := buf.String()
	if !strings.Contains(out, "[WARNING]") {
		t.Errorf("log output = %q, want it to contain [WARNING]", out)
	}
	if got := strings.Count(out, "\n"); got != 1 {
		t.Errorf("log output has %d lines, want exactly 1 (only the Paused one): %q", got, out)
	}
}

func TestExpiredTransactionIsLogged(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{logLevel: "warning", timeout: 30 * time.Millisecond, ceiling: time.Second})
	buf := captureLog(t)

	begin(t, srv)
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), "transaction expired") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if out := buf.String(); !strings.Contains(out, "[WARNING] transaction expired: idle timeout reached after") {
		t.Errorf("log output = %q, want a WARNING line for the expired transaction", out)
	}
}
