package api

import "net/http"

// handleReset implements DELETE /, wiping every event in the database via
// store.Truncate. Only registered by New when Options.DevMode is true.
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if err := s.st.Truncate(r.Context()); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
