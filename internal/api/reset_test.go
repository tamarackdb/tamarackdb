package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// newDevModeTestServer is newTestServer with Options.DevMode set to true, so
// DELETE / is registered.
func newDevModeTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	srv, _, st := newDevModeTestServerWithMaxQueued(t, 0)
	return srv, st
}

func newDevModeTestServerWithMaxQueued(t *testing.T, maxQueued int) (*Server, *queue.Manager, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })
	qm := queue.New(maxQueued)
	t.Cleanup(qm.Close)
	srv := New(qm, st, Options{
		EnableAuth:   true,
		AuthToken:    testToken,
		DefaultLimit: 1000,
		MaxLimit:     10000,
		MaxEventSize: 65536,
		DevMode:      true,
	})
	return srv, qm, st
}

func TestResetWipesDatabase(t *testing.T) {
	srv, _ := newDevModeTestServer(t)

	appendRec := doRequest(t, srv, "POST", "/append", `{"events":[{"type":"t","payload":""}]}`)
	if appendRec.Code != 200 {
		t.Fatalf("append status = %d, body = %s", appendRec.Code, appendRec.Body.String())
	}

	rec := doRequest(t, srv, "DELETE", "/", "")
	if rec.Code != 204 {
		t.Fatalf("status = %d, want 204, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}

	readRec := doRequest(t, srv, "QUERY", "/read", `{"query":"*"}`)
	if readRec.Code != 200 {
		t.Fatalf("read status = %d, body = %s", readRec.Code, readRec.Body.String())
	}
	_, events := parseNDJSON(t, readRec.Body.String())
	if len(events) != 0 {
		t.Errorf("events after reset = %+v, want none", events)
	}
}

func TestResetNotRegisteredWithoutDevMode(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doRequest(t, srv, "DELETE", "/", "")
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404 (DELETE / must not be registered when DevMode is false), body = %s", rec.Code, rec.Body.String())
	}
}

func TestResetReturns503WhenQueueFull(t *testing.T) {
	srv, qm, _ := newDevModeTestServerWithMaxQueued(t, 1)

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

	rec := doRequest(t, srv, "DELETE", "/", "")
	if rec.Code != 503 {
		t.Fatalf("status = %d, want 503, body = %s", rec.Code, rec.Body.String())
	}
	if ra := rec.Header().Get("Retry-After"); ra != "1" {
		t.Errorf("Retry-After = %q, want \"1\"", ra)
	}
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env.Error != "AppendQueueFull" {
		t.Errorf("error = %q, want AppendQueueFull", env.Error)
	}
}
