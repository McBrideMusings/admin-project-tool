// logcontext.go is the Go port of admin_lib/core.py's module-level
// set_log_context/_materialize_log_context machinery, hoisted into its own
// type so every luahost.Host a single command dispatch builds (26 kind
// factories, each with its own Host — internal/kinds/helpers.go's newHost)
// can share ONE deferred log-context rather than each Host racing to
// materialize its own file. Python got this for free because
// _pending_context/_log_context_active/LOG_FILE are bare module globals,
// shared by construction; Go's equivalent of "shared by construction" is
// one LogContext value every Host in a dispatch borrows (Host.UseLogContext)
// rather than owns.
//
// admin-rc82 is the ticket that made this unconditional: every command names
// a log before its first step runs (internal/kinds/dispatch.go's runSteps),
// not just a step body that happens to call set_log_context itself. See that
// file's own doc comment for the full port.
package luahost

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/logformat"
)

// RetainUnset marks "no explicit retain value was given" — the caller falls
// back to the LogContext's configured default (5, or an explicit
// `[logging] retain`). It is distinct from 0, which explicitly disables
// pruning/rotation — mirroring Python's retain=None (fall back to
// LOG_CONFIG.get("retain", 5)) versus retain=0 (prune to nothing... no,
// prune to zero pruning: n == 0 means _prune_logs/_rotate_log return
// immediately). Any real caller that means "I did not set retain" must pass
// this constant, never 0.
const RetainUnset = -1

// LogAnnouncePrefix is the literal text announce writes to Stderr, dimmed,
// immediately before the log path, so a person running a command in a
// terminal sees where its log went.
const LogAnnouncePrefix = "→ wrote: "

// LogAnnounceFDEnv names the file descriptor a hosted child writes its log
// path to, one path per line, instead of printing the announce line.
// internal/kinds/hostedpty.go sets it and reads the other end of the pipe,
// so the dashboard learns the path without parsing the child's output.
const LogAnnounceFDEnv = "ADMIN_LOG_ANNOUNCE_FD"

// announceFDNum is the descriptor LogAnnounceFDEnv names, or false when the
// variable is unset or invalid.
func announceFDNum() (int, bool) {
	raw, ok := os.LookupEnv(LogAnnounceFDEnv)
	if !ok {
		return 0, false
	}
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < 3 {
		return 0, false
	}
	return fd, true
}

// init marks the announce pipe close-on-exec before this process can start
// anything. The descriptor arrives inherited and without that flag, so every
// step's shell — and anything it daemonizes past the process group — would
// hold the write end open, and the dashboard's reader would wait on it for
// as long as that grandchild lives.
func init() {
	if fd, ok := announceFDNum(); ok {
		syscall.CloseOnExec(fd)
	}
}

// announceFD is the pipe LogAnnounceFDEnv names, opened once and never
// closed (os.NewFile's finalizer would close the descriptor if a second
// *os.File for it were dropped). nil when the variable is unset or invalid.
var announceFD = sync.OnceValue(func() *os.File {
	fd, ok := announceFDNum()
	if !ok {
		return nil
	}
	return os.NewFile(uintptr(fd), "log-announce")
})

// announce reports the log path: over LogAnnounceFDEnv's pipe when a
// dashboard hosts this process, otherwise as a dimmed line on Stderr.
func (c *LogContext) announce() {
	if f := announceFD(); f != nil {
		fmt.Fprintln(f, c.logFile)
		return
	}
	fmt.Fprintf(c.Stderr, "\033[2m%s%s\033[0m\n", LogAnnouncePrefix, c.logFile)
}

// pendingContext mirrors admin_lib/core.py's module-level _pending_context:
// a log-file name or path is held pending — not created — until first use,
// so an outer set_log_context (a command's step-runner) and an inner one (a
// multi-target target re-scoping, a framed action) don't both materialize a
// file. Whichever call fires first (i.e. is still pending when the log is
// first actually used) wins; every call before that just replaces it —
// innermost caller wins because it runs last, closest to first use.
type pendingContext struct {
	stem   bool // true: value is a name stem, timestamped into LogDir. false: value is an explicit path.
	value  string
	retain int // RetainUnset, or an explicit retain count (0 == no pruning/rotation).
}

