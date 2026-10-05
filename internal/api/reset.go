package api

import "net/http"

// handleReset implements POST /reset: during a pause, it deletes every
// event and projection and draws a new store ID, in its turn in the FIFO
// (see tx.Registry.Reset). Only registered by New when Options.DevMode is
// true.
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if err := s.txs.Reset(r.Context()); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
