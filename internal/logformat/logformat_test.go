package logformat

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func plainFormatter() *LogFormatter {
	return NewLogFormatter(Options{UseColor: false, ScopeColors: false})
}

// -- ParseJSONLogLine --------------------------------------------------------

func TestParseJSONUvicornAccess(t *testing.T) {
	line := `{"timestamp": "2026-05-22T17:57:25.236Z", "level": "INFO", ` +
		`"logger": "uvicorn.access", "message": "127.0.0.1 - GET / 200"}`
	level, logger, msg, ok := ParseJSONLogLine(line)
	if !ok || level != "INFO" || logger != "uvicorn.access" || msg != "127.0.0.1 - GET / 200" {
		t.Fatalf("got (%q,%q,%q,%v)", level, logger, msg, ok)
	}
}

func TestParseJSONAltKeys(t *testing.T) {
	level, logger, msg, ok := ParseJSONLogLine(`{"levelname": "warning", "name": "app", "msg": "hi"}`)
	if !ok || level != "WARNING" || logger != "app" || msg != "hi" {
		t.Fatalf("got (%q,%q,%q,%v)", level, logger, msg, ok)
	}
}

func TestParseJSONEventKey(t *testing.T) {
	level, logger, msg, ok := ParseJSONLogLine(`{"event": "started"}`)
	if !ok || level != "" || logger != "" || msg != "started" {
		t.Fatalf("got (%q,%q,%q,%v)", level, logger, msg, ok)
	}
}

func TestParseJSONMissingMessageReturnsFalse(t *testing.T) {
	if _, _, _, ok := ParseJSONLogLine(`{"level": "INFO"}`); ok {
		t.Fatal("expected ok=false")
	}
}

func TestParseJSONPlainLineReturnsFalse(t *testing.T) {
	if _, _, _, ok := ParseJSONLogLine("INFO:     Uvicorn running on http://127.0.0.1:8000"); ok {
		t.Fatal("expected ok=false")
	}
}

func TestParseJSONArrayReturnsFalse(t *testing.T) {
	if _, _, _, ok := ParseJSONLogLine(`[{"message": "hi"}]`); ok {
		t.Fatal("expected ok=false for a JSON array")
	}
}

func TestParseJSONMalformedReturnsFalse(t *testing.T) {
	if _, _, _, ok := ParseJSONLogLine(`{"message": "unterminated`); ok {
		t.Fatal("expected ok=false")
	}
}

func TestParseJSONExtraFieldsIgnored(t *testing.T) {
	line := `{"level": "INFO", "message": "ok", "extra": {"x": 1}, "timestamp": "z"}`
	level, logger, msg, ok := ParseJSONLogLine(line)
	if !ok || level != "INFO" || logger != "" || msg != "ok" {
		t.Fatalf("got (%q,%q,%q,%v)", level, logger, msg, ok)
	}
}

// -- FormatLine: JSON path ----------------------------------------------------

func TestFormatJSONLineRendersLevelLoggerMessage(t *testing.T) {
	f := plainFormatter()
	out := stripANSI(f.FormatLine(`{"level": "INFO", "logger": "uvicorn.access", "message": "GET / 200"}` + "\n"))
	want := regexp.MustCompile(`^\d{2}:\d{2}:\d{2}  INFO   uvicorn\.access  GET / 200\n$`)
	if !want.MatchString(out) {
		t.Fatalf("got %q", out)
	}
}

func TestFormatJSONLineOmitsMissingLevel(t *testing.T) {
	f := plainFormatter()
	out := stripANSI(f.FormatLine(`{"logger": "app", "message": "hello"}` + "\n"))
	want := regexp.MustCompile(`^\d{2}:\d{2}:\d{2}  app  hello\n$`)
	if !want.MatchString(out) {
		t.Fatalf("got %q", out)
	}
}

func TestFormatJSONLineOmitsMissingLogger(t *testing.T) {
	f := plainFormatter()
	out := stripANSI(f.FormatLine(`{"level": "WARNING", "message": "hmm"}` + "\n"))
	want := regexp.MustCompile(`^\d{2}:\d{2}:\d{2}  WARNING  hmm\n$`)
	if !want.MatchString(out) {
		t.Fatalf("got %q", out)
	}
}

// -- FormatLine: plain-line path ----------------------------------------------

