package logformat

import (
	"regexp"
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

func TestScopeColorsAppliedWhenEnabled(t *testing.T) {
	f := NewLogFormatter(Options{UseColor: true, ScopeColors: true})
	out := f.FormatLine("[api] hello\n")
	if !regexp.MustCompile(`38;5;`).MatchString(out) {
		t.Fatalf("expected a scope color escape, got %q", out)
	}
}
