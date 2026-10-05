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
	Pause        pauseStats        `json:"pause"`
	Errors       errorsStats       `json:"errors"`
}

// pauseStats is the state of the pause, since when, and how many
// transactions a requested pause still waits for.
type pauseStats struct {
	State            string `json:"state"`
	Since            string `json:"since"`
	OpenTransactions int    `json:"openTransactions"`
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
	Paused       uint64 `json:"paused"`
}

type errorsStats struct {
	Internal uint64 `json:"internal"`
}

// handleStats implements GET /stats. It never waits for a write.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ws, ts, ps := s.wr.Stats(), s.txs.Stats(), s.txs.PauseInfo()
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
			Paused:       ts.Paused,
		},
		Pause: pauseStats{
			State:            ps.State.String(),
			Since:            ps.Since.UTC().Format(dcb.TimeLayout),
			OpenTransactions: ps.Open,
		},
		Errors: errorsStats{Internal: s.internalErrors.Load()},
	})
}
