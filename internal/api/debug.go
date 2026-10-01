package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// debugTime is a time.Time written in the same fixed format as an event's
// time: UTC, with exactly 6 fractional digits.
type debugTime struct{ time.Time }

func (t debugTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UTC().Format(dcb.TimeLayout))
}

type debugResponse struct {
	Time  debugTime  `json:"time"`
	Write debugWrite `json:"write"`
	Read  debugRead  `json:"read"`
}

// debugWrite is the write side's full picture: the request holding the
// turn and the requests waiting in the FIFO (from internal/queue), the
// write-side requests in flight, and the underlying SQLite write pool,
// which is always InUse<=1, Max=1 (see store.WritePoolStats).
type debugWrite struct {
	Active      *debugActive  `json:"active"` // null when no request holds the turn
	Queued      []debugQueued `json:"queued"` // never null in the response, even when empty
	HTTPOpen    int           `json:"httpOpen"`
	SQLiteInUse int           `json:"sqliteInUse"`
	SQLiteMax   int           `json:"sqliteMax"`
}

// debugRead is the read side's picture: HTTPOpen (reads currently in
// flight) alongside the underlying SQLite read pool's usage.
// HTTPOpen can exceed SQLiteMax when the pool is saturated and extra
// requests are waiting for a free connection inside database/sql itself;
// a sustained gap between the two is a sign that readPoolSize is too
// small.
type debugRead struct {
	HTTPOpen    int `json:"httpOpen"`
	SQLiteInUse int `json:"sqliteInUse"`
	SQLiteMax   int `json:"sqliteMax"`
}

// debugActive is the request holding the turn: what it is (a queue.Kind)
// and since when.
type debugActive struct {
	Kind       string    `json:"kind"`
	Since      debugTime `json:"since"`
	AgeSeconds float64   `json:"ageSeconds"`
}

type debugQueued struct {
	Kind        string    `json:"kind"`
	QueuedAt    debugTime `json:"queuedAt"`
	WaitSeconds float64   `json:"waitSeconds"`
}

func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	snap := s.tm.Snapshot()
	writeStats := s.st.WritePoolStats()
	readStats := s.st.ReadPoolStats()

	resp := debugResponse{
		Time: debugTime{snap.Time},
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
	if q := snap.Queue; q.Active {
		resp.Write.Active = &debugActive{
			Kind:       string(q.ActiveKind),
			Since:      debugTime{q.ActiveSince},
			AgeSeconds: snap.Time.Sub(q.ActiveSince).Seconds(),
		}
	}
	for _, q := range snap.Queue.Queued {
		resp.Write.Queued = append(resp.Write.Queued, debugQueued{
			Kind:        string(q.Kind),
			QueuedAt:    debugTime{q.QueuedAt},
			WaitSeconds: snap.Time.Sub(q.QueuedAt).Seconds(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
