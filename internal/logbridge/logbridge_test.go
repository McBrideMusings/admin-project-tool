package logbridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestUnwrapInnerPrefixStripsClockAndLevel proves an app's own
// "HH:MM:SS.mmm [LEVEL] [Scope]" prefix is split off, the level extracted
// (case-normalized), and every tag after it kept — the CLAUDE.md-documented
// "unwrap_inner_prefix" behavior.
func TestUnwrapInnerPrefixStripsClockAndLevel(t *testing.T) {
	level, rest, ok := UnwrapInnerPrefix("17:36:32.366 [DEBUG] [Messaging] msg")
	if !ok {
		t.Fatal("UnwrapInnerPrefix ok = false, want true")
	}
	if level != "DEBUG" {
		t.Fatalf("level = %q, want DEBUG", level)
	}
	if rest != "[Messaging] msg" {
		t.Fatalf("rest = %q, want '[Messaging] msg'", rest)
	}
}

// TestUnwrapInnerPrefixLeavesPlainMessageAlone proves a message that merely
// starts with a clock (no bracketed level after it) is left untouched —
// only the full HH:MM:SS[.mmm] [LEVEL] shape counts.
func TestUnwrapInnerPrefixLeavesPlainMessageAlone(t *testing.T) {
	msg := "17:36:32 something happened"
	level, rest, ok := UnwrapInnerPrefix(msg)
	if ok {
		t.Fatalf("UnwrapInnerPrefix ok = true (level=%q, rest=%q), want false", level, rest)
	}
	if rest != msg {
		t.Fatalf("rest = %q, want unchanged %q", rest, msg)
	}
}

// TestFormatEntryUsesInnerLevelOverBrowserLevel proves a logger that routes
// debug through console.log (browser level "log") reports as DEBUG once its
// own inner prefix is unwrapped — the whole point of unwrapping.
func TestFormatEntryUsesInnerLevelOverBrowserLevel(t *testing.T) {
	line := FormatEntry(Entry{
		Timestamp: "2026-01-01T00:00:00.000Z",
		Level:     "log",
		Message:   "17:36:32.366 [DEBUG] [Messaging] msg",
	})
	if !strings.Contains(line, "[DEBUG] [CLIENT] [Messaging] msg") {
		t.Fatalf("formatted line = %q, want DEBUG level and [Messaging] kept", line)
	}
}

// TestFormatEntryMapsConsoleLevels proves the console.{log,info,warn,error,
// debug} -> {INFO,INFO,WARN,ERROR,DEBUG} mapping CLAUDE.md documents.
func TestFormatEntryMapsConsoleLevels(t *testing.T) {
	cases := map[string]string{"log": "INFO", "info": "INFO", "warn": "WARN", "error": "ERROR", "debug": "DEBUG"}
	for consoleLevel, want := range cases {
		line := FormatEntry(Entry{Timestamp: "2026-01-01T00:00:00Z", Level: consoleLevel, Message: "hi"})
		if !strings.Contains(line, "["+want+"]") {
			t.Errorf("level %q: line = %q, want [%s]", consoleLevel, line, want)
		}
	}
}

