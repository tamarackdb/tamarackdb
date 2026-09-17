package api

import "net/http"

// handleReset implements DELETE /, wiping every event in the database via
// store.Truncate. Only registered by New when Options.DevMode is true. It
// joins the same FIFO write-admission queue as POST /append, so a wipe can
// no longer land mid-append or an append mid-wipe.
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
