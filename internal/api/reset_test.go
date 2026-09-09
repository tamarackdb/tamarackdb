package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/gatekeeper"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// newDevModeTestServer is newTestServer with Options.DevMode set to true, so
// DELETE / is registered.
func newDevModeTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })
	gk := gatekeeper.New()
	t.Cleanup(gk.Close)
	srv := New(gk, st, Options{
		EnableAuth:   true,
		AuthToken:    testToken,
		DefaultLimit: 1000,
		MaxLimit:     10000,
		MaxEventSize: 65536,
		DevMode:      true,
	})
	return srv, st
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
