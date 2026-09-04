// Package api is TamarackDB's HTTP layer: routing, authentication, request
// validation, and the error envelope around internal/gatekeeper and
// internal/store. It has no knowledge of internal/config; callers pass an
// already-resolved Options.
package api

import (
	"net/http"
	"sync/atomic"

	"github.com/tamarackdb/tamarackdb/internal/gatekeeper"
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
	gk   *gatekeeper.Gatekeeper
	st   *store.Store
	opts Options

	// failedTotal counts appends that failed with
	// store.ErrConcurrencyConflict, exposed by GET /metrics. It lives
	// here, not in internal/gatekeeper, because that failure is only
	// known once store.Append runs, after the gatekeeper already granted
	// the reservation.
	failedTotal atomic.Uint64

	handler http.Handler
}

// New builds a Server ready to serve traffic. gk and st must already be
// constructed and are not owned by the returned Server — the caller
// remains responsible for closing both.
//
// New panics on invalid static configuration (empty token, non-positive
// limits, DefaultLimit > MaxLimit, nil gk/st): these are startup wiring
// bugs, not request-time conditions, the same "fail loud and immediately"
// treatment store.Open gives a bad database file.
func New(gk *gatekeeper.Gatekeeper, st *store.Store, opts Options) *Server {
	switch {
	case gk == nil:
		panic("api: New: gk must not be nil")
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

	s := &Server{gk: gk, st: st, opts: opts}

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

	if opts.EnableAuth {
		s.handler = s.withAuth(mux)
	} else {
		s.handler = mux
	}
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}
