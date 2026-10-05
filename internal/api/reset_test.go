package api

import "testing"

func TestResetDeletesEventsAndProjections(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{devMode: true})
	commitEvents(t, srv, `[{"type":"t","payload":""}]`)
	writeProjectionsCommitted(t, srv, `{"create":[{"type":"user-profile","id":"123","payload":"x"}]}`)

	rec := doRequest(t, srv, "POST", "/reset", "")
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %q, want 204 with no body", rec.Code, rec.Body.String())
	}

	if _, events := parseNDJSON(t, doRequest(t, srv, "QUERY", "/events", `{"query":"all"}`).Body.String()); len(events) != 0 {
		t.Errorf("events after reset = %+v, want none", events)
	}
	if rec := doRequest(t, srv, "GET", "/projections/user-profile/123", ""); rec.Code != 404 {
		t.Errorf("projection after reset status = %d, want 404", rec.Code)
	}
	commitEvents(t, srv, `[{"type":"t","payload":""}]`)
	if _, events := parseNDJSON(t, doRequest(t, srv, "QUERY", "/events", `{"query":"all"}`).Body.String()); events[0].Sequence != 1 {
		t.Errorf("first sequence after reset = %d, want 1", events[0].Sequence)
	}
}

// TestResetWaitsForItsTurn checks that POST /reset queues like a write:
// the write ahead of it goes through, then the reset deletes it.
func TestResetWaitsForItsTurn(t *testing.T) {
	srv, wr, _ := newTestServerWith(t, testOptions{devMode: true})
	release := holdTurn(t, wr)

	written := make(chan int, 1)
	go func() {
		written <- doRequest(t, srv, "POST", "/projections", `{"create":[{"type":"p","id":"1","payload":""}]}`).Code
	}()
	waitQueued(t, wr, 1)
	reset := make(chan int, 1)
	go func() { reset <- doRequest(t, srv, "POST", "/reset", "").Code }()
	waitQueued(t, wr, 2)

	release()
	if code := <-written; code != 200 {
		t.Errorf("POST /projections status = %d, want 200: it was queued before the reset", code)
	}
	if code := <-reset; code != 204 {
		t.Fatalf("POST /reset status = %d, want 204", code)
	}
	getProjection(t, srv, "p", "1", 404)
}

// TestResetNotRegisteredWithoutDevMode confirms POST /reset doesn't exist
// when DevMode is false.
func TestResetNotRegisteredWithoutDevMode(t *testing.T) {
	srv, _, _ := newTestServer(t)
	if rec := doRequest(t, srv, "POST", "/reset", ""); rec.Code != 404 {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}