func TestFormatPlainLineStripsLeadingTimestamp(t *testing.T) {
	f := plainFormatter()
	out := stripANSI(f.FormatLine("2026-05-22 12:34:56.789 INFO  starting up\n"))
	want := regexp.MustCompile(`^\d{2}:\d{2}:\d{2}  INFO  starting up\n$`)
	if !want.MatchString(out) {
		t.Fatalf("got %q", out)
	}
}

func TestFormatPlainLineNoTimestampPassesThrough(t *testing.T) {
	f := plainFormatter()
	out := stripANSI(f.FormatLine("INFO:     Uvicorn running on http://127.0.0.1:8000\n"))
	want := regexp.MustCompile(`^\d{2}:\d{2}:\d{2}  INFO:     Uvicorn running on http://127\.0\.0\.1:8000\n$`)
	if !want.MatchString(out) {
		t.Fatalf("got %q", out)
	}
}

func TestFormatBlankLineUnchanged(t *testing.T) {
	f := plainFormatter()
	if got := f.FormatLine("\n"); got != "\n" {
		t.Fatalf("got %q", got)
	}
}

// A source that carries its own timestamp must be re-stamped once, never
// twice — the concrete regression this ticket's acceptance criteria names.
func TestFormatLineIsNotDoubleStamped(t *testing.T) {
	f := plainFormatter()
	out := f.FormatLine("2026-05-22T17:57:25.236Z starting up\n")
	if n := len(regexp.MustCompile(`\d{2}:\d{2}:\d{2}`).FindAllString(out, -1)); n != 1 {
		t.Fatalf("expected exactly one timestamp-shaped run, got %d in %q", n, out)
	}
}

// -- ColorizeLevel -----------------------------------------------------------

