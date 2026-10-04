package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/ndjson"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/store"
	"github.com/tamarackdb/tamarackdb/internal/tx"
)

// The endpoints under /tx/{txId} work on one transaction (see package tx).
// None of their responses carries the store ID: the transaction keeps it.
// Any error ends the transaction, including one this layer finds before
// the transaction is reached, such as a malformed body: such an error
// goes through txFail, which ends it.

type txBeginResponse struct {
	TxID string `json:"txId"`
}

// txReadRequest is QUERY /tx/{txId}/events's body. A decision read has no
// afterSequence and no limit: it sees every event that matches.
type txReadRequest struct {
	Query dcb.Query `json:"query"`
}

// txEventsRequest is POST /tx/{txId}/events's body. events is a pointer so
// that a missing key gets 400, instead of closing the open condition with
// no events.
type txEventsRequest struct {
	Events *[]dcb.EventInput `json:"events"`
}

// txProjectionsRequest is POST /tx/{txId}/projections's body. The server
// knows the version of each projection the transaction read, so the
// client only says what the projection becomes.
type txProjectionsRequest struct {
	Upsert []projection.Create `json:"upsert"`
	Delete []projection.Key    `json:"delete"`
}

// txWriteResponse is the response to both writes of a transaction: the
// time the server gave the write, which every one of its events carries.
type txWriteResponse struct {
	Time string `json:"time"`
}

// pendingEventWire is one pending event in a QUERY /tx/{txId}/events
// response: an event of the transaction, with its time and no sequence.
type pendingEventWire struct {
	Time        string            `json:"time"`
	Type        string            `json:"type"`
	Identifiers dcb.IdentifierSet `json:"identifiers"`
	Metadata    dcb.MetadataSet   `json:"metadata"`
	Payload     string            `json:"payload"`
}

// txReadTrailer is the last line of a QUERY /tx/{txId}/events response. A
// response without it was cut short.
type txReadTrailer struct {
	End bool `json:"end"`
}

func (s *Server) handleTxBegin(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, txBeginResponse{TxID: s.txs.Begin()})
}

// txFail writes err for a request on transaction id that this layer
// refused before reaching the transaction, and ends the transaction. An
// item over a size limit is the application's data, not a bug of its
// client library, so it doesn't count as a design error. A transaction
// that doesn't exist gets 404 instead, as any call on it does.
func (s *Server) txFail(w http.ResponseWriter, r *http.Request, id string, err error) {
	var oe *oversizeError
	var be *bodyTooLargeError
	design := !errors.As(err, &oe) && !errors.As(err, &be)
	if !s.txs.Reject(id, design) {
		err = tx.ErrNotFound
	}
	s.handleErr(w, r, err)
}

// handleTxReadEvents implements QUERY /tx/{txId}/events: the committed
// events that match, then the transaction's pending events that match,
// then the trailer. It opens a condition (see tx.Registry.ReadEvents).
func (s *Server) handleTxReadEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("txId")
	var req txReadRequest
	err := decodeJSONStrict(r, &req)
	if err == nil {
		err = req.Query.Validate()
	}
	if err != nil {
		s.txFail(w, r, id, err)
		return
	}
	read, err := s.txs.ReadEvents(r.Context(), id, req.Query)
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	renew, done := s.stallDeadline(w)
	defer done()
	if err := streamTxEvents(w, read, renew); err != nil {
		s.handleErr(w, r, err)
	}
}

// streamTxEvents writes read as NDJSON, closing its iterator. As with
// streamEvents, a failure once the first line is out can only end the
// response early, without the trailer.
func streamTxEvents(w http.ResponseWriter, read tx.Read, beforeWrite func()) error {
	if read.Committed != nil {
		defer read.Committed.Close()
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	beforeWrite()
	w.WriteHeader(http.StatusOK)

	nw := ndjson.NewWriter(w)
	if it := read.Committed; it != nil {
		for it.Next() {
			beforeWrite()
			if err := nw.WriteValue(toReadEventWire(it.Event())); err != nil {
				return err
			}
		}
		if err := it.Err(); err != nil {
			return err
		}
	}
	for _, e := range read.Pending {
		beforeWrite()
		if err := nw.WriteValue(pendingEventWire{
			Time:        e.Time.Format(dcb.TimeLayout),
			Type:        e.Type,
			Identifiers: e.Identifiers,
			Metadata:    e.Metadata,
			Payload:     e.Payload,
		}); err != nil {
			return err
		}
	}
	beforeWrite()
	return nw.WriteValue(txReadTrailer{End: true})
}

// handleTxWriteEvents implements POST /tx/{txId}/events: the events of the
// open condition's decision, or none. It closes the condition.
func (s *Server) handleTxWriteEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("txId")
	var req txEventsRequest
	err := decodeJSONStrict(r, &req)
	if err == nil && req.Events == nil {
		err = &dcb.ValidationError{Err: errMissingEvents, Message: "events is required: send an empty list to write no event"}
	}
	if err == nil {
		err = validateEvents(*req.Events, s.opts.MaxEventSize)
	}
	if err != nil {
		s.txFail(w, r, id, err)
		return
	}
	t, err := s.txs.WriteEvents(id, eventData(*req.Events))
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, txWriteResponse{Time: t.Format(dcb.TimeLayout)})
}

