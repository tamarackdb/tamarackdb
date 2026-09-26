package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/store"
)

// TicketHeader carries the ticket of the transaction a call belongs to.
const TicketHeader = "X-Tamarackdb-Ticket"

type beginResponse struct {
	Ticket string `json:"ticket"`
}

// ticketFrom returns the request's ticket, and whether it carries one.
func ticketFrom(r *http.Request) (string, bool) {
	ticket := r.Header.Get(TicketHeader)
	return ticket, ticket != ""
}

// requireTicket returns the request's ticket, or writes 400 and returns
// ok=false when it carries none.
func (s *Server) requireTicket(w http.ResponseWriter, r *http.Request) (string, bool) {
	ticket, ok := ticketFrom(r)
	if !ok {
		s.handleErr(w, r, errMissingTicketValidation)
	}
	return ticket, ok
}

// doInTx runs fn inside ticket's transaction (see txn.Manager.Do), with the
// connection's read and write deadlines set to the transaction's ceiling.
// A call holds the write lock while it runs, and nothing can cut it short:
// a client that stops reading a streamed page, or sends its body very
// slowly, would otherwise hold the lock with no bound. With the deadlines,
// such a call fails at the ceiling at the latest, which rolls the
// transaction back. The deadlines are cleared when fn returns, so they
// never carry over to the next request on the same connection.
func (s *Server) doInTx(w http.ResponseWriter, ticket string, fn func(tx *store.Tx) error) error {
	if ceiling, ok := s.tm.Ceiling(ticket); ok {
		rc := http.NewResponseController(w)
		// Setting a deadline fails only when the ResponseWriter doesn't
		// support it (a test recorder): there's no connection to bound.
		_ = rc.SetReadDeadline(ceiling)
		_ = rc.SetWriteDeadline(ceiling)
		defer func() {
			_ = rc.SetReadDeadline(time.Time{})
			_ = rc.SetWriteDeadline(time.Time{})
		}()
	}
	return s.tm.Do(ticket, fn)
}

// trackWrite counts a request on the write side for GET /debug. Callers
// defer the returned function.
func (s *Server) trackWrite() func() {
	s.writeHTTPOpen.Add(1)
	return func() { s.writeHTTPOpen.Add(-1) }
}

// handleBegin implements POST /begin: it waits for a turn in the FIFO,
// then responds with the new transaction's ticket. It takes no request
// body.
func (s *Server) handleBegin(w http.ResponseWriter, r *http.Request) {
	defer s.trackWrite()()
	ticket, err := s.tm.Begin(r.Context())
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(beginResponse{Ticket: ticket})
}

// handleCommit implements POST /commit.
func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	defer s.trackWrite()()
	ticket, ok := s.requireTicket(w, r)
	if !ok {
		return
	}
	if err := s.tm.Commit(ticket); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRollback implements POST /rollback.
func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	defer s.trackWrite()()
	ticket, ok := s.requireTicket(w, r)
	if !ok {
		return
	}
	if err := s.tm.Rollback(ticket); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePause implements POST /pause: it waits for its turn in the FIFO,
// behind every transaction already queued, then pauses the server.
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	defer s.trackWrite()()
	if err := s.tm.Pause(r.Context()); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleResume implements POST /resume. It doesn't join the FIFO.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if err := s.tm.Resume(); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleReset implements POST /reset, deleting every event and document
// and cutting off the active transaction, if any (see
// txn.Manager.Reset). Only registered by New when Options.DevMode is true.
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	defer s.trackWrite()()
	if err := s.tm.Reset(r.Context()); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
