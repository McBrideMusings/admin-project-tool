// Package logbridge implements the per-process HTTP listener that receives
// batched browser console-log entries and appends them to the same log file
// the wrapping action is already writing.
//
// A project opts in with `[log_bridge] hosts = [...]` in admin.toml. An
// interactive-shell (or bridge-listen) action then calls Start before
// spawning its wrapped process and Stop in its cleanup path — internal/
// keybindings is the one caller.
//
// Two things this package must never silently change, both named in this
// repo's CLAUDE.md: the bind is 0.0.0.0 unconditionally (anyone on the
// tailnet can POST — that is the existing documented behaviour), and a
// logged entry carries the page's HOST, never the full URL (auth tokens
// live in query strings).
package logbridge

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/logformat"
)

// DefaultPortRangeStart/End is the default log-bridge port range, inclusive
// on both ends (9988-9999).
const (
	DefaultPortRangeStart = 9988
	DefaultPortRangeEnd   = 9999
)

// BindHost is the address every listener binds — unconditionally 0.0.0.0 so
// Tailscale peers can reach it. Do not narrow or widen this; see the
// package doc comment.
const BindHost = "0.0.0.0"

// DiscoveryDir is where each running listener drops a small JSON file naming
// its port/hosts/pid — a debugging aid, not load-bearing for delivery.
var DiscoveryDir = "/tmp/admin-project-tool"

// Config is one project's [log_bridge] table, resolved: Hosts already had
// any ${VAR} expanded by the caller, PortRange is (0, 0) to mean "use the
// default range".
type Config struct {
	Hosts     []string
	PortRange [2]int
	Project   string
}

// Enabled reports whether cfg declares at least one host — log_bridge is
// opt-in.
func (c Config) Enabled() bool { return len(c.Hosts) > 0 }

func (c Config) portRange() (start, end int) {
	if c.PortRange[0] != 0 && c.PortRange[1] != 0 {
		return c.PortRange[0], c.PortRange[1]
	}
	return DefaultPortRangeStart, DefaultPortRangeEnd
}

// consoleLevels maps a browser console level onto admin's own severity
// words. console.log carries no severity of its own and maps to INFO,
// exactly like a server line at the same visual weight.
var consoleLevels = map[string]string{
	"log": "INFO", "info": "INFO",
	"warn": "WARN", "warning": "WARN",
	"error": "ERROR",
	"debug": "DEBUG",
	"trace": "TRACE",
}

func mapLevel(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		raw = "log"
	}
	if lvl, ok := consoleLevels[raw]; ok {
		return lvl
	}
	return strings.ToUpper(raw)
}

// innerPrefixRe matches an app's own logger prefix printed INTO the console
// message: a wall clock, then its own level. Only the full HH:MM:SS[.mmm]
// [LEVEL] shape counts, never a message that merely starts with a clock.
var innerPrefixRe = regexp.MustCompile(
	`(?i)^\d{2}:\d{2}:\d{2}(?:[.,]\d+)?\s+\[(ERROR|ERR|FATAL|CRIT|CRITICAL|WARN|WARNING|INFO|DEBUG|TRACE|LOG)\]\s*`)

// UnwrapInnerPrefix splits an app-logger prefix off a console message,
// returning (level, remainder) with ok=false when msg has no such prefix.
// The stripped clock is discarded: the entry's own timestamp is the same
// instant and carries a date, so nothing is lost.
func UnwrapInnerPrefix(msg string) (level, remainder string, ok bool) {
	loc := innerPrefixRe.FindStringSubmatchIndex(msg)
	if loc == nil {
		return "", msg, false
	}
	level = strings.ToUpper(msg[loc[2]:loc[3]])
	return level, msg[loc[1]:], true
}

// Entry is one POSTed console-log record, as the browser sends it.
type Entry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Host      string `json:"host"`
	URL       string `json:"url"`
}

