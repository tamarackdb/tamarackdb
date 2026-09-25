package api

import "net/http"

// handleGetDocument implements GET /documents/{type}/{id}: 404 when no
// document exists, or 200 with the payload as the response body. The
// payload is returned as-is: its own format (JSON, XML, plain text) is up
// to the writing application, the store never parses it.
func (s *Server) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	payload, found, err := s.st.GetDocument(r.Context(), r.PathValue("type"), r.PathValue("id"))
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "DocumentNotFound", "")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(payload))
}

// handleDeleteDocumentsByType implements DELETE /documents/{type}: a bulk
// clear of every document of that type, meant for a rebuild, not for
// ordinary traffic. It joins the
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

// handleDeleteAllDocuments implements DELETE /documents: the same bulk
// clear as handleDeleteDocumentsByType, widened to every type at once, a
// shortcut for a total rebuild.
func (s *Server) handleDeleteAllDocuments(w http.ResponseWriter, r *http.Request) {
	ticket, ok := s.joinWriteQueue(w, r)
	if !ok {
		return
	}
	defer ticket.Done()

	if err := s.st.DeleteAllDocuments(r.Context()); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
