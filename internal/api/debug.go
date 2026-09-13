package api

import (
	"encoding/json"
	"net/http"
	"time"
)

type debugResponse struct {
	Time   time.Time     `json:"time"`
	Active *debugActive  `json:"active"` // null when no writer is active
	Queued []debugQueued `json:"queued"` // never null in the response, even when empty
}

type debugActive struct {
	Since      time.Time `json:"since"`
	AgeSeconds float64   `json:"ageSeconds"`
}

type debugQueued struct {
	QueuedAt    time.Time `json:"queuedAt"`
	WaitSeconds float64   `json:"waitSeconds"`
}

func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	snap := s.qm.Snapshot()

	resp := debugResponse{
		Time:   snap.Time,
		Queued: []debugQueued{},
	}
	if snap.Active {
		resp.Active = &debugActive{
			Since:      snap.ActiveSince,
			AgeSeconds: snap.Time.Sub(snap.ActiveSince).Seconds(),
		}
	}
	for _, q := range snap.Queued {
		resp.Queued = append(resp.Queued, debugQueued{
			QueuedAt:    q.QueuedAt,
			WaitSeconds: snap.Time.Sub(q.QueuedAt).Seconds(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
