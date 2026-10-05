package api

import (
	"encoding/json"
	"net/http"

	"github.com/tamarackdb/tamarackdb/internal/tx"
)

type healthResponse struct {
	Status  string `json:"status"`
	Paused  bool   `json:"paused"`
	Version string `json:"version"`
}

// handleHealth confirms the process is responsive and SQLite is reachable
// (store.Store.Ping). 200 with a trivial body on success, which says
// whether the pause is in place: a paused server still serves reads and
// writes of projections, so it stays 200. On failure, 503
// Service Unavailable rather than 500: this endpoint's job is reporting
// readiness to a supervisor/load balancer, and 503 is the conventional
// signal such tooling already expects for "not ready right now", distinct
// from the 500 an ordinary in-request handler failure returns elsewhere.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.st.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Unavailable", "storage is not reachable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	paused := s.txs.PauseInfo().State == tx.Paused
	_ = json.NewEncoder(w).Encode(healthResponse{Status: "ok", Paused: paused, Version: s.opts.Version})
}
