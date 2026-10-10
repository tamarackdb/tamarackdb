package api

import (
	"context"
	"net/http"
)

// handleDeleteEvents implements DELETE /events: it deletes every event, in
// its turn in the FIFO (see store.Store.DeleteAllEvents). It waits for no
// transaction, and checks no pause: an open transaction goes on, and its
// commit writes into the emptied log. It's for a developer alone on the
// instance, so only registered by New when Options.DevMode is true.
func (s *Server) handleDeleteEvents(w http.ResponseWriter, r *http.Request) {
	if err := s.wr.RunInTurn(r.Context(), func(ctx context.Context) error {
		return s.st.DeleteAllEvents(ctx)
	}); err != nil {
		s.handleErr(w, r, err)
		return
	}
	logAt(w, levelInfo)
	w.WriteHeader(http.StatusNoContent)
}
