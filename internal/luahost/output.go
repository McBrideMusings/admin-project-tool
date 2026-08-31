package luahost

import "fmt"

// ANSI colour codes — mirrors admin_lib/core.py's class C.
const (
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiReset  = "\033[0m"
)

// Info prints msg in cyan to Stdout and appends it (plain) to the active
// log. Host function: info(msg).
func (h *Host) Info(msg string) {
	fmt.Fprintf(h.Stdout, "%s%s%s\n", ansiCyan, msg, ansiReset)
	h.logWrite(msg)
}

// Ok prints msg in green to Stdout and appends it (plain) to the active log.
// Host function: ok(msg).
func (h *Host) Ok(msg string) {
	fmt.Fprintf(h.Stdout, "%s%s%s\n", ansiGreen, msg, ansiReset)
	h.logWrite(msg)
}

// Warn prints msg in yellow to Stderr and appends "WARNING: msg" to the
// active log. Host function: warn(msg).
func (h *Host) Warn(msg string) {
	fmt.Fprintf(h.Stderr, "%s%s%s\n", ansiYellow, msg, ansiReset)
	h.logWrite("WARNING: " + msg)
}

// Err prints msg in red to Stderr and appends "ERROR: msg" to the active
// log. Host function: err(msg).
func (h *Host) Err(msg string) {
	fmt.Fprintf(h.Stderr, "%s%s%s\n", ansiRed, msg, ansiReset)
	h.logWrite("ERROR: " + msg)
}

// SetLogContext names this run's log file by joining non-empty parts with
// "-", deferred until first actual use — see pendingContext's doc comment.
// retain <= 0 means "no pruning" (Python's retain=None). A no-op once a
// context is already active (_log_context_taken in the Python original):
// the innermost caller to arrive here first wins, and a later call from an
// outer step-runner inherits it rather than re-pointing the log.
// Host function: set_log_context(...parts[, {retain=N}]).
func (h *Host) SetLogContext(parts []string, retain int) {
	if !h.logEnabled || h.logContextTaken() {
		return
	}
	name := joinNonEmpty(parts, "-")
	h.pendingContext = &pendingContext{stem: true, value: name, retain: retain}
}

// SetLogContextPath names an explicit log path for this run (a `log_file`
// override), deferred the same way as SetLogContext.
// Host function: set_log_context_path(path[, {retain=N}]).
func (h *Host) SetLogContextPath(path string, retain int) {
	if !h.logEnabled || h.logContextTaken() {
		return
	}
	h.pendingContext = &pendingContext{stem: false, value: path, retain: retain}
}

// logContextTaken mirrors admin_lib/core.py's _log_context_taken: a context
// is already live and owns LogFile.
func (h *Host) logContextTaken() bool {
	return h.logContextActive && h.logFile != ""
}

func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}
