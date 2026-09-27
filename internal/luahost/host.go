// Package luahost embeds a pure-Go Lua VM (github.com/yuin/gopher-lua, Lua
// 5.1) and registers the host API an archetype action body written in Lua
// runs against — the surface ADR-0010 (docs/adr/0010-lua-and-bash-are-the-
// authoring-languages.md) settles on to replace kind="python".
//
// A Lua body never gets Lua's own os/io libraries: New opens only base,
// table, string and math (see register.go). Everything that touches the
// process — printing, logging, running a command, prompting the user, JSON,
// globbing, plist — is a Go function bound into the state instead. That is
// the ADR's point: "Bind capability from Go … rather than expecting it from
// Lua's stdlib," so the surface stays the one this package documents, not
// whatever Lua's stdlib happens to expose.
//
// The registered surface is documented at docs/lua-host-api.md — keep that
// file in step with any function added or changed here.
package luahost

import (
	"bufio"
	"io"
	"os"
)

// Host is the process-side state a Lua VM's host functions read and write:
// where output goes, the project root (config-relative paths, run_cmd's
// cwd), and the log-context it borrows (see UseLogContext) for
// info/ok/warn/err/AppendLog and set_log_context/set_log_context_path.
//
// One Host is meant to back one action dispatch. It is not safe for
// concurrent use by two Lua states at once.
type Host struct {
	// ProjectRoot is the directory admin.toml lives in. Relative paths from
	// Lua (set_log_context_path, run_cmd's cwd) resolve against it.
	ProjectRoot string

	// LogDir (conventionally "<ProjectRoot>/tmp") is for callers that want a
	// scratch path under the project's log directory without going through
	// the log-context machinery at all (metro.go's pid/log files,
	// apple_devices_darwin.go's --json-output path). It is independent of
	// logCtx.LogDir — a Host pointed at the shared process LogContext
	// (internal/kinds/helpers.go's newHost) still keeps its own LogDir for
	// these non-log-context uses.
	LogDir string

	// Stdout/Stderr/Stdin are the streams info/ok/warn/err/pick_target/
	// run_cmd use. Defaulted to os.Stdout/os.Stderr/os.Stdin by New; a
	// caller (tests, or a future TUI frame) can redirect them.
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader

	// RunAction, when set, backs the `run_action(name, ...)` Lua global: run
	// another declared action from inside this one — a picker that routes to
	// one of several sub-actions, a teardown that runs three of them in
	// order. Without it those bodies would have no expressible form at all:
	// run_cmd shells out, and a shell cannot reach a sibling action.
	//
	// Left nil (a Host built outside a kind dispatch, or a test), calling
	// run_action from Lua raises rather than silently doing nothing.
	RunAction func(name string, args []string) error

	// logCtx is the deferred log-context this Host writes through. New
	// gives every Host its own private one (this package's ~40 unit tests
	// depend on that isolation); UseLogContext lets a caller (newHost) point
	// it at a shared one instead, so many Hosts materialize into the same
	// file — see logcontext.go's doc comment.
	logCtx *LogContext
}

// New returns a Host defaulted to the real terminal and the given project
// root, with a private LogContext of its own (logging enabled). LogDir
// defaults to "<projectRoot>/tmp".
func New(projectRoot string) *Host {
	return &Host{
		ProjectRoot: projectRoot,
		LogDir:      projectRoot + string(os.PathSeparator) + "tmp",
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Stdin:       os.Stdin,
		logCtx:      NewLogContext(projectRoot),
	}
}

// UseLogContext points h at a log context built (and owned) elsewhere,
// replacing the private one New gave it — internal/kinds/helpers.go's
// newHost calls this with luahost.Process() so every kind-factory Host in
// one command dispatch shares the same deferred log file, while every other
// caller of New (this package's own tests) keeps the default private
// context.
func (h *Host) UseLogContext(c *LogContext) {
	h.logCtx = c
}

// LogFile returns the materialized log path, or "" if no context has been
// set (or logging is disabled). Reading this forces nothing; a body that
// never calls set_log_context just gets "".
func (h *Host) LogFile() string { return h.logCtx.LogFile() }

// MaterializeLogFile forces any pending log context to be created now and
// returns its path — "asking for the path is a use": a caller (the pinned
// footer wants to print a log path before the action has necessarily
// written a line) that read a path and then found the log created
// somewhere else later would be worse than announcing it slightly early.
// Plain LogFile() does NOT do this — a Lua body reading a global must not
// create a log file just by asking what it's named. Empty when no context
// was ever set.
func (h *Host) MaterializeLogFile() string { return h.logCtx.MaterializeLogFile() }

// logWrite appends plain (ANSI-stripped) text to the active log file via
// h's log context. A no-op if logging is disabled or no context has been
// materialized yet. Must never call info/ok/warn/err (which call back into
// this).
func (h *Host) logWrite(text string) { h.logCtx.Write(text) }

// writeLine writes a single already-terminated line to w, ignoring errors —
// used by run_cmd's live-streaming path.
func writeLine(w io.Writer, line string) {
	bw := bufio.NewWriter(w)
	_, _ = bw.WriteString(line)
	_ = bw.Flush()
}
