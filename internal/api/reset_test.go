package api

import "testing"

func TestResetDeletesEventsAndDocuments(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{devMode: true})
	appendCommitted(t, srv, `{"events":[{"type":"t","payload":""}]}`)
	writeDocumentsCommitted(t, srv, `{"documents":[{"type":"user-profile","id":"123","payload":"x"}]}`)

	rec := doRequest(t, srv, "POST", "/reset", "")
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %q, want 204 with no body", rec.Code, rec.Body.String())
	}

	if _, events := parseNDJSON(t, doRequest(t, srv, "QUERY", "/events", `{"query":"*"}`).Body.String()); len(events) != 0 {
		t.Errorf("events after reset = %+v, want none", events)
	}
	if rec := doRequest(t, srv, "GET", "/documents/user-profile/123", ""); rec.Code != 404 {
		t.Errorf("document after reset status = %d, want 404", rec.Code)
	}
	if resp := appendCommitted(t, srv, `{"events":[{"type":"t","payload":""}]}`); resp.Events[0].Sequence != 1 {
		t.Errorf("first sequence after reset = %d, want 1", resp.Events[0].Sequence)
	}
}

func TestResetCutsOffTheActiveTransaction(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{devMode: true})
	ticket := begin(t, srv)
	if rec := doRequest(t, srv, "POST", "/reset", ""); rec.Code != 204 {
		t.Fatalf("reset status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := doTicketRequest(t, srv, "POST", "/commit", ticket, ""); rec.Code != 410 {
		t.Errorf("commit after reset status = %d, want 410", rec.Code)
	}
}

// TestResetNotRegisteredWithoutDevMode confirms POST /reset doesn't exist
// when DevMode is false.
func TestResetNotRegisteredWithoutDevMode(t *testing.T) {
	srv, _, _ := newTestServer(t)
	if rec := doRequest(t, srv, "POST", "/reset", ""); rec.Code != 404 {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}