// TestFormatEntryMultilineMessageStampsEveryLine proves a stack trace (a
// message with embedded newlines) becomes one stamped physical line per
// line, with the level/[CLIENT] tag on the head line only.
func TestFormatEntryMultilineMessageStampsEveryLine(t *testing.T) {
	line := FormatEntry(Entry{Timestamp: "2026-01-01T00:00:00Z", Level: "error", Message: "boom\n  at foo\n  at bar"})
	lines := strings.Split(strings.TrimRight(line, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %q", len(lines), line)
	}
	if !strings.Contains(lines[0], "[ERROR] [CLIENT] boom") {
		t.Fatalf("head line = %q", lines[0])
	}
	if !strings.HasSuffix(lines[1], "  at foo") || strings.Contains(lines[1], "[CLIENT]") {
		t.Fatalf("continuation line = %q, want no [CLIENT] tag", lines[1])
	}
}

// TestEntryHostPrefersHostOverURL proves a new-shape entry's `host` field is
// used directly, and an old-shape entry's `url` is reduced to just the
// hostname — keeping query-string tokens (auth) out of the log.
func TestEntryHostPrefersHostOverURL(t *testing.T) {
	if got := entryHost(Entry{Host: "example.com"}); got != "example.com" {
		t.Fatalf("entryHost(host set) = %q", got)
	}
	if got := entryHost(Entry{URL: "https://example.com:8080/path?token=secret"}); got != "example.com" {
		t.Fatalf("entryHost(url only) = %q, want host only (no token)", got)
	}
}

// TestInTimeOrderSortsByOwnTimestamp proves a batch is reordered by each
// entry's own timestamp, not POST arrival order — the backlog-from-a-slow-
// frame case CLAUDE.md's "Ordering" section describes.
func TestInTimeOrderSortsByOwnTimestamp(t *testing.T) {
	entries := []Entry{
		{Timestamp: "2026-01-01T00:00:03Z", Message: "third"},
		{Timestamp: "2026-01-01T00:00:01Z", Message: "first"},
		{Timestamp: "2026-01-01T00:00:02Z", Message: "second"},
	}
	got := inTimeOrder(entries)
	want := []string{"first", "second", "third"}
	for i, w := range want {
		if got[i].Message != w {
			t.Fatalf("position %d = %q, want %q (full: %+v)", i, got[i].Message, w, got)
		}
	}
}

// TestInTimeOrderCarriesLastGoodKeyForward proves an entry with an
// unparseable timestamp stays exactly where the client put it (keyed by the
// previous valid timestamp), rather than sorting to the front.
func TestInTimeOrderCarriesLastGoodKeyForward(t *testing.T) {
	entries := []Entry{
		{Timestamp: "2026-01-01T00:00:01Z", Message: "first"},
		{Timestamp: "not-a-timestamp", Message: "bad-stays-after-first"},
		{Timestamp: "2026-01-01T00:00:02Z", Message: "third"},
	}
	got := inTimeOrder(entries)
	if got[0].Message != "first" || got[1].Message != "bad-stays-after-first" || got[2].Message != "third" {
		t.Fatalf("order = %v", []string{got[0].Message, got[1].Message, got[2].Message})
	}
}

// TestBridgeAppendWritesLogFileAndSink is an end-to-end check of the append
// path: two entries out of order are appended sorted, the connect banner
// appears once, and the sink receives the exact same lines the log file
// got.
func TestBridgeAppendWritesLogFileAndSink(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	b := &Bridge{logPath: logPath, seenClients: map[string]bool{}}

	var sunk []string
	b.SetSink(func(line string) { sunk = append(sunk, line) })

	n := b.Append([]Entry{
		{Timestamp: "2026-01-01T00:00:02Z", Host: "app.local", Level: "info", Message: "second"},
		{Timestamp: "2026-01-01T00:00:01Z", Host: "app.local", Level: "info", Message: "first"},
	}, "TestAgent/1.0")
	if n != 2 {
		t.Fatalf("Append returned %d, want 2", n)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	fileText := string(data)
	if !strings.Contains(fileText, "client connected: app.local  (TestAgent/1.0)") {
		t.Fatalf("log file missing connect banner: %q", fileText)
	}
	firstIdx := strings.Index(fileText, "first")
	secondIdx := strings.Index(fileText, "second")
	if firstIdx < 0 || secondIdx < 0 || firstIdx > secondIdx {
		t.Fatalf("entries not in timestamp order in file: %q", fileText)
	}

	sunkText := strings.Join(sunk, "")
	if sunkText != fileText {
		t.Fatalf("sink text != file text:\nsink: %q\nfile: %q", sunkText, fileText)
	}

	// A second batch from the same host must not repeat the connect banner.
	b.Append([]Entry{{Timestamp: "2026-01-01T00:00:03Z", Host: "app.local", Message: "third"}}, "TestAgent/1.0")
	data2, _ := os.ReadFile(logPath)
	if strings.Count(string(data2), "client connected") != 1 {
		t.Fatalf("connect banner appeared more than once: %q", string(data2))
	}
}

// TestStartServesHealthAndLog is a real end-to-end test through the actual
// HTTP listener: Start binds a free port, GET /health reports it, and a
// POST /log batch lands in the log file.
func TestStartServesHealthAndLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	DiscoveryDir = filepath.Join(dir, "discovery")

	b, err := Start(logPath, Config{Hosts: []string{"example.com"}, PortRange: [2]int{19988, 19999}})
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if b == nil {
		t.Fatal("Start returned nil bridge for an enabled config")
	}
	defer b.Stop()

	base := "http://127.0.0.1:" + itoa(b.Port())

	// Give the server goroutine a moment to actually be listening (Start's
	// Serve runs in a goroutine).
	deadlineOK := false
	for i := 0; i < 50; i++ {
		resp, err := http.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			deadlineOK = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !deadlineOK {
		t.Fatal("server never came up")
	}

	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	var health map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if int(health["port"].(float64)) != b.Port() {
		t.Fatalf("health port = %v, want %d", health["port"], b.Port())
	}

	body, _ := json.Marshal([]Entry{{Timestamp: "2026-01-01T00:00:00Z", Host: "example.com", Level: "info", Message: "hello"}})
	postResp, err := http.Post(base+"/log", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /log: %v", err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /log status = %d", postResp.StatusCode)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Fatalf("log file = %q, want it to contain the posted message", string(data))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
