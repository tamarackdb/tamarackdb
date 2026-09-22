// Package api is TamarackDB's HTTP layer: routing, authentication, request
// validation, and the error envelope around internal/queue and
// internal/store. It has no knowledge of internal/config; callers pass an
// already-resolved Options.
package api

import (
	"net/http"
	"net/http/pprof"
	"sync/atomic"

	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// Options configures a Server with values internal/config will have
// already resolved (defaults applied) by the time they reach here;
// internal/api applies no further defaulting of its own.
type Options struct {
	// Version is the running build's version string, reported as-is by
	// GET /health. Empty is valid: it just reports as "".
	Version string

	// EnableAuth turns on the Bearer-token check on every request. When
	// false, the API is served with no authentication at all.
	EnableAuth bool

	// AuthToken is the single static Bearer token every request must
	// present when EnableAuth is true. Unused otherwise.
	AuthToken string

	// DefaultLimit is the /read page size applied when a request omits
	// limit. Default: 1000.
	DefaultLimit int

	// MaxLimit is the highest limit a /read request may ask for; above
	// it, 400. Default: 10000.
	MaxLimit int

	// MaxEventSize is the maximum combined UTF-8 byte size
	// (dcb.EventData.Size()) of one appended event; over it, 413.
	// Default: 65536 (64 KiB).
	MaxEventSize int

	// MaxDocumentSize is the maximum UTF-8 byte size of one document's
	// payload in a /write request; over it, 413. Only checked when the
	// payload is present (a deletion has none to bound). Default: 65536
	// (64 KiB).
	MaxDocumentSize int

	// MaxDocumentsPerWrite caps how many documents a single /write
	// request may carry; over it, 400. Independent of
	// dcb.MaxEventsPerWrite: the two are unrelated limits, not a
	// combined one.
	MaxDocumentsPerWrite int

	// DevMode, when true, registers DELETE /events, which wipes every
	// event (documents are never touched). Never enable this in
	// production.
	DevMode bool

	// LogLevel is the minimum severity the per-request access log line
	// (withLogging) is written at: a request whose computed level is
	// below this is not logged at all. One of "debug", "info",
	// "warning", "error". internal/api applies no defaulting or
	// case-folding of its own: this must already be one of the four
	// exact lowercase names by the time it reaches here, the same
	// "already resolved" contract every other Options field follows.
	LogLevel string

	// OnFatalStorageError, if non-nil, is called whenever a handler
	// observes store.IsFatal(err) == true. The handler itself never
	// crashes the process, only reports; a future main.go supplies a
	// callback that triggers its own log-and-exit shutdown.
	OnFatalStorageError func(error)
}

// Server is TamarackDB's HTTP API. It implements http.Handler directly, so
// a future main.go can pass the result of New straight to
// http.ListenAndServeTLS.
type Server struct {
	qm   *queue.Manager
	st   *store.Store
	opts Options

	// failedTotal counts writes that failed with
	// store.ErrConcurrencyConflict, exposed by GET /metrics. It lives
	// here, not in internal/queue, because that failure is only known
	// once store.Append runs, after the queue manager already admitted
	// the writer.
	failedTotal atomic.Uint64

	// documentPayloadWriteFailedTotal counts documents whose best-effort
	// payload write to tamarackdb-documents.sqlite failed, across every
	// /write call, exposed by GET /metrics. Distinct from failedTotal: a
	// payload write failure never fails the /write call itself (see
	// store.Append's doc comment), so it needs its own counter to stay
	// visible to an operator instead of hiding inside 200 responses.
	documentPayloadWriteFailedTotal atomic.Uint64

	// readHTTPOpen counts QUERY /events requests currently in flight,
	// exposed by GET /debug. Unlike writes, reads have no FIFO queue to
	// derive this from (internal/queue only tracks write admission), so
	// handleEvents increments/decrements it directly around its whole
	// lifetime, including NDJSON streaming.
	readHTTPOpen atomic.Int64

	// logThreshold is Options.LogLevel parsed once at construction; see
	// withLogging.
	logThreshold level

	handler http.Handler
}

// New builds a Server ready to serve traffic. qm and st must already be
// constructed and are not owned by the returned Server; the caller
// remains responsible for closing both.
//
// New panics on invalid static configuration (empty token, non-positive
// limits, DefaultLimit > MaxLimit, nil qm/st): these are startup wiring
// bugs, not request-time conditions, the same "fail loud and immediately"
// treatment store.Open gives a bad database file.
func New(qm *queue.Manager, st *store.Store, opts Options) *Server {
	logThreshold, validLogLevel := parseLevel(opts.LogLevel)
	switch {
	case qm == nil:
		panic("api: New: qm must not be nil")
	case st == nil:
		panic("api: New: st must not be nil")
	case opts.DefaultLimit <= 0:
		panic("api: New: Options.DefaultLimit must be positive")
	case opts.MaxLimit <= 0:
		panic("api: New: Options.MaxLimit must be positive")
	case opts.DefaultLimit > opts.MaxLimit:
		panic("api: New: Options.DefaultLimit must not exceed Options.MaxLimit")
	case opts.MaxEventSize <= 0:
		panic("api: New: Options.MaxEventSize must be positive")
	case opts.MaxDocumentSize <= 0:
		panic("api: New: Options.MaxDocumentSize must be positive")
	case opts.MaxDocumentsPerWrite <= 0:
		panic("api: New: Options.MaxDocumentsPerWrite must be positive")
	case !validLogLevel:
		panic(`api: New: Options.LogLevel must be one of "debug", "info", "warning", "error"`)
	}

	s := &Server{qm: qm, st: st, opts: opts, logThreshold: logThreshold}

	mux := http.NewServeMux()
	mux.HandleFunc("QUERY /events", s.handleEvents)
	mux.HandleFunc("POST /write", s.handleWrite)
	mux.HandleFunc("GET /documents/{type}/{id}", s.handleGetDocument)
	mux.HandleFunc("DELETE /documents/{type}", s.handleDeleteDocumentsByType)
	mux.HandleFunc("DELETE /documents", s.handleDeleteAllDocuments)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /debug", s.handleDebug)
	// Deliberately no catch-all "/" route: registering one would live in
	// ServeMux's method-agnostic subtree and match any method on any
	// path, silently swallowing the mux's built-in 405 detection (which
	// only fires when truly nothing, including method-agnostic patterns,
	// matches). An unknown path gets the stdlib's plain-text 404; a known
	// path with the wrong method correctly gets 405 + Allow.
	if opts.DevMode {
		// Truncates every event (identifiers, metadata, events itself);
		// documents are never touched, see Store.Truncate. A different
		// method on the same path as "QUERY /events" above.
		mux.HandleFunc("DELETE /events", s.handleReset)

		// Standard net/http/pprof registration, mounted on our own mux
		// instead of relying on the package's http.DefaultServeMux side
		// effect. go tool pprof's client also POSTs to
		// /debug/pprof/symbol for large symbol lookups, hence the
		// second registration for that one path.
		//
		// DevMode-gated like DELETE /events above: CPU/heap profiles and
		// goroutine dumps can leak information about running queries
		// and are never meant for a production deployment.
		mux.HandleFunc("GET /debug/pprof/", pprof.Index)
		mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("POST /debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	}

	var h http.Handler = mux
	if opts.EnableAuth {
		h = s.withAuth(h)
	}
	s.handler = s.withLogging(h)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}
