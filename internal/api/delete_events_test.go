package api

import "testing"

func TestDeleteEventsKeepsProjectionsAndPositions(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{devMode: true})
	commitEvents(t, srv, `[{"type":"t","payload":""}]`)
	writeProjectionsCommitted(t, srv, `{"create":[{"type":"user-profile","id":"123","payload":"x"}]}`)

	rec := doRequest(t, srv, "DELETE", "/events", "")
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %q, want 204 with no body", rec.Code, rec.Body.String())
	}

	if n := countEvents(t, srv); n != 0 {
		t.Errorf("events after DELETE /events = %d, want none", n)
	}
	getProjection(t, srv, "user-profile", "123", 200)
	commitEvents(t, srv, `[{"type":"t","payload":""}]`)
	if _, events := parseNDJSON(t, doRequest(t, srv, "QUERY", "/events", `{"query":"all"}`).Body.String()); events[0].Sequence != 2 {
		t.Errorf("first sequence after DELETE /events = %d, want 2", events[0].Sequence)
	}
}

// TestDeleteEventsLeavesTransactionsOpen checks that DELETE /events waits
// for no transaction, needs no pause, and that an open transaction goes on
// and commits after it.
func TestDeleteEventsLeavesTransactionsOpen(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{devMode: true})
	commitEvents(t, srv, `[{"type":"t","payload":""}]`)
	tx := begin(t, srv)
	txRequest(t, srv, "QUERY", tx+"/events", `{"query":"none"}`, 200)

	if rec := doRequest(t, srv, "DELETE", "/events", ""); rec.Code != 204 {
		t.Fatalf("DELETE /events status = %d, body = %s, want 204", rec.Code, rec.Body.String())
	}

	txRequest(t, srv, "POST", tx+"/events", `{"events":[{"type":"t","payload":""}]}`, 200)
	txRequest(t, srv, "POST", tx+"/commit", "", 204)
	if n := countEvents(t, srv); n != 1 {
		t.Errorf("events after the commit = %d, want 1", n)
	}
}

// TestDeleteEventsKeepsThePause checks that DELETE /events runs during a
// pause, and leaves it in place.
func TestDeleteEventsKeepsThePause(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{devMode: true})
	if rec := doRequest(t, srv, "POST", "/pause", ""); rec.Code != 200 {
		t.Fatalf("POST /pause status = %d", rec.Code)
	}
	if rec := doRequest(t, srv, "DELETE", "/events", ""); rec.Code != 204 {
		t.Fatalf("DELETE /events status = %d, want 204", rec.Code)
	}
	if !healthPaused(t, srv) {
		t.Error("GET /health paused = false after DELETE /events")
	}
}

// TestDeleteEventsWaitsForItsTurn checks that DELETE /events queues like a
// write: the write ahead of it goes through, then it deletes the events.
func TestDeleteEventsWaitsForItsTurn(t *testing.T) {
	srv, wr, _ := newTestServerWith(t, testOptions{devMode: true})
	commitEvents(t, srv, `[{"type":"t","payload":""}]`)
	release := holdTurn(t, wr)

	written := make(chan int, 1)
	go func() {
		written <- doRequest(t, srv, "POST", "/projections", `{"create":[{"type":"p","id":"1","payload":""}]}`).Code
	}()
	waitQueued(t, wr, 1)
	deleted := make(chan int, 1)
	go func() { deleted <- doRequest(t, srv, "DELETE", "/events", "").Code }()
	waitQueued(t, wr, 2)

	if n := countEvents(t, srv); n != 1 {
		t.Fatalf("events while DELETE /events waits = %d, want 1", n)
	}
	release()
	if code := <-written; code != 200 {
		t.Errorf("POST /projections status = %d, want 200: it was queued first", code)
	}
	if code := <-deleted; code != 204 {
		t.Fatalf("DELETE /events status = %d, want 204", code)
	}
	if n := countEvents(t, srv); n != 0 {
		t.Errorf("events after DELETE /events = %d, want none", n)
	}
}

// TestDeleteEventsNotRegisteredWithoutDevMode confirms DELETE /events
// doesn't exist when DevMode is false: the path answers 405, since
// QUERY /events exists.
func TestDeleteEventsNotRegisteredWithoutDevMode(t *testing.T) {
	srv, _, _ := newTestServer(t)
	if rec := doRequest(t, srv, "DELETE", "/events", ""); rec.Code != 405 {
		t.Fatalf("status = %d, want 405, body = %s", rec.Code, rec.Body.String())
	}
}