// handleTxGetProjection implements GET /tx/{txId}/projections/{type}/{id}:
// the projection as the transaction sees it, with no version header.
func (s *Server) handleTxGetProjection(w http.ResponseWriter, r *http.Request) {
	key := tx.Key{Type: r.PathValue("type"), ID: r.PathValue("id")}
	payload, found, err := s.txs.GetProjection(r.Context(), r.PathValue("txId"), key)
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "ProjectionNotFound", "")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(payload))
}

// handleTxWriteProjections implements POST /tx/{txId}/projections.
func (s *Server) handleTxWriteProjections(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("txId")
	var req txProjectionsRequest
	err := decodeJSONStrict(r, &req)
	if err == nil {
		err = s.validateTxProjections(req)
	}
	if err != nil {
		s.txFail(w, r, id, err)
		return
	}
	upsert := make([]tx.Projection, len(req.Upsert))
	for i, p := range req.Upsert {
		upsert[i] = tx.Projection{Key: tx.Key{Type: p.Type, ID: p.ID}, Payload: *p.Payload}
	}
	del := make([]tx.Key, len(req.Delete))
	for i, k := range req.Delete {
		del[i] = tx.Key{Type: k.Type, ID: k.ID}
	}
	t, err := s.txs.WriteProjections(id, upsert, del)
	if err != nil {
		s.handleErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, txWriteResponse{Time: t.Format(dcb.TimeLayout)})
}

// validateTxProjections checks each projection's own rules, then
// maxProjectionSize. A message names it as upsert[i] or delete[i].
func (s *Server) validateTxProjections(req txProjectionsRequest) error {
	check := func(at string, validate func() error, size int) error {
		if err := validate(); err != nil {
			return prefixed(at, err)
		}
		if size > s.opts.MaxProjectionSize {
			return &oversizeError{at: at, setting: "maxProjectionSize", size: size, max: s.opts.MaxProjectionSize}
		}
		return nil
	}
	for i, p := range req.Upsert {
		size := len(p.Type) + len(p.ID)
		if p.Payload != nil {
			size += len(*p.Payload)
		}
		if err := check(fmt.Sprintf("upsert[%d]", i), p.Validate, size); err != nil {
			return err
		}
	}
	for i, k := range req.Delete {
		if err := check(fmt.Sprintf("delete[%d]", i), k.Validate, len(k.Type)+len(k.ID)); err != nil {
			return err
		}
	}
	return nil
}

// handleTxCommit implements POST /tx/{txId}/commit: 204 once everything
// the transaction holds is written. The transaction is over either way.
func (s *Server) handleTxCommit(w http.ResponseWriter, r *http.Request) {
	if err := s.txs.Commit(r.Context(), r.PathValue("txId")); err != nil {
		s.handleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTxAbandon implements DELETE /tx/{txId}: always 204, even for a
// transaction that is already over.
func (s *Server) handleTxAbandon(w http.ResponseWriter, r *http.Request) {
	s.txs.Abandon(r.PathValue("txId"))
	w.WriteHeader(http.StatusNoContent)
}

// toReadEventWire is one committed event as a read returns it.
func toReadEventWire(ev store.ReadEvent) readEventWire {
	return readEventWire{
		Sequence:    ev.Sequence,
		Time:        ev.Time,
		Type:        ev.Type,
		Identifiers: ev.Identifiers,
		Metadata:    ev.Metadata,
		Payload:     ev.Payload,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var errMissingEvents = errors.New("api: events is required")
