package api

import "net/http"

// handleOptimize implements POST /optimize: PRAGMA optimize, in its turn in
// the FIFO (see writer.Writer.Optimize). The server never runs it on a
// timer of its own: an operator's timer calls this endpoint, so the server
// keeps no goroutine for it.
func (s *Server) handleOptimize(w http.ResponseWriter, r *http.Request) {
	if err := s.wr.Optimize(r.Context()); err != nil {
		s.handleErr(w, r, err)
		return
	}
	logAt(w, levelInfo)
	w.WriteHeader(http.StatusNoContent)
}
