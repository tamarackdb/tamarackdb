package api

import "net/http"

// pauseResponse is POST /pause's body once the pause is in place. The
// store ID goes in the X-Tamarackdb-Store header, as on every response
// that depends on the store.
type pauseResponse struct {
	LastSequence int64 `json:"lastSequence"`
}

// pauseRequestedResponse is POST /pause's body while transactions are
// still open: the caller calls again later.
type pauseRequestedResponse struct {
	OpenTransactions int `json:"openTransactions"`
}

// handlePause implements POST /pause (see tx.Registry.Pause): 200 with the
// last Sequence Position once the pause is in place, or 202 with the
// transactions still open.
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	result, err := s.txs.Pause(r.Context())
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	logAt(w, levelInfo)
	if !result.Paused {
		writeJSON(w, http.StatusAccepted, pauseRequestedResponse{OpenTransactions: result.Open})
		return
	}
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

// logAt sets the access-log level of a successful response, which is
// DEBUG otherwise.
func logAt(w http.ResponseWriter, l level) {
	if sw, ok := w.(*statusWriter); ok {
		sw.level = l
	}
}