// LogContext is the process-level owner of one command dispatch's deferred
// log-file state — the Go analogue of admin_lib/core.py's
// LOG_DIR/LOG_FILE/LOG_CONFIG/_pending_context/_log_context_active module
// globals, minus the bit where Python gets sharing for free.
//
// internal/kinds/helpers.go's newHost points every kind-factory Host at
// the single package-level instance (Process()), so a command's 26-ish
// Hosts (one per step/target/frame) all materialize into the same file.
// luahost.New, by contrast, gives each Host its OWN private LogContext —
// this package's own ~40 unit tests dispatch a Host directly and must stay
// isolated from each other and from anything internal/kinds does.
type LogContext struct {
	mu sync.Mutex

	// ProjectRoot resolves a relative set_log_context_path/log_file.
	ProjectRoot string
	// LogDir is where a stem-named context creates its file — "<root>/tmp"
	// by default, overridable via Configure's `[logging] dir`.
	LogDir string
	// Stderr is where the "→ wrote: <path>" announce line goes. Defaults to
	// os.Stderr; newHost points the process context's Stderr at the
	// dispatching Host's own Stderr so the announce lands on the run's
	// actual output stream, not wherever the process's stderr happens to be
	// wired.
	Stderr io.Writer

	enabled       bool
	retainDefault int

	pending      *pendingContext
	logFile      string
	active       bool
	announcedEnd bool
}

// NewLogContext returns a LogContext rooted at projectRoot, logging
// enabled, retain defaulted to 5 (matching Python's
// `LOG_CONFIG.get("retain", 5)` when no `[logging]` table overrides it).
func NewLogContext(projectRoot string) *LogContext {
	return &LogContext{
		ProjectRoot:   projectRoot,
		LogDir:        filepath.Join(projectRoot, "tmp"),
		Stderr:        os.Stderr,
		enabled:       true,
		retainDefault: 5,
	}
}

var (
	processMu  sync.Mutex
	processCtx *LogContext
)

// Process returns the package-level LogContext every kind-dispatch Host
// shares, lazily created (rooted at the current working directory, which
// this repo's interpreter always cd's to the project root before dispatch —
// see the Guards section of this repo's own docs) on first use.
func Process() *LogContext {
	processMu.Lock()
	defer processMu.Unlock()
	if processCtx == nil {
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "."
		}
		processCtx = NewLogContext(cwd)
	}
	return processCtx
}

// ResetProcessLogContext replaces the process-wide LogContext with a fresh
// one rooted at projectRoot. It is a test seam: since admin-rc82 made
// runSteps name a log unconditionally, any test that dispatches a real
// command through the shared registry writes a log file the instant it
// materializes one — pointing the process context at a t.TempDir() first is
// what keeps that write out of the source tree.
func ResetProcessLogContext(projectRoot string) {
	processMu.Lock()
	defer processMu.Unlock()
	processCtx = NewLogContext(projectRoot)
}

// Configure applies (or re-applies) a manifest's `[logging]` table and
// re-roots c at projectRoot — the Go port of admin_lib/core.py's _init +
// _apply_log_config.
//
// **It does not touch pending, logFile or active**, and that is the whole
// point of the function. In Python those two calls run once per process from
// boot(); naming a log is a separate concern that runs per command and is
// guarded by _log_context_taken(). This port originally cleared the context
// here, on the theory that "a Go process runs exactly one command per
// Configure call". That premise is false: internal/kinds/dispatch.go:217
// routes surf.Command straight back into runSteps, which calls Configure
// again, and three shipped kinds take that hop before starting
// (vitedev.go, docker.go, cleandev.go, each via runPreHook's
// `surf.Command(<project-declared name>, …)`).
//
// Clearing here therefore reset `active` before taken() was ever consulted,
// so `admin dev` materialized tmp/dev.<ts>.log, hit the pre-kill hop, and
// wrote every line after it to tmp/kill.<ts>.log — one invocation, two
// files, the second named after a sub-step. That is issue #474 re-entered
// through a different door (admin-rc82.1).
//
// Test isolation, the other job the clearing was doing, belongs to
// ResetProcessLogContext, which internal/kinds/testhelpers_test.go already
// calls before every dispatching test.
func (c *LogContext) Configure(projectRoot string, logging map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ProjectRoot = projectRoot
	c.LogDir = filepath.Join(projectRoot, "tmp")
	c.enabled = true
	c.retainDefault = 5
	if logging == nil {
		return
	}
	if d, ok := logging["dir"].(string); ok && d != "" {
		c.LogDir = filepath.Join(projectRoot, d)
	}
	if e, ok := logging["enabled"].(bool); ok {
		c.enabled = e
	}
	if r, ok := asConfigInt(logging["retain"]); ok {
		c.retainDefault = r
	}
}