// entryHost returns entry's page host — new clients send `host` directly;
// older ones sent the full `url` (which carried auth tokens in its query
// string), so that's parsed down to just the hostname.
func entryHost(e Entry) string {
	if e.Host != "" {
		return e.Host
	}
	if e.URL == "" {
		return ""
	}
	u, err := url.Parse(e.URL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// sortLayout is a fixed-width, zero-padded ISO layout: fixed width means a
// lexicographic string sort on its output matches chronological order,
// which is all inTimeOrder needs — it never surfaces this string, only uses
// it as a sort key.
const sortLayout = "2006-01-02T15:04:05.000000000"

// inTimeOrder sorts one POSTed batch by the entries' own timestamps. The
// userscript buffers from document-start but only flushes once its listener
// probe resolves, so a frame's first POST can be a backlog predating lines
// another frame already delivered; sorting the batch puts that backlog back
// in order. A stable sort with the previous valid key carried forward
// leaves an entry with an unparseable timestamp exactly where the client
// put it.
func inTimeOrder(entries []Entry) []Entry {
	type keyed struct {
		key string
		idx int
		e   Entry
	}
	out := make([]keyed, len(entries))
	last := ""
	for i, e := range entries {
		if e.Timestamp != "" {
			if norm, ok := logformat.NormalizeTimestamp(e.Timestamp, sortLayout); ok {
				last = norm
			}
		}
		out[i] = keyed{key: last, idx: i, e: e}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].key != out[j].key {
			return out[i].key < out[j].key
		}
		return out[i].idx < out[j].idx
	})
	result := make([]Entry, len(out))
	for i, k := range out {
		result[i] = k.e
	}
	return result
}

// FormatEntry renders one bridged console entry as one or more log lines.
// Shape is `<iso-timestamp> [LEVEL] [CLIENT] <message>`, matching the
// `[LEVEL] <scope> message` order a server process prints so both streams
// render identically once admin's formatter strips the timestamp and
// colorizes the level. A message with embedded newlines (a stack trace)
// becomes one stamped line per physical line, with the level/[CLIENT] tag
// on the head line only.
func FormatEntry(e Entry) string {
	ts := e.Timestamp
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339Nano)
	}
	level := mapLevel(e.Level)
	msg := strings.TrimRight(e.Message, "\n")
	if inner, rest, ok := UnwrapInnerPrefix(msg); ok {
		// The app's own level beats the browser's: a logger that routes
		// debug through console.log is reported by the browser as `log`.
		level = mapLevel(inner)
		msg = rest
	}
	lines := strings.Split(msg, "\n")
	var b strings.Builder
	b.WriteString(ts + " [" + level + "] [CLIENT] " + lines[0] + "\n")
	for _, cont := range lines[1:] {
		b.WriteString(ts + " " + cont + "\n")
	}
	return b.String()
}

// Bridge is one running log-bridge HTTP listener. Kept as an instance (not
// package globals) so a test can start several without interfering with
// each other; production code starts exactly one per process.
type Bridge struct {
	mu            sync.Mutex
	logPath       string
	hosts         []string
	project       string
	pid           int
	startedAt     string
	port          int
	server        *http.Server
	listener      net.Listener
	discoveryPath string
	sink          func(line string)
	seenClients   map[string]bool
}

// bindFreePort scans port_range for a free port and returns it still bound.
// Returning the open listener (not a bare port number, then closing and
// rebinding later) makes acquisition atomic: no window for another process
// to steal the port between a probe and the real bind.
func bindFreePort(start, end int) (net.Listener, int, error) {
	for port := start; port <= end; port++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(BindHost, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		return ln, port, nil
	}
	return nil, 0, &portRangeError{start, end}
}

type portRangeError struct{ start, end int }

func (e *portRangeError) Error() string {
	return "log_bridge: no free port in " + strconv.Itoa(e.start) + "-" + strconv.Itoa(e.end)
}

// sweepStaleDiscoveryFiles removes discovery files naming a pid that is no
// longer alive, or that fail to parse.
func sweepStaleDiscoveryFiles() {
	entries, err := os.ReadDir(DiscoveryDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(DiscoveryDir, entry.Name())
		data, err := os.ReadFile(path)
		stale := false
		if err != nil {
			stale = true
		} else {
			var rec struct {
				PID int `json:"pid"`
			}
			if err := json.Unmarshal(data, &rec); err != nil || !isPidAlive(rec.PID) {
				stale = true
			}
		}
		if stale {
			_ = os.Remove(path)
		}
	}
}

