package api

import (
	"net/http"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// statsResponse is GET /stats's body: counters since startup, and when
// that was. Its shape isn't part of the API: it may change between
// versions.
type statsResponse struct {
	StartedAt    string            `json:"startedAt"`
	Writes       writesStats       `json:"writes"`
	Transactions transactionsStats `json:"transactions"`
	Errors       errorsStats       `json:"errors"`
}

type writesStats struct {
	Committed      uint64         `json:"committed"`
	Conflicts      conflictsStats `json:"conflicts"`
	WriteQueueFull uint64         `json:"writeQueueFull"`
}

type conflictsStats struct {
	Condition  uint64 `json:"condition"`
	Projection uint64 `json:"projection"`
}

type transactionsStats struct {
	Begun        uint64 `json:"begun"`
	Committed    uint64 `json:"committed"`
	Abandoned    uint64 `json:"abandoned"`
	Expired      uint64 `json:"expired"`
	DesignErrors uint64 `json:"designErrors"`
}

type errorsStats struct {
	Internal uint64 `json:"internal"`
}

// handleStats implements GET /stats. It never waits for a write.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ws, ts := s.wr.Stats(), s.txs.Stats()
	writeJSON(w, http.StatusOK, statsResponse{
		StartedAt: s.startedAt.UTC().Format(dcb.TimeLayout),
		Writes: writesStats{
			Committed:      ws.Committed,
			Conflicts:      conflictsStats{Condition: ws.ConditionConflicts, Projection: ws.ProjectionConflicts},
			WriteQueueFull: ws.WriteQueueFull,
		},
		Transactions: transactionsStats{
			Begun:        ts.Begun,
			Committed:    ts.Committed,
			Abandoned:    ts.Abandoned,
			Expired:      ts.Expired,
			DesignErrors: ts.DesignErrors,
		},
		Errors: errorsStats{Internal: s.internalErrors.Load()},
	})
}