func asConfigInt(v any) (int, bool) {
	switch n := v.(type) {
	case int64:
		return int(n), true
	case int:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

// SetStderr redirects where c announces a materialized log path — see
// newHost's doc comment for why every dispatching Host wants that landing
// on its own Stderr rather than whichever Host happened to materialize the
// file first.
func (c *LogContext) SetStderr(w io.Writer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Stderr = w
}

// SetEnabled turns logging on or off for the rest of this context's life —
// the Go port of a command's `[logging] log = false` (which bypasses
// set_log_context entirely, so nothing this context does for the rest of
// the run creates a file) and of Python's `LOG_CONFIG.get("enabled") is
// False`.
func (c *LogContext) SetEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabled = enabled
}

// taken mirrors admin_lib/core.py's _log_context_taken: a context is
// already live and owns a real file. Caller must hold c.mu.
func (c *LogContext) taken() bool {
	return c.active && c.logFile != ""
}

// SetLogContext names this run's log file by joining non-empty parts with
// "-", deferred until first actual use. A no-op once a context is already
// materialized (taken) — see pendingContext's doc comment for why the
// LAST caller before first use wins rather than the first.
func (c *LogContext) SetLogContext(parts []string, retain int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled || c.taken() {
		return
	}
	name := joinNonEmpty(parts, "-")
	c.pending = &pendingContext{stem: true, value: name, retain: retain}
}

// SetLogContextPath names an explicit log path for this run (a `log_file`
// override), deferred the same way as SetLogContext.
func (c *LogContext) SetLogContextPath(path string, retain int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled || c.taken() {
		return
	}
	c.pending = &pendingContext{stem: false, value: path, retain: retain}
}

// LogFile returns the materialized log path, or "" if no context has been
// set (or logging is disabled) or none has been used yet. Reading this
// forces nothing — a body that never triggers a write, and never calls
// MaterializeLogFile, just gets "".
func (c *LogContext) LogFile() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logFile
}

// MaterializeLogFile forces any pending log context to be created now and
// returns its path — the Go port of admin_lib/core.py's get_log_file():
// "asking for the path is a use". Empty when no context was ever set (or
// logging is disabled).
func (c *LogContext) MaterializeLogFile() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.materializeLocked()
	return c.logFile
}

// AnnounceEnd prints the materialized log path to Stderr once, the Go port
// of admin_lib/core.py's _announce_log_end — the "→ wrote: <path>" line
// Python printed both explicitly right after a command's func() returned
// (core.py:1701-1702, core.py:1412 for the interactive menu) and, as a
// safety net for any other exit path, from an atexit.register hook
// (core.py:449). Go has no atexit; callers get the same coverage by
// deferring AnnounceEnd at the outermost per-process dispatch boundary
// (cmd/root.go's three RunE closures) so it fires whether the run
// succeeds or fails, without needing a second registration.
//
// A no-op when nothing ever materialized this run (active is false — e.g.
// a `branch`/`worktree` verb dispatch that never touched logging) and a
// no-op on any call after the first: unlike Python, which reuses the
// consumed _log_context_active flag both to gate this print AND to answer
// "is a context live" elsewhere (_log_context_taken), Go's taken() must
// keep answering that question correctly for the rest of this
// LogContext's life — so AnnounceEnd tracks its own one-shot
// announcedEnd flag instead of clearing active.
func (c *LogContext) AnnounceEnd() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active || c.logFile == "" || c.announcedEnd {
		return
	}
	c.announcedEnd = true
	c.announce()
}

