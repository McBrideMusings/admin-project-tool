// Package logformat renders one line of subprocess/log output the same way
// everywhere admin prints one: strip whatever timestamp the source already
// printed, stamp one HH:MM:SS column, colorize the four severity levels by
// hand, and colorize scope tags (`[api]`, `[Messaging]`) by a hash of the
// label so a tag reads the same color in every project and every run.
//
// This is the Go port of admin_lib/log_format.py. It has no dependency on
// internal/frame or internal/streams — a formatter is a pure string-in,
// string-out function, and every pump wires one in as a constructor
// parameter rather than this package knowing about pumps or terminals.
package logformat

import (
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"regexp"
	"strings"
	"time"
)

// ANSI codes — mirrors admin_lib/core.py's class C.
const (
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
	ansiDim    = "\x1b[2m"
	ansiReset  = "\x1b[0m"
)

// Each pattern eats at most ONE trailing space/tab after the stamp, not the
// whole run: the rest is the line's own indentation, and a stack trace's
// continuation lines are unreadable without it. Go's regexp has no
// look-behind and (unlike Python's re.match) does not anchor at position 0
// by default, so every pattern here carries its own explicit "^".
var timestampPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}[.,]\d+([+-]\d{4}|Z)?[ \t]?`),
	regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}([+-]\d{4}|Z)?[ \t]?`),
	regexp.MustCompile(`^\d{2}:\d{2}:\d{2}[.,]?\d*[ \t]?`),
}

// StripLogTimestamp strips a leading timestamp from line, returning
// (timestamp, remainder). Returns ("", line) if no pattern matches.
func StripLogTimestamp(line string) (string, string) {
	for _, pat := range timestampPatterns {
		if loc := pat.FindStringIndex(line); loc != nil {
			return strings.TrimRight(line[:loc[1]], " \t"), line[loc[1]:]
		}
	}
	return "", line
}

// AnsiEscapeRe strips every ANSI escape sequence, not just SGR colour codes —
// admin_lib/core.py's _ANSI_ESCAPE_RE. The single definition every ANSI-aware
// stripper in this repo imports rather than redeclaring — internal/luahost's
// LogContext (ParseLogAnnounce, Write) uses this same regexp instead of
// keeping its own byte-for-byte copy. Used here only to see through color
// codes when StampLine checks whether a chunk already opens with its own
// timestamp; the returned/stamped text itself keeps its original escapes.
var AnsiEscapeRe = regexp.MustCompile("\x1b(?:[@-Z\\\\-_]|\\[[0-?]*[ -/]*[@-~])")

// StampLine prefixes an arbitrary chunk of subprocess output with an
// absolute wall-clock timestamp (HH:MM:SS.mmm) — the Go port of
// admin_lib/core.py's _stamp_log, meant to be wired in as
// internal/streams.OutputPump.DisplayTransform for a pump that has no
// Formatter (one already stamps its own lines via FormatLine, so a pump
// running one is left alone to avoid a double stamp).
//
// Unlike FormatLine, this never buffers: it stamps whatever chunk Write
// receives, complete line or newline-less fragment alike, so it is safe on a
// pump that must let a prompt with no trailing newline (e.g. `Type
// "deploy": `) reach the terminal live — buffering until "\n" would hold
// that prompt forever.
//
// A blank chunk passes through unstamped, and a chunk that already opens
// with its own timestamp (seen through any ANSI color codes) passes through
// untouched — stamping it would prepend the time admin read the chunk in
// front of the time the event happened, which is wrong exactly when it
// matters: a source that arrives pre-stamped (docker logs -t, a re-tailed
// admin log) keeps its own, often higher-precision, event time.
func StampLine(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	if ts, _ := StripLogTimestamp(AnsiEscapeRe.ReplaceAllString(text, "")); ts != "" {
		return text
	}
	ts := time.Now().Format("15:04:05.000")
	return ts + " " + text
}

// LineStamper wraps StampLine with state tracking across successive Write
// calls fed by the SAME chunked reader (a pty ptmx.Read loop, RunCmd's
// idle-timer chunking), so a raw fixed-size read that lands mid-line is
// never mistaken for a fresh line start and stamped in the middle of a
// line — the concrete bug a stateless per-chunk StampLine has: a logical
// line spanning two reads got a timestamp spliced into its middle.
//
// It performs no buffering — every byte handed to Write is returned
// immediately, exactly like StampLine itself, so an interactive prompt
// with no trailing newline (the reason RunCmdPTY exists at all) still
// displays live. It only remembers whether the PREVIOUS chunk it saw ended
// in '\n' (or whether this is the very first chunk) to decide whether
// StampLine is allowed to fire on the current one.
//
// Deliberately not shared: two independent LineStampers are wired as
// DisplayTransform and LogTransform (internal/luahost's newOutputPump), so
// a future divergence between what reaches the terminal and what reaches
// the log file can't cross-contaminate state between the two.
type LineStamper struct {
	atLineStart bool
}

