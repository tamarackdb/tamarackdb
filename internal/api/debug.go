package api

import (
	"encoding/json"
	"net/http"
	"time"
)

type debugResponse struct {
	Time   time.Time    `json:"time"`
	Paused *debugPaused `json:"paused"` // null when the server isn't paused
	Write  debugWrite   `json:"write"`
	Read   debugRead    `json:"read"`
}

type debugPaused struct {
	Since time.Time `json:"since"`
}

// debugWrite is the write side's full picture: the active transaction and
// the requests waiting in the FIFO (from internal/txn and internal/queue),
// the write-side requests in flight, and the underlying SQLite write pool,
// which is always InUse<=1, Max=1 (see store.WritePoolStats). It never
// carries the ticket.
type debugWrite struct {
	Active      *debugActive  `json:"active"` // null when no transaction is active
	Queued      []debugQueued `json:"queued"` // never null in the response, even when empty
	HTTPOpen    int           `json:"httpOpen"`
	SQLiteInUse int           `json:"sqliteInUse"`
	SQLiteMax   int           `json:"sqliteMax"`
}

// debugRead is the read side's picture: HTTPOpen (reads without a ticket
// currently in flight) alongside the underlying SQLite read pool's usage.
// HTTPOpen can exceed SQLiteMax when the pool is saturated and extra
// requests are waiting for a free connection inside database/sql itself;
// a sustained gap between the two is a sign that readPoolSize is too
// small.
type debugRead struct {
	HTTPOpen    int `json:"httpOpen"`
	SQLiteInUse int `json:"sqliteInUse"`
	SQLiteMax   int `json:"sqliteMax"`
}

type debugActive struct {
	Since      time.Time `json:"since"`
	AgeSeconds float64   `json:"ageSeconds"`
	Deadline   time.Time `json:"deadline"`
	Ceiling    time.Time `json:"ceiling"`
	Calls      int       `json:"calls"`
}

type debugQueued struct {
	Kind        string    `json:"kind"`
	QueuedAt    time.Time `json:"queuedAt"`
	WaitSeconds float64   `json:"waitSeconds"`
}

func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	snap := s.tm.Snapshot()
	writeStats := s.st.WritePoolStats()
	readStats := s.st.ReadPoolStats()

	resp := debugResponse{
		Time: snap.Time,
		Write: debugWrite{
			Queued:      []debugQueued{},
			HTTPOpen:    int(s.writeHTTPOpen.Load()),
			SQLiteInUse: writeStats.InUse,
			SQLiteMax:   writeStats.Max,
		},
		Read: debugRead{
			HTTPOpen:    int(s.readHTTPOpen.Load()),
			SQLiteInUse: readStats.InUse,
			SQLiteMax:   readStats.Max,
		},
	}
	if snap.Paused {
		resp.Paused = &debugPaused{Since: snap.PausedSince}
	}
	if a := snap.Active; a != nil {
		resp.Write.Active = &debugActive{
			Since:      a.Since,
			AgeSeconds: snap.Time.Sub(a.Since).Seconds(),
			Deadline:   a.Deadline,
			Ceiling:    a.Ceiling,
			Calls:      a.Calls,
		}
	}
	for _, q := range snap.Queue.Queued {
		resp.Write.Queued = append(resp.Write.Queued, debugQueued{
			Kind:        string(q.Kind),
			QueuedAt:    q.QueuedAt,
			WaitSeconds: snap.Time.Sub(q.QueuedAt).Seconds(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