// Write appends text (ANSI-stripped, newline-terminated) to the active log
// file, materializing a pending context on first use — the same path
// Host.Info/Ok/Warn/Err write through.
func (c *LogContext) Write(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return
	}
	c.materializeLocked()
	if c.logFile == "" {
		return
	}
	plain := logformat.AnsiEscapeRe.ReplaceAllString(text, "")
	if len(plain) == 0 || plain[len(plain)-1] != '\n' {
		plain += "\n"
	}
	f, err := os.OpenFile(c.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return // best-effort, matching Python's bare except OSError: pass
	}
	defer f.Close()
	_, _ = f.WriteString(plain)
}

// materializeLocked creates and announces the pending log file on first
// actual use — the moment the name stops changing. Caller must hold c.mu.
func (c *LogContext) materializeLocked() {
	if c.pending == nil || c.active {
		return
	}
	pc := c.pending
	c.pending = nil
	retain := pc.retain
	if retain == RetainUnset {
		retain = c.retainDefault
	}
	if pc.stem {
		// Nanosecond precision, underscore-joined onto the same
		// dot-delimited segment (never a second dot — internal/tui/work.go's
		// logStemTimestamp takes everything after the LAST dot before
		// ".log", so an added dot would truncate the extracted timestamp to
		// just the fractional tail). Fixed-width, zero-padded, so
		// pruneLogsLocked's sort.Strings and commandLogPath's lexicographic
		// bestTS comparison keep tracking chronological order. This is what
		// makes two dispatches of the same action inside the same
		// wall-clock second (admin-yypo.1) resolve to two distinct files
		// instead of the second silently overwriting the first — a
		// same-process in-process counter would not do the job here, since
		// each hostedpty dispatch re-execs a brand-new OS process with its
		// own fresh LogContext.
		now := time.Now()
		ts := fmt.Sprintf("%s_%09d", now.Format("2006-01-02_150405"), now.Nanosecond())
		c.logFile = filepath.Join(c.LogDir, pc.value+"."+ts+".log")
		_ = os.MkdirAll(c.LogDir, 0o755)
		f, err := os.Create(c.logFile)
		if err == nil {
			f.Close()
		}
		c.pruneLogsLocked(pc.value, retain)
	} else {
		path := pc.value
		if !filepath.IsAbs(path) {
			path = filepath.Join(c.ProjectRoot, path)
		}
		c.logFile = path
		_ = os.MkdirAll(filepath.Dir(c.logFile), 0o755)
		c.rotateLogLocked(c.logFile, retain)
		f, err := os.Create(c.logFile)
		if err == nil {
			f.Close()
		}
	}
	c.active = true
	c.announce()
}

// pruneLogsLocked deletes older same-stem logs beyond retain, matching
// admin_lib/core.py's _prune_logs(stem, retain). retain == 0 means "no
// pruning" (Python's n == 0 early return). Errors are silently swallowed —
// pruning is best-effort, exactly like the Python original's bare except.
func (c *LogContext) pruneLogsLocked(stem string, retain int) {
	if retain == 0 {
		return
	}
	pattern := filepath.Join(c.LogDir, escapedGlob(stem)+".*.log")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	if len(matches) <= retain {
		return
	}
	sort.Strings(matches)
	for _, path := range matches[:len(matches)-retain] {
		_ = os.Remove(path)
	}
}

// rotateLogLocked ports admin_lib/core.py's _rotate_log: numbered-suffix
// rotation for an explicit-path (log_file) context, run before the fresh
// file is created. retain == 0 means "no rotation". A path that doesn't
// exist yet (the first-ever run under this log_file) is left alone — there
// is nothing to rotate.
func (c *LogContext) rotateLogLocked(path string, retain int) {
	if retain == 0 {
		return
	}
	if _, err := os.Stat(path); err != nil {
		return
	}
	for i := retain - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", path, i)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, fmt.Sprintf("%s.%d", path, i+1))
		}
	}
	_ = os.Rename(path, path+".1")
}

// escapedGlob escapes glob metacharacters in a stem name — the Go analogue
// of Python's glob.escape(), used so a stem containing *, ?, or [ is
// matched literally rather than as a pattern.
func escapedGlob(s string) string {
	s = strings.ReplaceAll(s, "[", "[[]")
	s = strings.ReplaceAll(s, "*", "[*]")
	s = strings.ReplaceAll(s, "?", "[?]")
	return s
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