// Start binds a free port from cfg's range and begins serving — a no-op
// (nil, nil) when cfg is not Enabled(). ADMIN_LOG_BRIDGE_HOSTS, when set,
// overrides cfg.Hosts for this one listener (comma-separated) — the
// per-dispatch narrowing kind="bridge-listen" uses so two bridges in the
// same project isolate by host.
func Start(logPath string, cfg Config) (*Bridge, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	hosts := cfg.Hosts
	if override := strings.TrimSpace(os.Getenv("ADMIN_LOG_BRIDGE_HOSTS")); override != "" {
		var overridden []string
		for _, h := range strings.Split(override, ",") {
			if h = strings.TrimSpace(h); h != "" {
				overridden = append(overridden, h)
			}
		}
		hosts = overridden
	}

	if err := os.MkdirAll(DiscoveryDir, 0o755); err != nil {
		return nil, err
	}
	sweepStaleDiscoveryFiles()

	start, end := cfg.portRange()
	ln, port, err := bindFreePort(start, end)
	if err != nil {
		return nil, err
	}

	b := &Bridge{
		logPath:     logPath,
		hosts:       hosts,
		project:     cfg.Project,
		pid:         os.Getpid(),
		startedAt:   time.Now().UTC().Format(time.RFC3339),
		port:        port,
		listener:    ln,
		seenClients: map[string]bool{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", b.handleHealth)
	mux.HandleFunc("/log", b.handleLog)
	b.server = &http.Server{Handler: mux}
	go func() { _ = b.server.Serve(ln) }()

	b.discoveryPath = filepath.Join(DiscoveryDir, strconv.Itoa(b.pid)+".json")
	discovery, _ := json.Marshal(map[string]any{
		"port": b.port, "hosts": b.hosts, "project": b.project,
		"pid": b.pid, "started_at": b.startedAt, "log_path": b.logPath,
	})
	_ = os.WriteFile(b.discoveryPath, discovery, 0o644)

	return b, nil
}

// Port is the port this bridge bound.
func (b *Bridge) Port() int { return b.port }

// SetSink routes every appended line through fn instead of leaving it
// unrouted — internal/keybindings wires this to frame.Write through this
// action's formatter, so a browser line and a server line reach the
// terminal indistinguishably. A nil fn (or never calling SetSink) falls
// back to a raw os.Stdout write — callers of this package should always set
// a sink in production; the fallback here exists only so a unit test
// exercising Start/append doesn't need one.
func (b *Bridge) SetSink(fn func(line string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sink = fn
}

// ClearSink removes the sink set by SetSink.
func (b *Bridge) ClearSink() { b.SetSink(nil) }

// Stop shuts the listener down and removes the discovery file. Safe to call
// once; b is unusable afterward.
func (b *Bridge) Stop() {
	if b.server != nil {
		_ = b.server.Close()
	}
	if b.discoveryPath != "" {
		_ = os.Remove(b.discoveryPath)
	}
}

func (b *Bridge) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"port": b.port, "hosts": b.hosts, "project": b.project,
		"pid": b.pid, "started_at": b.startedAt,
	})
}

func (b *Bridge) handleLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	var entries []Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	ua := r.Header.Get("User-Agent")
	if ua == "" {
		ua = "unknown"
	}
	count := b.Append(entries, ua)
	writeJSON(w, http.StatusOK, map[string]any{"written": count})
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	data, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// Append sorts entries into timestamp order, formats each into log lines,
// appends them to the bridge's log file, and routes them to the sink (or
// writes them to os.Stdout when no sink is set). Returns how many entries
// were written.
func (b *Bridge) Append(entries []Entry, userAgent string) int {
	if b.logPath == "" || len(entries) == 0 {
		return 0
	}
	ordered := inTimeOrder(entries)

	b.mu.Lock()
	var lines []string
	host := entryHost(ordered[0])
	seenKey := host
	if seenKey == "" {
		seenKey = userAgent
	}
	if seenKey != "" && !b.seenClients[seenKey] {
		b.seenClients[seenKey] = true
		label := host
		if label == "" {
			label = "unknown host"
		}
		lines = append(lines, "[log-bridge] client connected: "+label+"  ("+orUnknown(userAgent)+")\n")
	}
	for _, e := range ordered {
		formatted := FormatEntry(e)
		// One physical line per element, keepends — a line-at-a-time sink
		// (a formatter wrapping frame.Write) must stamp every line of a
		// multi-line message, not only the first.
		for _, ln := range splitKeepEnds(formatted) {
			lines = append(lines, ln)
		}
	}
	sink := b.sink
	b.mu.Unlock()

	f, err := os.OpenFile(b.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		for _, ln := range lines {
			_, _ = f.WriteString(ln)
		}
		_ = f.Close()
	}

	if sink != nil {
		for _, ln := range lines {
			sink(ln)
		}
	} else {
		for _, ln := range lines {
			_, _ = os.Stdout.WriteString(ln)
		}
	}
	return len(ordered)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// splitKeepEnds splits s into lines, each retaining its trailing "\n".
func splitKeepEnds(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
