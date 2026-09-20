package api

import "net/http"

// handleReset implements DELETE /events, wiping every event via
// store.Truncate; documents are never touched. Only registered by New when
// Options.DevMode is true. It joins the same FIFO write-admission queue as
// POST /write, so a wipe can no longer land mid-write or a write mid-wipe.
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	ticket, ok := s.joinWriteQueue(w, r)
	if !ok {
		return
	}
	defer ticket.Done()

	if err := s.st.Truncate(r.Context()); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
