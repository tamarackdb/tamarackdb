package api

import (
	"encoding/json"
	"testing"
)

func TestHealthSuccess(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doRequest(t, srv, "GET", "/health", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status field = %q, want %q", resp.Status, "ok")
	}
}

func TestHealthFailure(t *testing.T) {
	srv, _, st := newTestServer(t)
	st.Close()

	rec := doRequest(t, srv, "GET", "/health", "")
	if rec.Code != 503 {
		t.Fatalf("status = %d, want 503, body = %s", rec.Code, rec.Body.String())
	}
}
