package api

import (
	"bytes"
	"context"
	"log"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// captureLog redirects the standard logger's output for the duration of the
// test and returns the buffer withLogging writes into.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// newTestServerWithLogLevel is newTestServer with an explicit logLevel and
// a write-admission queue capped at 1, for tests that need to pick a
// non-default threshold or provoke AppendQueueFull.
func newTestServerWithLogLevel(t *testing.T, logLevel string) (*Server, *queue.Manager, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })
	qm := queue.New(1)
	t.Cleanup(qm.Close)
	srv := New(qm, st, Options{
		EnableAuth:           true,
		AuthToken:            testToken,
		DefaultLimit:         1000,
		MaxLimit:             10000,
		MaxEventSize:         65536,
		MaxDocumentSize:      65536,
		MaxDocumentsPerWrite: 100,
		LogLevel:             logLevel,
	})
	return srv, qm, st
}

func TestAccessLogLevelPerOutcome(t *testing.T) {
	tests := []struct {
		name  string
		level string
		serve func(t *testing.T) *httptest.ResponseRecorder
	}{
		{
			name:  "success",
			level: "DEBUG",
			serve: func(t *testing.T) *httptest.ResponseRecorder {
				srv, _, _ := newTestServer(t)
				return doRequest(t, srv, "GET", "/health", "")
			},
		},
		{
			name:  "DocumentNotFound",
			level: "DEBUG",
			serve: func(t *testing.T) *httptest.ResponseRecorder {
				srv, _, _ := newTestServer(t)
				return doRequest(t, srv, "GET", "/documents/user-profile/123", "")
			},
		},
		{
			name:  "ConcurrencyException",
			level: "DEBUG",
			serve: func(t *testing.T) *httptest.ResponseRecorder {
				srv, _, _ := newTestServer(t)
				doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","identifiers":{"userId":"123"},"metadata":{},"payload":""}]}`)
				body := `{"events":[{"type":"t","identifiers":{"userId":"999"},"metadata":{},"payload":""}],
					"condition":{"failIfEventsMatch":[{"identifiers":[{"name":"userId","value":"123"}]}],"afterSequence":0}}`
				return doRequest(t, srv, "POST", "/write", body)
			},
		},
		{
			name:  "InvalidRequest",
			level: "INFO",
			serve: func(t *testing.T) *httptest.ResponseRecorder {
				srv, _, _ := newTestServer(t)
				return doRequest(t, srv, "POST", "/write", `{"events":[]}`)
			},
		},
		{
			name:  "PayloadTooLarge",
			level: "INFO",
			serve: func(t *testing.T) *httptest.ResponseRecorder {
				srv, _, _ := newTestServer(t)
				hugePayload := strings.Repeat("x", 70000) // over the 65536 test-server MaxEventSize
				body := `{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":"` + hugePayload + `"}]}`
				return doRequest(t, srv, "POST", "/write", body)
			},
		},
		{
			name:  "Unauthorized",
			level: "INFO",
			serve: func(t *testing.T) *httptest.ResponseRecorder {
				srv, _, _ := newTestServer(t)
				req := httptest.NewRequest("GET", "/health", nil)
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, req)
				return rec
			},
		},
		{
			name:  "AppendQueueFull",
			level: "WARNING",
			serve: func(t *testing.T) *httptest.ResponseRecorder {
				srv, qm, _ := newTestServerWithMaxQueued(t, 1)
				active, err := qm.Join(context.Background())
				if err != nil {
					t.Fatalf("Join() error = %v", err)
				}
				queuedDone := make(chan struct{})
				go func() {
					ticket, err := qm.Join(context.Background())
					if err == nil {
						ticket.Done()
					}
					close(queuedDone)
				}()
				time.Sleep(50 * time.Millisecond) // let the goroutine occupy the one queue slot
				defer func() {
					active.Done()
					<-queuedDone
				}()
				return doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":""}]}`)
			},
		},
		{
			name:  "InternalError",
			level: "ERROR",
			serve: func(t *testing.T) *httptest.ResponseRecorder {
				srv, qm, _ := newTestServer(t)
				qm.Close() // Join now fails with queue.ErrClosed, handleErr's default case
				return doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":""}]}`)
			},
		},
		{
			name:  "Unavailable",
			level: "ERROR",
			serve: func(t *testing.T) *httptest.ResponseRecorder {
				srv, _, st := newTestServer(t)
				st.Close()
				return doRequest(t, srv, "GET", "/health", "")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := captureLog(t)
			tt.serve(t)
			tag := "[" + tt.level + "]"
			if !strings.Contains(buf.String(), tag) {
				t.Errorf("log output = %q, want it to contain %q", buf.String(), tag)
			}
		})
	}
}

func TestAccessLogBelowThresholdIsSuppressed(t *testing.T) {
	srv, _, _ := newTestServerWithLogLevel(t, "error")
	buf := captureLog(t)

	doRequest(t, srv, "GET", "/health", "") // success, DEBUG

	doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","identifiers":{"userId":"123"},"metadata":{},"payload":""}]}`)
	conflictBody := `{"events":[{"type":"t","identifiers":{"userId":"999"},"metadata":{},"payload":""}],
		"condition":{"failIfEventsMatch":[{"identifiers":[{"name":"userId","value":"123"}]}],"afterSequence":0}}`
	doRequest(t, srv, "POST", "/write", conflictBody) // 409, DEBUG

	if buf.Len() != 0 {
		t.Errorf("log output = %q, want empty (everything above is below the \"error\" threshold)", buf.String())
	}
}

func TestAccessLogAtOrAboveThresholdIsLogged(t *testing.T) {
	srv, qm, _ := newTestServerWithLogLevel(t, "warning")
	buf := captureLog(t)

	active, err := qm.Join(context.Background())
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	queuedDone := make(chan struct{})
	go func() {
		ticket, err := qm.Join(context.Background())
		if err == nil {
			ticket.Done()
		}
		close(queuedDone)
	}()
	time.Sleep(50 * time.Millisecond)
	doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":""}]}`) // AppendQueueFull, WARNING
	active.Done()
	<-queuedDone

	doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","identifiers":{"userId":"123"},"metadata":{},"payload":""}]}`)
	conflictBody := `{"events":[{"type":"t","identifiers":{"userId":"999"},"metadata":{},"payload":""}],
		"condition":{"failIfEventsMatch":[{"identifiers":[{"name":"userId","value":"123"}]}],"afterSequence":0}}`
	doRequest(t, srv, "POST", "/write", conflictBody) // 409, DEBUG, below "warning"

	out := buf.String()
	if !strings.Contains(out, "[WARNING]") {
		t.Errorf("log output = %q, want it to contain [WARNING]", out)
	}
	if got := strings.Count(out, "\n"); got != 1 {
		t.Errorf("log output has %d lines, want exactly 1 (only the AppendQueueFull one): %q", got, out)
	}
}
