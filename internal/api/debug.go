package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

type debugResponse struct {
	Time   time.Time            `json:"time"`
	Held   []debugReservation   `json:"held"`
	Queued []debugQueuedRequest `json:"queued"`
}

type debugReservation struct {
	Condition  dcb.AppendCondition `json:"condition"`
	Events     []dcb.EventData     `json:"events"`
	GrantedAt  time.Time           `json:"grantedAt"`
	AgeSeconds float64             `json:"ageSeconds"`
}

type debugQueuedRequest struct {
	Condition   dcb.AppendCondition `json:"condition"`
	Events      []dcb.EventData     `json:"events"`
	QueuedAt    time.Time           `json:"queuedAt"`
	WaitSeconds float64             `json:"waitSeconds"`
}

func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	snap, err := s.gk.Snapshot(r.Context())
	if err != nil {
		s.handleErr(w, r, err)
		return
	}

	resp := debugResponse{
		Time:   snap.Time,
		Held:   []debugReservation{}, // never null in the response, even when empty
		Queued: []debugQueuedRequest{},
	}
	for _, h := range snap.Held {
		resp.Held = append(resp.Held, debugReservation{
			Condition: h.Condition, Events: h.Events, GrantedAt: h.GrantedAt,
			AgeSeconds: snap.Time.Sub(h.GrantedAt).Seconds(),
		})
	}
	for _, q := range snap.Queued {
		resp.Queued = append(resp.Queued, debugQueuedRequest{
			Condition: q.Condition, Events: q.Events, QueuedAt: q.QueuedAt,
			WaitSeconds: snap.Time.Sub(q.QueuedAt).Seconds(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
