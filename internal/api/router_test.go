package api

import "testing"

// TestWrongMethodReturns405 confirms that not registering a catch-all "/"
// route preserves ServeMux's built-in method-mismatch behavior: a known
// path with the wrong method gets 405 + Allow, not swallowed into a 404.
func TestWrongMethodReturns405(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doRequest(t, srv, "PUT", "/health", "")
	if rec.Code != 405 {
		t.Fatalf("status = %d, want 405, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Allow") == "" {
		t.Errorf("Allow header is empty, want it set")
	}
}

func TestUnknownPathReturns404(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doRequest(t, srv, "GET", "/nonexistent", "")
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}