// NewLineStamper returns a LineStamper primed to stamp its very first
// chunk — the true start of the stream.
func NewLineStamper() *LineStamper {
	return &LineStamper{atLineStart: true}
}

// Write returns text unchanged, or with a timestamp prefixed via StampLine
// when text opens at a genuine line start (right after a previous chunk's
// trailing '\n', or as the first chunk ever seen). Bound to
// internal/streams.OutputPump's DisplayTransform/LogTransform shape
// (func(string) string) via a method value, e.g. logformat.NewLineStamper().Write.
func (s *LineStamper) Write(text string) string {
	out := text
	if s.atLineStart {
		out = StampLine(text)
	}
	if text != "" {
		s.atLineStart = strings.HasSuffix(text, "\n")
	}
	return out
}

var isoTimestampRe = regexp.MustCompile(
	`^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2}):(\d{2})(?:[.,](\d+))?(?:(Z)|([+-])(\d{2}):?(\d{2}))?$`)
var bareTimeRe = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2})`)

// NormalizeTimestamp renders a timestamp already present in a log line as
// local `layout` (a Go time layout, e.g. "15:04:05"). Accepts what
// StripLogTimestamp yields: an ISO-8601 stamp (Z, a numeric offset, or
// naive) or a bare HH:MM:SS[.f] wall clock. A stamp carrying a timezone is
// converted to local time. Returns ("", false) when nothing parses — the
// caller falls back to arrival time.
func NormalizeTimestamp(ts, layout string) (string, bool) {
	s := strings.TrimSpace(ts)
	if s == "" {
		return "", false
	}
	if m := bareTimeRe.FindStringSubmatch(s); m != nil {
		h, mi, sec := atoi(m[1]), atoi(m[2]), atoi(m[3])
		if h > 23 || mi > 59 || sec > 59 {
			return "", false
		}
		now := time.Now()
		t := time.Date(now.Year(), now.Month(), now.Day(), h, mi, sec, 0, time.Local)
		return t.Format(layout), true
	}
	m := isoTimestampRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	year, month, day := atoi(m[1]), atoi(m[2]), atoi(m[3])
	hour, min, sec := atoi(m[4]), atoi(m[5]), atoi(m[6])
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || min > 59 || sec > 59 {
		return "", false
	}
	nsec := 0
	if m[7] != "" {
		frac := (m[7] + "000000000")[:9]
		nsec = atoi(frac)
	}
	loc := time.Local
	switch {
	case m[8] == "Z":
		loc = time.UTC
	case m[9] != "":
		offH, offM := atoi(m[10]), atoi(m[11])
		offSec := offH*3600 + offM*60
		if m[9] == "-" {
			offSec = -offSec
		}
		loc = time.FixedZone("", offSec)
	}
	t := time.Date(year, time.Month(month), day, hour, min, sec, nsec, loc)
	return t.Local().Format(layout), true
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

var levelColors = []struct {
	re    *regexp.Regexp
	color string
}{
	{regexp.MustCompile(`(?i)\b(ERROR|ERR|FATAL|CRIT)\b`), ansiRed},
	{regexp.MustCompile(`(?i)\b(WARN|WARNING)\b`), ansiYellow},
	{regexp.MustCompile(`(?i)\b(INFO)\b`), ansiCyan},
	{regexp.MustCompile(`(?i)\b(DEBUG|TRACE)\b`), ansiDim},
}

var levelColorMap = map[string]string{
	"ERROR": ansiRed, "ERR": ansiRed, "FATAL": ansiRed, "CRIT": ansiRed, "CRITICAL": ansiRed,
	"WARN": ansiYellow, "WARNING": ansiYellow,
	"INFO":  ansiCyan,
	"DEBUG": ansiDim, "TRACE": ansiDim,
}

var levelWords = func() map[string]bool {
	out := map[string]bool{"LOG": true, "NOTICE": true, "VERBOSE": true}
	for k := range levelColorMap {
		out[k] = true
	}
	return out
}()

// scopePalette is 56 xterm-256 indices, mid-brightness so they read on a
// light and a dark terminal, deliberately excluding near-cyan, near-red and
// near-yellow (which would be mistaken for an INFO/WARN/ERROR level word).
var scopePalette = [...]int{
	32, 33, 38, 39, 41, 42, 43, 47, 48, 49,
	61, 62, 63, 66, 67, 68, 69, 71, 72, 73, 74, 77, 78, 79,
	104, 105, 106, 107, 108, 109, 110, 111, 113, 114, 115,
	133, 134, 135, 139, 140, 141, 143, 144, 145, 146, 147,
	169, 170, 171, 175, 176, 177, 178, 179, 180, 181,
}

// A bracketed token, letter-led and space-free. Deliberately narrow: it
// must not match a JSON array printed into a message (["https://…"]), an
// empty `userIds=[]`, or the `[31m` inside an escape sequence already in
// the line.
var scopeRe = regexp.MustCompile(`\[([A-Za-z][\w.:@/+-]*)\]`)

// paddedLevelRe matches structlog's ConsoleRenderer level tag: a lowercase
// word right-padded with spaces before the closing bracket, e.g.
// "[info     ]". It is anchored at the START of the string on purpose — that
// anchor is what keeps it from ever matching a JSON array printed into a
// message, an empty `userIds=[]`, or a `[31m` escape mid-line, so scopeRe
// itself never needs to tolerate internal or trailing whitespace to handle
// this case.
var paddedLevelRe = regexp.MustCompile(`^\[(\w+)(\s+)\]`)

// messagePadRe collapses the run of 2+ spaces ConsoleRenderer inserts
// between a message and the tag that follows it (its fixed-column padding)
// down to one space. The bracket class is deliberately identical to
// scopeRe's — letter-led, space-free — so this can never collapse the space
// before a JSON array or an empty `[]` sitting in the message text.
var messagePadRe = regexp.MustCompile(`  +(\[[A-Za-z][\w.:@/+-]*\])`)

// UnpadConsoleRenderer recognizes structlog's ConsoleRenderer shape — a
// timestamp (already stripped by the caller) followed by a right-padded
// level tag and a fixed-column-padded message — and re-renders it in
// admin's own vocabulary: an unpadded "[level]" plus single-spaced tags.
//
// This is the same family as logbridge.UnwrapInnerPrefix (see CLAUDE.md §
// Log line rendering), extended rather than reusing that helper's pattern:
// UnwrapInnerPrefix handles an app that prints "HH:MM:SS[.mmm] [LEVEL]" into
// its own message, while ConsoleRenderer prints an ISO-8601 stamp (already
// stripped upstream) followed by a padded lowercase level — a different
// shape needing its own anchored matcher.
//
// Returns (rest, false) unchanged when rest does not start with a padded
// bracketed token, or when that token is not a recognized level word (e.g.
// "[widget   ]" is left alone rather than promoted to a level).
func UnpadConsoleRenderer(rest string) (string, bool) {
	m := paddedLevelRe.FindStringSubmatchIndex(rest)
	if m == nil {
		return rest, false
	}
	label := rest[m[2]:m[3]]
	if !levelWords[strings.ToUpper(label)] {
		return rest, false
	}
	tail := rest[m[1]:]
	tail = messagePadRe.ReplaceAllString(tail, " $1")
	return "[" + label + "]" + tail, true
}

// ScopeColor returns the stable escape sequence for a scope label
// (case-insensitive). CRC32 — not Go's map iteration or any per-process
// salted hash — makes the color a pure function of the label: the same tag
// is the same color in every project, every run, and in a log file replayed
// a year later, with nothing cached anywhere. Two dimensions: a hue from
// the palette, plus bold or not from the next bits of the same hash.
func ScopeColor(label string) string {
	h := crc32.ChecksumIEEE([]byte(strings.ToLower(label)))
	idx := scopePalette[int(h)%len(scopePalette)]
	bold := (h / uint32(len(scopePalette))) % 2
	if bold == 1 {
		return fmt.Sprintf("\x1b[1;38;5;%dm", idx)
	}
	return fmt.Sprintf("\x1b[38;5;%dm", idx)
}

// ColorizeScopes colors every bracketed scope tag by hash, leaving the
// brackets plain. Level words are skipped — severity already has
// meaning-carrying colors, and giving ERROR an arbitrary hue would bury it.
func ColorizeScopes(line string) string {
	return scopeRe.ReplaceAllStringFunc(line, func(m string) string {
		label := scopeRe.FindStringSubmatch(m)[1]
		if levelWords[strings.ToUpper(label)] {
			return m
		}
		return "[" + ScopeColor(label) + label + ansiReset + "]"
	})
}

var (
	jsonMessageKeys = []string{"message", "msg", "event"}
	jsonLevelKeys   = []string{"level", "levelname", "severity"}
	jsonLoggerKeys  = []string{"logger", "name", "logger_name"}
)

func firstPresent(obj map[string]any, keys []string) (any, bool) {
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			return v, true
		}
	}
	return nil, false
}

// ParseJSONLogLine reports whether line is a single-object JSON log record
// with a message-like field, returning its (level, logger, message) —
// level/logger are "" when the record carries neither. Returns ok=false for
// anything else, including a JSON array or malformed JSON.
func ParseJSONLogLine(line string) (level, logger, message string, ok bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return "", "", "", false
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return "", "", "", false
	}
	msg, hasMsg := firstPresent(obj, jsonMessageKeys)
	if !hasMsg {
		return "", "", "", false
	}
	if lvl, ok := firstPresent(obj, jsonLevelKeys); ok {
		level = strings.ToUpper(fmt.Sprint(lvl))
	}
	if lg, ok := firstPresent(obj, jsonLoggerKeys); ok {
		logger = fmt.Sprint(lg)
	}
	return level, logger, fmt.Sprint(msg), true
}

// Options configures a LogFormatter. UseColor and ScopeColors are resolved
// by the caller (DefaultOptions does it from the real terminal/environment)
// rather than auto-detected inside FormatLine, so the formatter itself
// stays a pure function of its options and input.
type Options struct {
	// TimestampFormat is a Go time layout, e.g. "15:04:05". Defaults to
	// that when left "".
	TimestampFormat string
	// ReuseTimestamp: when the line already carries its own timestamp,
	// print that (converted to local time) instead of the moment the line
	// arrived. Off by default — a subprocess's stamp is usually a
	// duplicate of now(). On for sources where arrival time is a lie: a
	// browser's log-bridge entries batched up to a second late, and a log
	// file replayed by `admin logs`.
	ReuseTimestamp bool
	UseColor       bool
	ScopeColors    bool
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// DefaultOptions resolves UseColor/ScopeColors from the real process:
// os.Stdout being a terminal, and ADMIN_NO_SCOPE_COLORS being unset.
func DefaultOptions() Options {
	return Options{
		TimestampFormat: "15:04:05",
		UseColor:        isTerminal(os.Stdout),
		ScopeColors:     os.Getenv("ADMIN_NO_SCOPE_COLORS") == "",
	}
}

// LogFormatter stamps every line with one timestamp format and colorizes
// its level. The Go port of admin_lib/log_format.py's LogFormatter class.
type LogFormatter struct {
	opts Options
}

// NewLogFormatter returns a LogFormatter for opts, defaulting an empty
// TimestampFormat to "15:04:05".
func NewLogFormatter(opts Options) *LogFormatter {
	if opts.TimestampFormat == "" {
		opts.TimestampFormat = "15:04:05"
	}
	return &LogFormatter{opts: opts}
}

// FormatLine renders one raw line (with or without its trailing newline).
// A blank line passes through unchanged, exactly as it arrived.
func (f *LogFormatter) FormatLine(raw string) string {
	line := strings.TrimRight(raw, "\n")
	if strings.TrimSpace(line) == "" {
		return raw
	}

	var ts string
	tsSet := false
	if level, logger, message, ok := ParseJSONLogLine(line); ok {
		line = f.renderJSON(level, logger, message)
	} else {
		stamp, rest := StripLogTimestamp(line)
		if unpadded, ok := UnpadConsoleRenderer(rest); ok {
			rest = unpadded
		}
		line = rest
		if f.opts.ReuseTimestamp && stamp != "" {
			if norm, ok := NormalizeTimestamp(stamp, f.opts.TimestampFormat); ok {
				ts, tsSet = norm, true
			}
		}
		if f.opts.UseColor {
			line = f.colorizeLevel(line)
			if f.opts.ScopeColors {
				line = ColorizeScopes(line)
			}
		}
	}
	if !tsSet {
		ts = time.Now().Format(f.opts.TimestampFormat)
	}
	return ts + "  " + line + "\n"
}

func (f *LogFormatter) renderJSON(level, logger, message string) string {
	var parts []string
	if level != "" {
		levelStr := fmt.Sprintf("%-5s", level)
		if f.opts.UseColor {
			if color, ok := levelColorMap[level]; ok {
				levelStr = color + levelStr + ansiReset
			}
		}
		parts = append(parts, levelStr)
	}
	if logger != "" {
		if f.opts.UseColor {
			parts = append(parts, ansiDim+logger+ansiReset)
		} else {
			parts = append(parts, logger)
		}
	}
	parts = append(parts, message)
	return strings.Join(parts, "  ")
}

func (f *LogFormatter) colorizeLevel(line string) string {
	for _, lc := range levelColors {
		if loc := lc.re.FindStringIndex(line); loc != nil {
			return line[:loc[0]] + lc.color + line[loc[0]:loc[1]] + ansiReset + line[loc[1]:]
		}
	}
	return line
}

// MakeFormatter returns a func(string) string bound to a LogFormatter built
// from opts — the shape internal/streams.OutputPump's Formatter field
// expects.
func MakeFormatter(opts Options) func(string) string {
	return NewLogFormatter(opts).FormatLine
}
