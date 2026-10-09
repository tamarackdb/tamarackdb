package api

// level is the severity of one access-log line. TamarackDB uses four
// levels only, not the full syslog/PSR-3 hierarchy: debug < info <
// warning < error is enough to separate "nothing to see," "worth
// knowing," "capacity signal," and "real failure" for a single HTTP API
// server.
type level int

const (
	levelDebug level = iota
	levelInfo
	levelWarning
	levelError
)

// String is the uppercase tag written into the access log line, e.g.
// "ERROR" in "tamarackdb-server: [ERROR] ...".
func (l level) String() string {
	switch l {
	case levelDebug:
		return "DEBUG"
	case levelInfo:
		return "INFO"
	case levelWarning:
		return "WARNING"
	default:
		return "ERROR"
	}
}

// parseLevel maps a config-supplied level name (already lowercased by
// internal/config) to a level. ok is false for anything not one of the
// four recognized names.
func parseLevel(s string) (level, bool) {
	switch s {
	case "debug":
		return levelDebug, true
	case "info":
		return levelInfo, true
	case "warning":
		return levelWarning, true
	case "error":
		return levelError, true
	default:
		return 0, false
	}
}

// codeLevel maps every error envelope's "code" string (writeError's
// second argument, called directly or via handleErr) to the access-log
// level a response carrying it should be logged at. A plain successful
// response never calls writeError at all, so it keeps statusWriter's
// default, levelDebug: a success is exactly as unremarkable as
// ConcurrencyException or ProjectionNotFound below, the server did what it
// was supposed to do.
var codeLevel = map[string]level{
	"ProjectionNotFound":   levelDebug,   // 404, exactly as designed
	"TransactionNotFound":  levelInfo,    // 404, client-side noise, often an expired transaction
	"ConcurrencyException": levelDebug,   // 409, exactly as designed
	"InvalidRequest":       levelInfo,    // 400, client-side noise
	"PayloadTooLarge":      levelInfo,    // 413, client-side noise
	"Unauthorized":         levelInfo,    // 401, client-side noise
	"WriteQueueFull":       levelWarning, // 503, real capacity signal
	"TooManyTransactions":  levelWarning, // 503, real capacity signal, or a client that never ends its transactions
	"ShuttingDown":         levelInfo,    // 503, the operator stopped the server
	"Paused":               levelInfo,    // 503, the operator paused transactions
	"NotPaused":            levelInfo,    // 409, a reset outside a pause
	"TransactionBusy":      levelInfo,    // 409, a call while another one uses the transaction
	"InternalError":        levelError,   // 500, real failure
	"Unavailable":          levelError,   // 503, storage unreachable
}
