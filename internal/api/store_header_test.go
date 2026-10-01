package api

import (
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// storeHeader checks that rec carries a UUID in the X-Tamarackdb-Store
// header, and returns it.
func storeHeader(t *testing.T, rec *httptest.ResponseRecorder, what string) string {
	t.Helper()
	id := rec.Header().Get(StoreHeader)
	if err := uuid.Validate(id); err != nil {
		t.Fatalf("%s: %s = %q, want a UUID", what, StoreHeader, id)
	}
	return id
}

// TestReadsCarryTheStoreHeader checks that every read without a ticket
// carries the same store ID, on an empty page, a full page, a found
// projection, and a missing one, and that POST /reset changes it.
func TestReadsCarryTheStoreHeader(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{devMode: true})

	id := storeHeader(t, doRequest(t, srv, "QUERY", "/events", `{"query":"*"}`), "empty page")
	appendCommitted(t, srv, `{"events":[{"type":"t","payload":""}]}`)
	writeProjectionsCommitted(t, srv, `{"create":[{"type":"user-profile","id":"123","payload":"x"}]}`)

	for _, tt := range []struct {
		what, method, path, body string
		wantCode                 int
	}{
		{"full page", "QUERY", "/events", `{"query":"*"}`, 200},
		{"found projection", "GET", "/projections/user-profile/123", "", 200},
		{"missing projection", "GET", "/projections/user-profile/456", "", 404},
	} {
		rec := doRequest(t, srv, tt.method, tt.path, tt.body)
		if rec.Code != tt.wantCode {
			t.Fatalf("%s: status = %d, want %d, body = %s", tt.what, rec.Code, tt.wantCode, rec.Body.String())
		}
		if got := storeHeader(t, rec, tt.what); got != id {
			t.Errorf("%s: %s = %q, want %q", tt.what, StoreHeader, got, id)
		}
	}

	if rec := doRequest(t, srv, "POST", "/reset", ""); rec.Code != 204 {
		t.Fatalf("reset status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := storeHeader(t, doRequest(t, srv, "QUERY", "/events", `{"query":"*"}`), "after reset"); got == id {
		t.Errorf("%s after reset = %q, want a new store ID", StoreHeader, got)
	}
}

// TestReadErrorHasNoStoreHeader checks that a read refused before it runs
// says nothing about the store.
func TestReadErrorHasNoStoreHeader(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doRequest(t, srv, "QUERY", "/events", `{"query":"*","limit":0}`)
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := rec.Header().Get(StoreHeader); got != "" {
		t.Errorf("%s = %q, want none", StoreHeader, got)
	}
}
