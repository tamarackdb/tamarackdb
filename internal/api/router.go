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
// already resolved (defaults applied) by the time they reach here —
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

	// DevMode, when true, registers DELETE /, which wipes the entire
	// database. Never enable this in production.
	DevMode bool

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

	// failedTotal counts appends that failed with
	// store.ErrConcurrencyConflict, exposed by GET /metrics. It lives
	// here, not in internal/queue, because that failure is only known
	// once store.Append runs, after the queue manager already admitted
	// the writer.
	failedTotal atomic.Uint64

	// readHTTPOpen counts QUERY /read requests currently in flight,
	// exposed by GET /debug. Unlike writes, reads have no FIFO queue to
	// derive this from (internal/queue only tracks write admission), so
	// handleRead increments/decrements it directly around its whole
	// lifetime, including NDJSON streaming.
	readHTTPOpen atomic.Int64

	handler http.Handler
}

// New builds a Server ready to serve traffic. qm and st must already be
// constructed and are not owned by the returned Server — the caller
// remains responsible for closing both.
//
// New panics on invalid static configuration (empty token, non-positive
// limits, DefaultLimit > MaxLimit, nil qm/st): these are startup wiring
// bugs, not request-time conditions, the same "fail loud and immediately"
// treatment store.Open gives a bad database file.
func New(qm *queue.Manager, st *store.Store, opts Options) *Server {
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
	}

	s := &Server{qm: qm, st: st, opts: opts}

	mux := http.NewServeMux()
	mux.HandleFunc("QUERY /read", s.handleRead)
	mux.HandleFunc("POST /append", s.handleAppend)
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
		// "DELETE /" is method-scoped, unlike a bare "/": it only ever
		// matches DELETE requests, so it doesn't reintroduce the
		// 405-swallowing problem described above for the other methods.
		mux.HandleFunc("DELETE /", s.handleReset)

		// Standard net/http/pprof registration, mounted on our own mux
		// instead of relying on the package's http.DefaultServeMux
		// side effect. Method-scoped, unlike upstream net/http/pprof's
		// own method-agnostic registration: a method-agnostic pattern
		// here would conflict with "DELETE /" above (neither pattern
		// is strictly more specific than the other, since one wins on
		// method and the other on path), which ServeMux rejects at
		// registration time. go tool pprof's client also POSTs to
		// /debug/pprof/symbol for large symbol lookups, hence the
		// second registration for that one path.
		//
		// DevMode-gated like DELETE / above: CPU/heap profiles and
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
