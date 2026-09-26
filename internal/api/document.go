package api

import (
	"fmt"
	"net/http"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/document"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

type documentsRequest struct {
	Documents []document.Data `json:"documents"`
}

// handleGetDocument implements GET /documents/{type}/{id}: 404 when no
// document exists, or 200 with the payload as the response body. With a
// ticket, the read runs inside the transaction and sees documents written
// earlier in it; a 404 is an ordinary answer there, not a failure, and the
// transaction goes on. Without a ticket, it sees committed documents only.
// The payload is returned as-is: its own format (JSON, XML, plain text) is
// up to the writing application, the store never parses it.
func (s *Server) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	typ, id := r.PathValue("type"), r.PathValue("id")

	var payload string
	var found bool
	var err error
	if ticket, ok := ticketFrom(r); ok {
		defer s.trackWrite()()
		err = s.tm.Do(ticket, func(tx *store.Tx) error {
			payload, found, err = tx.GetDocument(r.Context(), typ, id)
			return err
		})
	} else {
		s.readHTTPOpen.Add(1)
		defer s.readHTTPOpen.Add(-1)
		payload, found, err = s.st.GetDocument(r.Context(), typ, id)
	}
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

// handleWriteDocuments implements POST /documents: it creates, replaces,
// or deletes documents. With a ticket, the write runs inside the
// transaction, and any failure rolls it back. Without a ticket, it's
// accepted only while the server is paused, for a projection rebuild, and
// commits on its own; outside a pause it gets 409 NotPaused.
func (s *Server) handleWriteDocuments(w http.ResponseWriter, r *http.Request) {
	defer s.trackWrite()()

	write := func(writeFn func([]document.Data) error) error {
		var req documentsRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if err := validateDocumentsRequest(req, s.opts.MaxDocumentSize, s.opts.MaxDocumentsPerWrite); err != nil {
			return err
		}
		return writeFn(req.Documents)
	}

	var err error
	if ticket, ok := ticketFrom(r); ok {
		err = s.tm.Do(ticket, func(tx *store.Tx) error {
			return write(func(docs []document.Data) error { return tx.WriteDocuments(r.Context(), docs) })
		})
	} else {
		err = s.tm.RunPaused(func() error {
			return write(func(docs []document.Data) error { return s.st.WriteDocuments(r.Context(), docs) })
		})
	}
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateDocumentsRequest checks request-shape rules (between 1 and
// maxDocumentsPerWrite documents), then, per document,
// document.Data.Validate() and its size limit, rejecting a repeated
// type+id pair within the same request rather than leaving its outcome to
// write order.
func validateDocumentsRequest(req documentsRequest, maxDocumentSize, maxDocumentsPerWrite int) error {
	if len(req.Documents) == 0 {
		return &dcb.ValidationError{Err: errNoDocuments, Message: "request must carry at least one document"}
	}
	if len(req.Documents) > maxDocumentsPerWrite {
		return &dcb.ValidationError{Err: errTooManyDocuments, Message: fmt.Sprintf(
			"request carries %d documents, more than the maximum of %d", len(req.Documents), maxDocumentsPerWrite)}
	}
	seen := make(map[[2]string]struct{}, len(req.Documents))
	for i, d := range req.Documents {
		if err := d.Validate(); err != nil {
			return err
		}
		key := [2]string{d.Type, d.ID}
		if _, dup := seen[key]; dup {
			return &dcb.ValidationError{Err: errDuplicateDocumentKey, Message: fmt.Sprintf(
				"document at index %d has the same type and id as an earlier entry in this request", i)}
		}
		seen[key] = struct{}{}
		if d.Payload != nil {
			if size := len(*d.Payload); size > maxDocumentSize {
				return &oversizeError{kind: "document", index: i, size: size, max: maxDocumentSize}
			}
		}
	}
	return nil
}

// handleDeleteDocumentsByType implements DELETE /documents/{type}: a bulk
// delete of every document of that type, for a projection rebuild. It's
// accepted only while the server is paused.
func (s *Server) handleDeleteDocumentsByType(w http.ResponseWriter, r *http.Request) {
	defer s.trackWrite()()
	if err := s.tm.RunPaused(func() error {
		return s.st.DeleteDocumentsByType(r.Context(), r.PathValue("type"))
	}); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteAllDocuments implements DELETE /documents: the same bulk
// delete as handleDeleteDocumentsByType, widened to every type at once.
func (s *Server) handleDeleteAllDocuments(w http.ResponseWriter, r *http.Request) {
	defer s.trackWrite()()
	if err := s.tm.RunPaused(func() error {
		return s.st.DeleteAllDocuments(r.Context())
	}); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
