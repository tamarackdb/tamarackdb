package api

import (
	"net/http"

	"github.com/tamarackdb/tamarackdb/internal/queue"
)

// joinWriteQueue joins the shared FIFO admission queue for exclusive
// SQLite write access (internal/queue), used identically by POST /append
// and DELETE /. On success the caller owns the returned *queue.Ticket and
// must call Done exactly once (typically via defer). On failure,
// joinWriteQueue has already written the appropriate error response
// (503 AppendQueueFull, or whatever handleErr maps ctx-cancellation/
// queue.ErrClosed to) and returns ok=false; the caller must return
// immediately.
func (s *Server) joinWriteQueue(w http.ResponseWriter, r *http.Request) (*queue.Ticket, bool) {
	ticket, err := s.qm.Join(r.Context())
	if err != nil {
		s.handleErr(w, r, err)
		return nil, false
	}
	return ticket, true
}
