package api

import (
	"encoding/json"
	"net/http"
	"time"
)

type debugResponse struct {
	Time  time.Time  `json:"time"`
	Write debugWrite `json:"write"`
	Read  debugRead  `json:"read"`
}

// debugWrite is the write side's full picture: exclusive-writer admission
// state (active/queued, from internal/queue) plus the underlying SQLite
// write pool, which is always InUse<=1, Max=1 (see store.WritePoolStats).
// HTTPOpen is the number of POST /write and, in dev mode, DELETE /
// requests currently in flight; since every one of them is either the
// active writer or sitting in the queue, it's derived as
// len(Queued)+1(if Active), not tracked separately.
type debugWrite struct {
	Active      *debugActive  `json:"active"` // null when no writer is active
	Queued      []debugQueued `json:"queued"` // never null in the response, even when empty
	HTTPOpen    int           `json:"httpOpen"`
	SQLiteInUse int           `json:"sqliteInUse"`
	SQLiteMax   int           `json:"sqliteMax"`
}

// debugRead is the read side's picture: HTTPOpen (QUERY /events requests
// currently in flight, tracked directly by Server.readHTTPOpen since reads
// have no FIFO queue to derive it from) alongside the underlying SQLite
// read pool's usage. HTTPOpen can exceed SQLiteMax when the pool is
// saturated and extra requests are waiting for a free connection inside
// database/sql itself; a sustained gap between the two is a sign that
// readPoolSize is too small.
type debugRead struct {
	HTTPOpen    int `json:"httpOpen"`
	SQLiteInUse int `json:"sqliteInUse"`
	SQLiteMax   int `json:"sqliteMax"`
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
	writeStats := s.st.WritePoolStats()
	readStats := s.st.ReadPoolStats()

	resp := debugResponse{
		Time: snap.Time,
		Write: debugWrite{
			Queued:      []debugQueued{},
			HTTPOpen:    len(snap.Queued),
			SQLiteInUse: writeStats.InUse,
			SQLiteMax:   writeStats.Max,
		},
		Read: debugRead{
			HTTPOpen:    int(s.readHTTPOpen.Load()),
			SQLiteInUse: readStats.InUse,
			SQLiteMax:   readStats.Max,
		},
	}
	if snap.Active {
		resp.Write.Active = &debugActive{
			Since:      snap.ActiveSince,
			AgeSeconds: snap.Time.Sub(snap.ActiveSince).Seconds(),
		}
		resp.Write.HTTPOpen++
	}
	for _, q := range snap.Queued {
		resp.Write.Queued = append(resp.Write.Queued, debugQueued{
			QueuedAt:    q.QueuedAt,
			WaitSeconds: snap.Time.Sub(q.QueuedAt).Seconds(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
