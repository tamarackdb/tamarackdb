package api

import (
	"log"
	"net/http"

	"github.com/tamarackdb/tamarackdb/internal/tx"
)

// pauseResponse is POST /pause's body. The store ID goes in the
// X-Tamarackdb-Store header, as on every response that depends on the
// store.
type pauseResponse struct {
	LastSequence int64 `json:"lastSequence"`
}

// handlePause implements POST /pause (see tx.Registry.Pause): it returns
// once the pause is in place, with the last Sequence Position.
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	result, err := s.txs.Pause(r.Context())
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	logAt(w, levelInfo)
	w.Header().Set(StoreHeader, result.StoreID)
	writeJSON(w, http.StatusOK, pauseResponse{LastSequence: result.LastSequence})
}

// handleResume implements POST /resume (see tx.Registry.Resume).
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if err := s.txs.Resume(r.Context()); err != nil {
		s.handleErr(w, r, err)
		return
	}
	logAt(w, levelInfo)
	w.WriteHeader(http.StatusNoContent)
}

// logPaused logs a pause taking hold, at INFO, even if no caller of
// POST /pause is still there to see it.
func (s *Server) logPaused(result tx.PauseResult) {
	if levelInfo < s.logThreshold {
		return
	}
	log.Printf("tamarackdb-server: [%s] pause in place at sequence %d", levelInfo, result.LastSequence)
}

// logAt sets the access-log level of a successful response, which is
// DEBUG otherwise.
func logAt(w http.ResponseWriter, l level) {
	if sw, ok := w.(*statusWriter); ok {
		sw.level = l
	}
}
