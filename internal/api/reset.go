package api

import "net/http"

// handleReset implements POST /reset: it deletes every event and
// projection and draws a new store ID, in its turn in the FIFO (see
// txn.Manager.Reset). Only registered by New when Options.DevMode is true.
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if err := s.tm.Reset(r.Context()); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
