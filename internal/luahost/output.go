package luahost

import "fmt"

// ANSI colour codes.
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

// Print writes msg plus a newline to Stdout verbatim — no colour, no log
// write. For output a caller captures and re-parses (`$(admin run-args)`,
// `admin diff --format json`) rather than a diagnostic a person reads on a
// terminal. info/ok/warn/err all wrap msg in an ANSI colour code
// unconditionally (see above) and are therefore unusable for that shape: a
// `[actions.X] log = false` command capturing `$(...)` would get the escape
// codes baked into the captured string. Added by admin-3rgp.37 (docker's
// `run-args` action is the first Lua body needing it) — no host verb existed
// for "write plain stdout" before this.
// Host function: print(msg).
func (h *Host) Print(msg string) {
	fmt.Fprintf(h.Stdout, "%s\n", msg)
}

// SetLogContext names this run's log file by joining non-empty parts with
// "-", deferred until first actual use — see logcontext.go's pendingContext
// doc comment. retain is RetainUnset to fall back to the configured
// default, or an explicit count (0 means "no pruning"). A no-op once a
// context is already active: the LAST call to arrive here before first use
// wins — a command names the log first (dispatch.go's runSteps), and an
// inner call (a multi-target target, a framed action) refines that
// still-pending name rather than being blocked by it.
// Host function: set_log_context(...parts[, {retain=N}]).
func (h *Host) SetLogContext(parts []string, retain int) {
	h.logCtx.SetLogContext(parts, retain)
}

// SetLogContextPath names an explicit log path for this run (a `log_file`
// override), deferred the same way as SetLogContext.
// Host function: set_log_context_path(path[, {retain=N}]).
func (h *Host) SetLogContextPath(path string, retain int) {
	h.logCtx.SetLogContextPath(path, retain)
}

// AppendLog appends text (ANSI-stripped, newline-terminated) to the active
// log file, materializing a pending log context on first use — the same
// path Info/Ok/Warn/Err already write through. Exported so a caller outside
// this package (internal/reload's long-running dev loop, which streams a
// child process's own output rather than a message Host itself composed)
// can feed that output into the same log file Info/Ok/Warn/Err use, without
// duplicating the log-context materialization logic here.
func (h *Host) AppendLog(text string) {
	h.logWrite(text)
}