func TestColorizeLevelColorsFirstMatchInPriorityOrder(t *testing.T) {
	// ERROR outranks INFO in levelColors, so even though INFO appears first
	// in the line, ERROR is the one that gets colored — this is the exact
	// behavior internal/logcolumns.MakeColumnFormatter relies on to color a
	// rendered column line without keeping its own copy of the level table.
	out := ColorizeLevel("INFO saw an ERROR while starting up")
	want := "INFO saw an " + ansiRed + "ERROR" + ansiReset + " while starting up"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestColorizeLevelNoLevelWordUnchanged(t *testing.T) {
	in := "nothing to see here"
	if got := ColorizeLevel(in); got != in {
		t.Fatalf("got %q, want unchanged %q", got, in)
	}
}

// -- StampLine -------------------------------------------------------------

func TestStampLineBlankTextUnchanged(t *testing.T) {
	for _, in := range []string{"", "   ", "\t", "\n", "  \t\n"} {
		if got := StampLine(in); got != in {
			t.Errorf("StampLine(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestStampLineAlreadyStampedPassesThroughUnchanged(t *testing.T) {
	in := "17:36:32.366 [DEBUG] starting up\n"
	if got := StampLine(in); got != in {
		t.Fatalf("got %q, want unchanged %q", got, in)
	}
}

func TestStampLineAlreadyStampedAfterANSIPassesThroughUnchanged(t *testing.T) {
	in := "\x1b[36m17:36:32.366 [INFO] hi\x1b[0m\n"
	if got := StampLine(in); got != in {
		t.Fatalf("got %q, want unchanged %q", got, in)
	}
}

func TestStampLineNormalCasePrependsWellFormedStamp(t *testing.T) {
	in := "starting up\n"
	out := StampLine(in)
	want := regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\.\d{3} starting up\n$`)
	if !want.MatchString(out) {
		t.Fatalf("got %q", out)
	}
}

// -- LineStamper -------------------------------------------------------------

func TestLineStamperStampsFirstChunkOnly(t *testing.T) {
	s := NewLineStamper()
	first := s.Write("starting up\n")
	if !regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\.\d{3} starting up\n$`).MatchString(first) {
		t.Fatalf("got %q", first)
	}
	second := s.Write("second line\n")
	if !regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\.\d{3} second line\n$`).MatchString(second) {
		t.Fatalf("got %q", second)
	}
}

// The concrete regression finding 1 describes: a logical line split across
// two Write-sized chunks (a pty ptmx.Read boundary landing mid-line) must
// get exactly one stamp, at the true start, and the continuation chunk must
// pass through untouched rather than getting a splice mid-line.
func TestLineStamperDoesNotStampMidLineContinuation(t *testing.T) {
	s := NewLineStamper()
	first := s.Write("hello wor")
	if !regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\.\d{3} hello wor$`).MatchString(first) {
		t.Fatalf("first chunk: got %q", first)
	}
	second := s.Write("ld\n")
	if second != "ld\n" {
		t.Fatalf("continuation chunk got stamped: %q", second)
	}
	third := s.Write("next line\n")
	if !regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\.\d{3} next line\n$`).MatchString(third) {
		t.Fatalf("third chunk (fresh line start): got %q", third)
	}
}

func TestLineStamperEmptyChunkDoesNotResetLineStartState(t *testing.T) {
	s := NewLineStamper()
	_ = s.Write("mid-line, no newline")
	if got := s.Write(""); got != "" {
		t.Fatalf("empty chunk: got %q", got)
	}
	// Still mid-line: the empty Write above must not have flipped state back
	// to "at line start".
	cont := s.Write(" continues\n")
	if cont != " continues\n" {
		t.Fatalf("continuation after empty chunk got stamped: %q", cont)
	}
}

// -- StripLogTimestamp ---------------------------------------------------------

func TestStripLogTimestampISOWithMillis(t *testing.T) {
	ts, rest := StripLogTimestamp("2026-05-22T17:57:25.236Z INFO foo")
	if ts != "2026-05-22T17:57:25.236Z" || rest != "INFO foo" {
		t.Fatalf("got (%q,%q)", ts, rest)
	}
}

func TestStripLogTimestampNoMatch(t *testing.T) {
	ts, rest := StripLogTimestamp("no timestamp here")
	if ts != "" || rest != "no timestamp here" {
		t.Fatalf("got (%q,%q)", ts, rest)
	}
}

// -- NormalizeTimestamp ---------------------------------------------------------

func TestNormalizeTimestampUTCConvertsToLocal(t *testing.T) {
	want := time.Date(2026, 8, 27, 17, 19, 1, 0, time.UTC).Local().Format("15:04:05")
	got, ok := NormalizeTimestamp("2026-08-27T17:19:01.723Z", "15:04:05")
	if !ok || got != want {
		t.Fatalf("got (%q,%v), want %q", got, ok, want)
	}
}

func TestNormalizeTimestampBareTimeKept(t *testing.T) {
	got, ok := NormalizeTimestamp("13:18:56.412", "15:04:05")
	if !ok || got != "13:18:56" {
		t.Fatalf("got (%q,%v)", got, ok)
	}
}

func TestNormalizeTimestampUnparseableReturnsFalse(t *testing.T) {
	if _, ok := NormalizeTimestamp("not-a-time-at-all", "15:04:05"); ok {
		t.Fatal("expected ok=false")
	}
}

func TestNormalizeTimestampWithNumericOffset(t *testing.T) {
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("", -5*3600)).Local().Format("15:04:05")
	got, ok := NormalizeTimestamp("2026-01-02T03:04:05-0500", "15:04:05")
	if !ok || got != want {
		t.Fatalf("got (%q,%v), want %q", got, ok, want)
	}
}

func TestReuseTimestampPrintsTheLinesOwnTime(t *testing.T) {
	f := NewLogFormatter(Options{ReuseTimestamp: true})
	out := f.FormatLine("2026-08-27T17:19:01.723Z [INFO] [client] hi\n")
	want, _ := NormalizeTimestamp("2026-08-27T17:19:01.723Z", "15:04:05")
	if !regexp.MustCompile(`^` + regexp.QuoteMeta(want)).MatchString(out) {
		t.Fatalf("got %q, want prefix %q", out, want)
	}
	if !regexp.MustCompile(`\[INFO\] \[client\] hi\n$`).MatchString(stripANSI(out)) {
		t.Fatalf("got %q", out)
	}
}

func TestReuseTimestampFallsBackToNowWithoutOne(t *testing.T) {
	f := NewLogFormatter(Options{ReuseTimestamp: true})
	out := f.FormatLine("[INFO] [client] hi\n")
	now := time.Now().Format("15:04")
	if len(out) < len(now) || out[:len(now)] != now {
		t.Fatalf("got %q, want prefix %q", out, now)
	}
}

// -- hashed scope colors ---------------------------------------------------

func TestScopeColorIsStableAndCaseInsensitive(t *testing.T) {
	if ScopeColor("supabase") != ScopeColor("SUPABASE") {
		t.Fatal("expected case-insensitive match")
	}
	if ScopeColor("supabase") != ScopeColor("supabase") {
		t.Fatal("expected a pure function")
	}
	if ScopeColor("supabase") == ScopeColor("api") {
		t.Fatal("expected different labels to (almost always) differ — this pair is a known-good non-collision")
	}
}

// The literal repo-history reason CRC32 was picked over the builtin hash:
// stability across process runs. Simulated here by calling twice — a
// per-process-salted hash would differ across runs, not within one, so the
// real regression this guards is "don't swap crc32.ChecksumIEEE for
// Go's fnv/maphash without reading this comment first."
func TestScopeColorIsDeterministicNotProcessSalted(t *testing.T) {
	a := ScopeColor("worker-7")
	b := ScopeColor("worker-7")
	if a != b {
		t.Fatalf("scope color changed between calls: %q vs %q", a, b)
	}
}

func TestColorizeScopesColorsTagsButNotLevels(t *testing.T) {
	out := ColorizeScopes("[ERROR] [api] boom")
	want := "[ERROR] [" + ScopeColor("api") + "api\x1b[0m] boom"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestColorizeScopesIgnoresJSONArraysAndEscapeSequences(t *testing.T) {
	lines := []string{
		`scripts ["https://a.js","https://b.js"]`,
		"count=0 userIds=[]",
		"[\x1b[31mERROR\x1b[0m] boom",
	}
	for _, line := range lines {
		if got := ColorizeScopes(line); got != line {
			t.Errorf("ColorizeScopes(%q) = %q, want unchanged", line, got)
		}
	}
}

func TestScopeColorsSuppressedByOption(t *testing.T) {
	f := NewLogFormatter(Options{UseColor: true, ScopeColors: false})
	out := f.FormatLine("[api] hello\n")
	if regexp.MustCompile(`38;5;`).MatchString(out) {
		t.Fatalf("expected no scope color escapes, got %q", out)
	}
}

// -- AnsiEscapeRe: OSC/DCS, not just CSI --------------------------------------

// TestAnsiEscapeReStripsOSCQueryResponse is admin-work-tab-orientation's
// defect 2: a hosted pty's own terminal-query RESPONSE (an OSC sequence, e.g.
// a background-color query answered as ESC ] 11 ; ? BEL) has a plain,
// printable body with no further escape bytes in it, so a stripper that only
// consumed the opening "ESC ]" left that body — "11;?" — as literal text
// glued onto whatever real output followed it. Both terminator forms (BEL and
// the ESC \ "ST" form) must be consumed along with the body.
func TestAnsiEscapeReStripsOSCQueryResponse(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"BEL-terminated", "\x1b]11;?\a→ wrote: /Users/x", "→ wrote: /Users/x"},
		{"ST-terminated", "before\x1b]0;title\x1b\\after", "beforeafter"},
	}
	for _, c := range cases {
		if got := AnsiEscapeRe.ReplaceAllString(c.in, ""); got != c.want {
			t.Errorf("%s: AnsiEscapeRe.ReplaceAllString(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestAnsiEscapeReStripsDCS proves DCS/SOS/PM/APC (ESC P/X/^/_, the same
// "runs to its own terminator" shape as OSC) get the same treatment.
func TestAnsiEscapeReStripsDCS(t *testing.T) {
	in := "dcs\x1bP1$q\x1b\\end"
	if got := AnsiEscapeRe.ReplaceAllString(in, ""); got != "dcsend" {
		t.Errorf("AnsiEscapeRe.ReplaceAllString(%q) = %q, want %q", in, got, "dcsend")
	}
}

// TestAnsiEscapeReStillStripsCSI is the regression guard for every existing
// caller (StampLine, luahost.LogContext.Write/ParseLogAnnounce): plain SGR
// color codes must strip exactly as before.
func TestAnsiEscapeReStillStripsCSI(t *testing.T) {
	in := "\x1b[31mred\x1b[0m"
	if got := AnsiEscapeRe.ReplaceAllString(in, ""); got != "red" {
		t.Errorf("AnsiEscapeRe.ReplaceAllString(%q) = %q, want %q", in, got, "red")
	}
}

func TestScopeColorsAppliedWhenEnabled(t *testing.T) {
	f := NewLogFormatter(Options{UseColor: true, ScopeColors: true})
	out := f.FormatLine("[api] hello\n")
	if !regexp.MustCompile(`38;5;`).MatchString(out) {
		t.Fatalf("expected a scope color escape, got %q", out)
	}
}

// -- structlog ConsoleRenderer -------------------------------------------------

const structlogLine1 = `2026-09-03T01:42:10.606618Z [info     ] Starting dev persistence flush loop [langgraph_runtime_inmem._persistence] api_variant=local_dev langgraph_api_version=0.13.3 thread_name=MainThread` + "\n"
const structlogLine2 = `2026-09-03T01:42:12.914773Z [info     ] Worker stats                   [langgraph_runtime_inmem.queue] active=0 api_variant=local_dev available=1 max=1` + "\n"

func TestFormatLineConsoleRendererLine1(t *testing.T) {
	f := plainFormatter()
	out := f.FormatLine(structlogLine1)

	if n := len(regexp.MustCompile(`\d{2}:\d{2}:\d{2}`).FindAllString(out, -1)); n != 1 {
		t.Fatalf("expected exactly one timestamp-shaped run, got %d in %q", n, out)
	}

	// The formatter's own "ts + two spaces" gutter is legitimate padding —
	// only check for a run of 2+ spaces in what follows it.
	afterTS := regexp.MustCompile(`^\d{2}:\d{2}:\d{2}  `).ReplaceAllString(out, "")
	if regexp.MustCompile(`  +`).MatchString(afterTS) {
		t.Fatalf("expected no run of 2+ spaces after the timestamp gutter, got %q", out)
	}

	want := "[info] Starting dev persistence flush loop [langgraph_runtime_inmem._persistence] api_variant=local_dev langgraph_api_version=0.13.3 thread_name=MainThread\n"
	if afterTS != want {
		t.Fatalf("got remainder %q, want %q", afterTS, want)
	}
}

func TestFormatLineConsoleRendererLine2(t *testing.T) {
	f := plainFormatter()
	out := f.FormatLine(structlogLine2)

	if n := len(regexp.MustCompile(`\d{2}:\d{2}:\d{2}`).FindAllString(out, -1)); n != 1 {
		t.Fatalf("expected exactly one timestamp-shaped run, got %d in %q", n, out)
	}

	afterTS := regexp.MustCompile(`^\d{2}:\d{2}:\d{2}  `).ReplaceAllString(out, "")
	if regexp.MustCompile(`  +`).MatchString(afterTS) {
		t.Fatalf("expected no run of 2+ spaces after the timestamp gutter, got %q", out)
	}

	want := "[info] Worker stats [langgraph_runtime_inmem.queue] active=0 api_variant=local_dev available=1 max=1\n"
	if afterTS != want {
		t.Fatalf("got remainder %q, want %q", afterTS, want)
	}
}

func TestFormatLineConsoleRendererColorizesLevelNotScope(t *testing.T) {
	f := NewLogFormatter(Options{UseColor: true, ScopeColors: true})
	out := f.FormatLine(structlogLine1)

	wantLevel := "[" + ansiCyan + "info" + ansiReset + "]"
	if !strings.Contains(out, wantLevel) {
		t.Fatalf("expected level colored as %q, got %q", wantLevel, out)
	}

	wantScope := "[" + ScopeColor("langgraph_runtime_inmem._persistence") + "langgraph_runtime_inmem._persistence" + ansiReset + "]"
	if !strings.Contains(out, wantScope) {
		t.Fatalf("expected logger tag scope-colored as %q, got %q", wantScope, out)
	}
}

func TestUnpadConsoleRendererNegativeCases(t *testing.T) {
	cases := []string{
		`scripts ["https://a.js","https://b.js"]`,
		"count=0 userIds=[]",
		"[\x1b[31mERROR\x1b[0m] boom",
		"[INFO] [client] hi", // already unpadded — must not match
		"[widget   ] hi",     // padded but not a recognized level word
	}
	for _, line := range cases {
		if got, ok := UnpadConsoleRenderer(line); ok || got != line {
			t.Errorf("UnpadConsoleRenderer(%q) = (%q, %v), want (%q, false)", line, got, ok, line)
		}
	}
}
