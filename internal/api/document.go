package api

import (
	"net/http"
	"strconv"

	"github.com/tamarackdb/tamarackdb/internal/store"
)

// documentNotReadyRetryAfterSeconds is the fixed Retry-After hint sent
// with 503 DocumentNotReady. Not configurable in this iteration.
const documentNotReadyRetryAfterSeconds = 1

// DocumentVersionHeader carries a document's version on a successful
// GET /documents/{type}/{id}, so the response body can be the payload
// itself instead of a JSON envelope around it.
const DocumentVersionHeader = "X-Tamarackdb-Document-Version"

// handleGetDocument implements GET /documents/{type}/{id}. It maps
// store.DocumentStatus to one of three outcomes: 404 when no metadata
// exists at all, 503 DocumentNotReady (with Retry-After) when the
// metadata exists but its payload hasn't caught up yet, or 200 with the
// current payload as the response body and its version in the
// X-Tamarackdb-Document-Version header. The payload is returned as-is: its own
// format (JSON, XML, plain text) is up to the writing application, the
// store never parses it.
func (s *Server) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	typ, id := r.PathValue("type"), r.PathValue("id")

	doc, status, err := s.st.GetDocument(r.Context(), typ, id)
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	switch status {
	case store.DocumentNotFound:
		writeError(w, http.StatusNotFound, "DocumentNotFound", "")
	case store.DocumentNotReady:
		w.Header().Set("Retry-After", strconv.Itoa(documentNotReadyRetryAfterSeconds))
		writeError(w, http.StatusServiceUnavailable, "DocumentNotReady", "")
	default: // store.DocumentFound
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set(DocumentVersionHeader, strconv.FormatInt(*doc.Version, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(*doc.Payload))
	}
}

// handleDeleteDocumentsByType implements DELETE /documents/{type}: an
// unversioned bulk clear of every document of that type, meant for a
// rebuild, not for ordinary traffic (see the design doc). It joins the
// same FIFO write-admission queue as POST /write, so it can never land
// mid-write or a write mid-clear.
func (s *Server) handleDeleteDocumentsByType(w http.ResponseWriter, r *http.Request) {
	ticket, ok := s.joinWriteQueue(w, r)
	if !ok {
		return
	}
	defer ticket.Done()

	if err := s.st.DeleteDocumentsByType(r.Context(), r.PathValue("type")); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
