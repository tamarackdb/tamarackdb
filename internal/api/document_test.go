package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

func TestGetDocumentNotFound(t *testing.T) {
	srv, _, _ := newTestServerWithDocuments(t)
	rec := doRequest(t, srv, "GET", "/documents/user-profile/123", "")
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env.Error != "DocumentNotFound" {
		t.Errorf("error = %q, want DocumentNotFound", env.Error)
	}
}

// TestGetDocumentNotReady sets up a document whose metadata exists but
// whose payload row lags behind (deleted out from under it, bypassing
// the HTTP layer), the same scenario a real best-effort payload write
// failure produces, to confirm the 503 + Retry-After mapping.
func TestGetDocumentNotReady(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })
	docPath := filepath.Join(t.TempDir(), "documents.db")
	if err := st.OpenDocuments(context.Background(), docPath, 0); err != nil {
		t.Fatalf("OpenDocuments() error = %v", err)
	}
	qm := queue.New(0)
	t.Cleanup(qm.Close)
	srv := New(qm, st, Options{
		EnableAuth: true, AuthToken: testToken, DefaultLimit: 1000, MaxLimit: 10000,
		MaxEventSize: 65536, MaxDocumentSize: 65536, MaxDocumentsPerWrite: 100,
		LogLevel: "debug",
	})

	create := doRequest(t, srv, "POST", "/write", `{"documents":[{"type":"user-profile","id":"123","payload":"hello"}]}`)
	if create.Code != 200 {
		t.Fatalf("create status = %d, body = %s", create.Code, create.Body.String())
	}

	// Simulate a payload write that never landed, by reaching straight
	// into tamarackdb-documents.sqlite (the sqlite driver is already
	// registered process-wide via internal/store's blank import).
	docDB, err := sql.Open("sqlite", docPath)
	if err != nil {
		t.Fatalf("open documents db: %v", err)
	}
	defer docDB.Close()
	if _, err := docDB.ExecContext(context.Background(),
		"DELETE FROM documents_payload WHERE type = 'user-profile' AND id = '123'"); err != nil {
		t.Fatalf("delete payload row: %v", err)
	}

	rec := doRequest(t, srv, "GET", "/documents/user-profile/123", "")
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
	if env.Error != "DocumentNotReady" {
		t.Errorf("error = %q, want DocumentNotReady", env.Error)
	}
}

func TestDeleteDocumentsByTypeErrorWhenDocumentsNotOpen(t *testing.T) {
	srv, _, _ := newTestServer(t) // no OpenDocuments call
	rec := doRequest(t, srv, "DELETE", "/documents/user-profile", "")
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteDocumentsByType(t *testing.T) {
	srv, _, _ := newTestServerWithDocuments(t)

	create := doRequest(t, srv, "POST", "/write", `{"documents":[
		{"type":"user-profile","id":"1","payload":"a"},
		{"type":"user-profile","id":"2","payload":"b"},
		{"type":"user-list","id":"user-list","payload":"c"}
	]}`)
	if create.Code != 200 {
		t.Fatalf("create status = %d, body = %s", create.Code, create.Body.String())
	}

	del := doRequest(t, srv, "DELETE", "/documents/user-profile", "")
	if del.Code != 204 {
		t.Fatalf("delete status = %d, want 204, body = %s", del.Code, del.Body.String())
	}

	for _, id := range []string{"1", "2"} {
		rec := doRequest(t, srv, "GET", "/documents/user-profile/"+id, "")
		if rec.Code != 404 {
			t.Errorf("GET /documents/user-profile/%s status = %d, want 404", id, rec.Code)
		}
	}
	rec := doRequest(t, srv, "GET", "/documents/user-list/user-list", "")
	if rec.Code != 200 {
		t.Errorf("GET /documents/user-list/user-list status = %d, want 200", rec.Code)
	}
}

func TestDeleteAllDocumentsErrorWhenDocumentsNotOpen(t *testing.T) {
	srv, _, _ := newTestServer(t) // no OpenDocuments call
	rec := doRequest(t, srv, "DELETE", "/documents", "")
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteAllDocuments(t *testing.T) {
	srv, _, _ := newTestServerWithDocuments(t)

	create := doRequest(t, srv, "POST", "/write", `{"documents":[
		{"type":"user-profile","id":"1","payload":"a"},
		{"type":"user-list","id":"user-list","payload":"c"}
	]}`)
	if create.Code != 200 {
		t.Fatalf("create status = %d, body = %s", create.Code, create.Body.String())
	}

	del := doRequest(t, srv, "DELETE", "/documents", "")
	if del.Code != 204 {
		t.Fatalf("delete status = %d, want 204, body = %s", del.Code, del.Body.String())
	}

	for _, path := range []string{"/documents/user-profile/1", "/documents/user-list/user-list"} {
		rec := doRequest(t, srv, "GET", path, "")
		if rec.Code != 404 {
			t.Errorf("GET %s status = %d, want 404", path, rec.Code)
		}
	}
}
