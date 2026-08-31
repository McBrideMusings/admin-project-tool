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
	"fmt"
	"io"
	"os"
	"regexp"
	"time"
)

// Host is the process-side state a Lua VM's host functions read and write:
// where output goes, the project root (config-relative paths, .env, log
// directory), and the deferred log-context machinery ported from
// admin_lib/core.py's set_log_context/_materialize_log_context.
//
// One Host is meant to back one action dispatch. It is not safe for
// concurrent use by two Lua states at once — set_log_context's pending/
// active state is per-dispatch, exactly as it is in the Python interpreter.
type Host struct {
	// ProjectRoot is the directory admin.toml lives in. Relative paths from
	// Lua (set_log_context_path, run_cmd's cwd) resolve against it.
	ProjectRoot string

	// LogDir is where a stem-named log context creates its file — mirrors
	// admin_lib/core.py's LOG_DIR (conventionally "<ProjectRoot>/tmp").
	LogDir string

	// Stdout/Stderr/Stdin are the streams info/ok/warn/err/pick_target/
	// run_cmd use. Defaulted to os.Stdout/os.Stderr/os.Stdin by New; a
	// caller (tests, or a future TUI frame) can redirect them.
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader

	// logFile is the materialized path once a pending context is realized.
	// Empty until then, exactly like Python's LOG_FILE.
	logFile          string
	logContextActive bool
	pendingContext   *pendingContext
	logEnabled       bool
}

// pendingContext mirrors admin_lib/core.py's module-level _pending_context:
// a log-file name or path is held pending — not created — until first use,
// so an outer set_log_context and an inner one (a multi-target step
// re-scoping under a step-runner) don't both materialize a file. Whichever
// call fires first wins; see host_log.go's setLogContext for the same
// "already taken" guard as _log_context_taken.
type pendingContext struct {
	stem   bool // true: value is a name stem, timestamped into LogDir. false: value is an explicit path.
	value  string
	retain int // 0 means "no pruning" (Python's None)
}

// New returns a Host defaulted to the real terminal and the given project
// root, with logging enabled. LogDir defaults to "<projectRoot>/tmp",
// matching admin_lib/core.py's LOG_DIR.
func New(projectRoot string) *Host {
	return &Host{
		ProjectRoot: projectRoot,
		LogDir:      projectRoot + string(os.PathSeparator) + "tmp",
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Stdin:       os.Stdin,
		logEnabled:  true,
	}
}

// LogFile returns the materialized log path, or "" if no context has been
// set (or logging is disabled). Reading this — like Python's LOG_FILE global
// — forces nothing; a body that never calls set_log_context just gets "".
func (h *Host) LogFile() string { return h.logFile }

// ansiEscape strips every ANSI escape sequence, not just SGR colour codes —
// a log file should carry no escape sequences at all, matching
// admin_lib/core.py's _ANSI_ESCAPE_RE.
var ansiEscape = regexp.MustCompile("\x1b(?:[@-Z\\\\-_]|\\[[0-?]*[ -/]*[@-~])")

// logWrite appends plain (ANSI-stripped) text to the active log file. A
// no-op if logging is disabled or no context has been materialized yet —
// mirrors admin_lib/core.py's _log_write, including that it must never call
// info/ok/warn/err (which call back into this).
func (h *Host) logWrite(text string) {
	if !h.logEnabled {
		return
	}
	h.materializeLogContext()
	if h.logFile == "" {
		return
	}
	plain := ansiEscape.ReplaceAllString(text, "")
	if len(plain) == 0 || plain[len(plain)-1] != '\n' {
		plain += "\n"
	}
	f, err := os.OpenFile(h.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return // best-effort, matching Python's bare except OSError: pass
	}
	defer f.Close()
	_, _ = f.WriteString(plain)
}

// materializeLogContext creates and announces the pending log file on first
// actual use — the moment the name stops changing. Every path that touches
// the log (logWrite, or a caller asking LogFile()) must go through here
// first, exactly as admin_lib/core.py's _materialize_log_context is the
// single choke point.
func (h *Host) materializeLogContext() {
	if h.pendingContext == nil || h.logContextActive {
		return
	}
	pc := h.pendingContext
	h.pendingContext = nil
	if pc.stem {
		ts := time.Now().Format("2006-01-02_150405")
		h.logFile = h.LogDir + string(os.PathSeparator) + pc.value + "." + ts + ".log"
		_ = os.MkdirAll(h.LogDir, 0o755)
	} else {
		path := pc.value
		if !isAbs(path) {
			path = h.ProjectRoot + string(os.PathSeparator) + path
		}
		h.logFile = path
		_ = os.MkdirAll(dirOf(h.logFile), 0o755)
	}
	f, err := os.Create(h.logFile)
	if err == nil {
		f.Close()
	}
	h.logContextActive = true
	if h.pruneRetain(pc) {
		// pruning is best-effort; errors are swallowed like Python's.
	}
	fmt.Fprintf(h.Stderr, "\033[2m→ wrote: %s\033[0m\n", h.logFile)
}

func isAbs(p string) bool { return len(p) > 0 && p[0] == '/' }

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

// pruneRetain is a stub for admin_lib/core.py's _prune_logs(stem, retain):
// deleting older same-stem logs beyond `retain`. Left a no-op (retain is
// accepted but not enforced) — pruning policy is not part of this ticket's
// scope (embedding the VM and the host API), and a no-op prune never loses a
// log, only fails to clean one up. Tracked as a follow-up.
func (h *Host) pruneRetain(pc *pendingContext) bool { return true }

// writeLine writes a single already-terminated line to w, ignoring errors —
// used by run_cmd's live-streaming path.
func writeLine(w io.Writer, line string) {
	bw := bufio.NewWriter(w)
	_, _ = bw.WriteString(line)
	_ = bw.Flush()
}
