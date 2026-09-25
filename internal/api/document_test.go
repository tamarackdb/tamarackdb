package api

import (
	"encoding/json"
	"testing"
)

func TestGetDocumentNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
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

func TestDeleteDocumentsByType(t *testing.T) {
	srv, _, _ := newTestServer(t)

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

func TestDeleteAllDocuments(t *testing.T) {
	srv, _, _ := newTestServer(t)

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
