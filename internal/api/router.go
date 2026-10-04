// Package api is TamarackDB's HTTP layer: routing, authentication, request
// validation, and the error envelope around internal/writer and
// internal/store. It has no knowledge of internal/config; callers pass an
// already-resolved Options.
package api

import (
	"net/http"
	"net/http/pprof"
	"sync/atomic"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/store"
	"github.com/tamarackdb/tamarackdb/internal/tx"
	"github.com/tamarackdb/tamarackdb/internal/writer"
)

// Options configures a Server with values internal/config has already
// resolved (defaults applied); internal/api applies no further defaulting
// of its own.
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

	// DefaultEventsPerPage is the QUERY /events page size applied when a request
	// omits limit. Default: 1000.
	DefaultEventsPerPage int

	// MaxEventsPerPage is the highest limit a QUERY /events request may ask for;
	// above it, 400. Default: 10000.
	MaxEventsPerPage int

	// MaxEventSize is the maximum combined UTF-8 byte size
	// (dcb.EventData.Size()) of one appended event; over it, 413.
	// Default: 65536 (64 KiB).
	MaxEventSize int

	// MaxProjectionSize is the maximum combined UTF-8 byte size of one
	// projection's type, id, and payload (a deletion has no payload) in a
	// POST /write request; over it, 413. Default: 65536 (64 KiB).
	MaxProjectionSize int

	// MaxEventsPerWrite caps how many events, and how many Append
	// Conditions, a single POST /write may carry; over it, 400.
	MaxEventsPerWrite int

	// MaxProjectionsPerWrite caps how many projections a single
	// POST /write may carry, across create, replace, and delete; over it,
	// 400.
	MaxProjectionsPerWrite int

	// MaxRequestBodySize caps every request body, in bytes; over it, 413.
	// It isn't checked against the other limits: it's the real bound on a
	// write, the others are per-item rules.
	MaxRequestBodySize int

	// DevMode, when true, registers POST /reset, which deletes every event
	// and projection, and the /debug/pprof/ profiling endpoints. Never
	// enable this in production.
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
	// crashes the process, only reports; main.go supplies a callback
	// that triggers its own ordered shutdown.
	OnFatalStorageError func(error)
}

// Server is TamarackDB's HTTP API. It implements http.Handler directly, so
// main.go can pass the result of New straight to http.Server.
type Server struct {
	wr   *writer.Writer
	txs  *tx.Registry
	st   *store.Store
	opts Options

	// startedAt and internalErrors are for GET /stats.
	startedAt      time.Time
	internalErrors atomic.Uint64

	// logThreshold is Options.LogLevel parsed once at construction; see
	// withLogging.
	logThreshold level

	handler http.Handler
}

// New builds a Server ready to serve traffic. wr, txs, and st must already be
// constructed and are not owned by the returned Server; the caller
// remains responsible for closing both.
//
// New panics on invalid static configuration (non-positive limits,
// DefaultEventsPerPage > MaxEventsPerPage, an unknown LogLevel, nil
// wr/st): these are startup wiring bugs, not request-time conditions, the
// same "fail loud and immediately" treatment store.Open gives a bad
// database file.
func New(wr *writer.Writer, txs *tx.Registry, st *store.Store, opts Options) *Server {
	logThreshold, validLogLevel := parseLevel(opts.LogLevel)
	switch {
	case wr == nil:
		panic("api: New: wr must not be nil")
	case txs == nil:
		panic("api: New: txs must not be nil")
	case st == nil:
		panic("api: New: st must not be nil")
	case opts.DefaultEventsPerPage <= 0:
		panic("api: New: Options.DefaultEventsPerPage must be positive")
	case opts.MaxEventsPerPage <= 0:
		panic("api: New: Options.MaxEventsPerPage must be positive")
	case opts.DefaultEventsPerPage > opts.MaxEventsPerPage:
		panic("api: New: Options.DefaultEventsPerPage must not exceed Options.MaxEventsPerPage")
	case opts.MaxEventSize <= 0:
		panic("api: New: Options.MaxEventSize must be positive")
	case opts.MaxProjectionSize <= 0:
		panic("api: New: Options.MaxProjectionSize must be positive")
	case opts.MaxEventsPerWrite <= 0:
		panic("api: New: Options.MaxEventsPerWrite must be positive")
	case opts.MaxProjectionsPerWrite <= 0:
		panic("api: New: Options.MaxProjectionsPerWrite must be positive")
	case opts.MaxRequestBodySize <= 0:
		panic("api: New: Options.MaxRequestBodySize must be positive")
	case !validLogLevel:
		panic(`api: New: Options.LogLevel must be one of "debug", "info", "warning", "error"`)
	}

	s := &Server{wr: wr, txs: txs, st: st, opts: opts, logThreshold: logThreshold, startedAt: time.Now()}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /write", s.handleWrite)
	mux.HandleFunc("QUERY /events", s.handleReadEvents)
	mux.HandleFunc("GET /projections/{type}/{id}", s.handleGetProjection)
	mux.HandleFunc("DELETE /projections/{type}", s.handleDeleteProjectionsByType)
	mux.HandleFunc("DELETE /projections", s.handleDeleteAllProjections)
	mux.HandleFunc("POST /tx", s.handleTxBegin)
	mux.HandleFunc("QUERY /tx/{txId}/events", s.handleTxReadEvents)
	mux.HandleFunc("POST /tx/{txId}/events", s.handleTxWriteEvents)
	mux.HandleFunc("GET /tx/{txId}/projections/{type}/{id}", s.handleTxGetProjection)
	mux.HandleFunc("POST /tx/{txId}/projections", s.handleTxWriteProjections)
	mux.HandleFunc("POST /tx/{txId}/commit", s.handleTxCommit)
	mux.HandleFunc("DELETE /tx/{txId}", s.handleTxAbandon)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /stats", s.handleStats)
	// Deliberately no catch-all "/" route: registering one would live in
	// ServeMux's method-agnostic subtree and match any method on any
	// path, silently swallowing the mux's built-in 405 detection (which
	// only fires when truly nothing, including method-agnostic patterns,
	// matches). An unknown path gets the stdlib's plain-text 404; a known
	// path with the wrong method correctly gets 405 + Allow.
	if opts.DevMode {
		// Deletes every event and projection, in its turn in the FIFO,
		// see writer.Writer.Reset.
		mux.HandleFunc("POST /reset", s.handleReset)

		// Standard net/http/pprof registration, mounted on our own mux
		// instead of relying on the package's http.DefaultServeMux side
		// effect. go tool pprof's client also POSTs to
		// /debug/pprof/symbol for large symbol lookups, hence the
		// second registration for that one path.
		//
		// DevMode-gated like POST /reset above: CPU/heap profiles and
		// goroutine dumps can leak information about running queries
		// and are never meant for a production deployment.
		mux.HandleFunc("GET /debug/pprof/", pprof.Index)
		mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("POST /debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	}

	var h http.Handler = s.withBodyLimit(mux)
	if opts.EnableAuth {
		h = s.withAuth(h)
	}
	s.handler = s.withLogging(h)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}
