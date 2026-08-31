package luahost

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func newTestHost(t *testing.T) (*Host, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	h := New(dir)
	var out, errOut bytes.Buffer
	h.Stdout = &out
	h.Stderr = &errOut
	h.Stdin = strings.NewReader("") // not a *os.File -> pick_target treats as non-TTY
	return h, &out, &errOut
}

func TestOutputFunctions(t *testing.T) {
	h, out, errOut := newTestHost(t)
	if err := h.Exec(`info("hello")
ok("done")
warn("careful")
err("boom")`, nil); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out.String(), "hello") || !strings.Contains(out.String(), "done") {
		t.Fatalf("stdout missing info/ok output: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "careful") || !strings.Contains(errOut.String(), "boom") {
		t.Fatalf("stderr missing warn/err output: %q", errOut.String())
	}
}

func TestConfigReachesLuaAsTable(t *testing.T) {
	h, out, _ := newTestHost(t)
	cfg := map[string]any{
		"bundle_id": "com.example.app",
		"schemes":   []any{"Debug", "Release"},
		"nested":    map[string]any{"ios_scheme": "MyApp"},
	}
	err := h.Exec(`
info(config.bundle_id)
info(config.schemes[1] .. "," .. config.schemes[2])
info(config.nested.ios_scheme)
`, cfg)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := out.String()
	for _, want := range []string{"com.example.app", "Debug,Release", "MyApp"} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q missing %q", got, want)
		}
	}
}

func TestRunCmd(t *testing.T) {
	h, out, _ := newTestHost(t)
	err := h.Exec(`
local ok, output = run_cmd("echo hi-from-run-cmd")
if not ok then error("run_cmd reported failure") end
info("captured:" .. output)
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	// run_cmd also streams the child's raw line straight to Stdout as it
	// arrives, so a bare Contains("hi-from-run-cmd") check passes even when
	// the returned `output` value itself is empty — assert on the
	// "captured:" prefix info() only prints when it concatenates the actual
	// returned string, so this fails if the accumulation is dropped.
	if !strings.Contains(out.String(), "captured:hi-from-run-cmd") {
		t.Fatalf("run_cmd output not captured: %q", out.String())
	}
}

func TestRunCmdFailureReportsNotOk(t *testing.T) {
	h, _, _ := newTestHost(t)
	err := h.Exec(`
local ok, _ = run_cmd("exit 1")
if ok then error("expected ok=false for a failing command") end
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
}

func TestOnlyTargetWithOneChoice(t *testing.T) {
	h, out, _ := newTestHost(t)
	err := h.Exec(`
local picked = only_target({ {key="ios", description="iPhone"} }, "build")
if picked ~= "ios" then error("expected ios, got " .. tostring(picked)) end
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out.String(), "build: ios is this project's only target") {
		t.Fatalf("missing only_target announcement: %q", out.String())
	}
}

func TestOnlyTargetWithMultipleChoicesReturnsNil(t *testing.T) {
	h, _, _ := newTestHost(t)
	err := h.Exec(`
local picked = only_target({ {key="ios"}, {key="mac"} }, "build")
if picked ~= nil then error("expected nil for multiple choices") end
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
}

func TestPickTargetNonTTYReturnsNilAndErrors(t *testing.T) {
	h, _, errOut := newTestHost(t)
	err := h.Exec(`
local picked = pick_target({ {key="ios", description="iPhone"}, {key="mac", description="Mac"} }, "Select")
if picked ~= nil then error("expected nil without a TTY") end
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(errOut.String(), "not a TTY") {
		t.Fatalf("expected non-TTY diagnostic, got %q", errOut.String())
	}
}

func TestNoOSOrIOLibraryExposed(t *testing.T) {
	h, _, _ := newTestHost(t)
	if err := h.Exec(`if os ~= nil then error("os must not be exposed to a Lua body") end`, nil); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if err := h.Exec(`if io ~= nil then error("io must not be exposed to a Lua body") end`, nil); err != nil {
		t.Fatalf("Exec: %v", err)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	h, out, _ := newTestHost(t)
	err := h.Exec(`
local encoded = json.encode({name="admin", targets={"ios","mac"}})
local decoded = json.decode(encoded)
info(decoded.name)
info(decoded.targets[1])
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out.String(), "admin") || !strings.Contains(out.String(), "ios") {
		t.Fatalf("json round trip failed: %q", out.String())
	}
}

// TestFsReadReturnsFileContents proves fs.read(path) reaches the real file
// on disk.
//
// MUTATION EVIDENCE: change fsRead's `os.ReadFile(path)` call to
// `os.ReadFile(path + ".nope")` (read the wrong path), and this test fails:
// the file doesn't exist under that name, ok is false, L.Push(lua.LNil)
// fires instead, and `contents` reads as "nil" rather than the real text.
func TestFsReadReturnsFileContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello from disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, out, _ := newTestHost(t)
	err := h.Exec(`info(tostring(fs.read("`+path+`")))`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out.String(), "hello from disk") {
		t.Fatalf("expected fs.read to return the file's contents, got %q", out.String())
	}
}

// TestFsReadReturnsNilForMissingFile proves a missing file comes back as nil
// rather than raising a Lua error — a body should be able to check
// `if fs.read(path) then ...` without wrapping every call in pcall.
func TestFsReadReturnsNilForMissingFile(t *testing.T) {
	h, out, _ := newTestHost(t)
	err := h.Exec(`info(tostring(fs.read("/does/not/exist/at/all.txt")))`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out.String(), "nil") {
		t.Fatalf("expected fs.read of a missing file to be nil, got %q", out.String())
	}
}

func TestGlobMatch(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.lua", "b.lua", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h, out, _ := newTestHost(t)
	err := h.Exec(`
local matches = glob.match("*.lua", "`+dir+`")
info(tostring(#matches))
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out.String(), "2") {
		t.Fatalf("expected 2 .lua matches, got %q", out.String())
	}
}

func TestPlistRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Info.plist")
	contents := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key>
	<string>com.example.app</string>
	<key>CFBundleVersion</key>
	<string>1.2.3</string>
	<key>LSRequiresIPhoneOS</key>
	<true/>
</dict>
</plist>`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	h, out, _ := newTestHost(t)
	err := h.Exec(`
local info_dict = plist.read("`+path+`")
info(info_dict.CFBundleIdentifier)
info(info_dict.CFBundleVersion)
info(tostring(info_dict.LSRequiresIPhoneOS))
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := out.String()
	for _, want := range []string{"com.example.app", "1.2.3", "true"} {
		if !strings.Contains(got, want) {
			t.Errorf("plist read output %q missing %q", got, want)
		}
	}
}

func TestSetLogContextMaterializesAndWrites(t *testing.T) {
	dir := t.TempDir()
	h := New(dir)
	var out, errOut bytes.Buffer
	h.Stdout = &out
	h.Stderr = &errOut

	err := h.Exec(`
set_log_context("dev", "cf")
info("first line")
ok("second line")
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if h.LogFile() == "" {
		t.Fatal("expected a materialized log file")
	}
	if !strings.HasPrefix(h.LogFile(), filepath.Join(dir, "tmp", "dev-cf.")) {
		t.Fatalf("unexpected log file path: %s", h.LogFile())
	}
	data, err := os.ReadFile(h.LogFile())
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	logged := string(data)
	if !strings.Contains(logged, "first line") || !strings.Contains(logged, "second line") {
		t.Fatalf("log file missing entries: %q", logged)
	}
	// The log file must carry no ANSI escapes even though stdout got colour.
	if strings.Contains(logged, "\x1b[") {
		t.Fatalf("log file retained ANSI escapes: %q", logged)
	}
	if !strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("stdout should still be coloured: %q", out.String())
	}
	// Every current caller (info/ok/warn/err, run_cmd's line streaming)
	// passes logWrite already-plain text, so the check above never actually
	// exercises ansiEscape stripping — it would pass identically even if
	// logWrite forgot to strip. Call logWrite directly with ANSI-coloured
	// text to prove the stripping itself works, not just that callers
	// happen not to trigger it.
	h.logWrite("\x1b[31mdirect-colour\x1b[0m")
	data, err = os.ReadFile(h.LogFile())
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	if strings.Contains(string(data), "\x1b[") {
		t.Fatalf("logWrite did not strip ANSI escapes: %q", string(data))
	}
	if !strings.Contains(string(data), "direct-colour") {
		t.Fatalf("logWrite dropped the message entirely: %q", string(data))
	}
}

func TestExecReturnsLuaError(t *testing.T) {
	h, _, _ := newTestHost(t)
	err := h.Exec(`error("boom from body")`, nil)
	if err == nil {
		t.Fatal("expected an error from a failing Lua body")
	}
	if !strings.Contains(err.Error(), "boom from body") {
		t.Fatalf("error should surface the Lua message: %v", err)
	}
}

func TestConvertRoundTripViaJSON(t *testing.T) {
	// Exercises fromLuaValue/toLuaValue indirectly through the json
	// capability with a nested structure, guarding against regressions in
	// the array-vs-map detection in fromLuaValue.
	h, out, _ := newTestHost(t)
	err := h.Exec(`
local payload = {ok=true, count=3, items={"a","b","c"}}
info(json.encode(payload))
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	line := strings.TrimSpace(strings.TrimPrefix(out.String(), ""))
	// Extract the JSON substring (stdout carries ANSI colour around it).
	start := strings.Index(line, "{")
	end := strings.LastIndex(line, "}")
	if start < 0 || end < 0 {
		t.Fatalf("no JSON object found in output: %q", line)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(line[start:end+1]), &decoded); err != nil {
		t.Fatalf("decoding emitted JSON: %v (line=%q)", err, line)
	}
	if decoded["count"].(float64) != 3 {
		t.Fatalf("unexpected count: %v", decoded["count"])
	}
	// items must round-trip as a JSON array, not an object keyed by
	// stringified indices — this is what actually exercises fromLuaValue's
	// array-vs-map detection; the "count" check above passes even when that
	// detection is completely broken.
	items, ok := decoded["items"].([]any)
	if !ok {
		t.Fatalf("expected items to encode as a JSON array, got %T: %v", decoded["items"], decoded["items"])
	}
	if want := []any{"a", "b", "c"}; !reflect.DeepEqual(items, want) {
		t.Fatalf("items = %v, want %v", items, want)
	}
}

func TestSetLogContextRetentionPrunesOldLogs(t *testing.T) {
	// Test that pruning deletes logs older than the retain count,
	// keeping only the newest N files.
	dir := t.TempDir()
	h := New(dir)
	var out, errOut bytes.Buffer
	h.Stdout = &out
	h.Stderr = &errOut

	tmpDir := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create 5 pre-existing log files with the stem "mylog" and timestamps.
	// We'll use fixed timestamps to ensure consistent chronological ordering.
	existingLogs := []string{
		"mylog.2026-01-01_100000.log",
		"mylog.2026-01-02_100000.log",
		"mylog.2026-01-03_100000.log",
		"mylog.2026-01-04_100000.log",
		"mylog.2026-01-05_100000.log",
	}
	for _, name := range existingLogs {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("old log content"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Call set_log_context with retain=3, which should keep only the 3 newest
	// and delete the 2 oldest.
	err := h.Exec(`
set_log_context("mylog", {retain=3})
info("new log entry")
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	// After materialization, there should be exactly 4 files:
	// - The 3 oldest pre-existing files from existingLogs should be deleted
	// - The 2 newest pre-existing files should remain
	// - The new file created by set_log_context should be added
	// Total: 2 (kept) + 1 (new) = 3 files

	// Wait, that's not right. The retain value applies after creating the new log.
	// So: 5 (pre-existing) + 1 (new) = 6 total, then prune to 3, leaving 3 files.
	// The 3 kept should be the 3 newest: the 2 most recent pre-existing files
	// plus the newly created file.

	matches, err := filepath.Glob(filepath.Join(tmpDir, "mylog.*.log"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(matches)

	// We should have 3 files left: the 3 newest (chronologically).
	if len(matches) != 3 {
		t.Fatalf("expected 3 log files after pruning, got %d: %v", len(matches), matches)
	}

	// Verify the survivors are the correct ones (the 3 newest):
	// The expected remaining files should be:
	// - mylog.2026-01-03_100000.log
	// - mylog.2026-01-04_100000.log
	// - mylog.2026-01-05_100000.log
	// - plus the new materialized log (which has the current timestamp)
	// Wait, after pruning to 3, we should only have 3 total, not 4.

	// Let me reconsider the logic:
	// - Before materialization: 5 pre-existing files
	// - During materialization: new file is created
	// - pruneRetain is called: should keep 3 most recent
	// - After pruning: 3 files remain (the 3 newest)

	// So the survivors should be the 2 most recent pre-existing ones
	// (2026-01-04 and 2026-01-05) plus the newly created one.
	// But when sorted lexicographically, the new one will sort after 2026-01-05
	// because it has today's date.

	// Verify the two oldest pre-existing logs were deleted:
	for _, name := range existingLogs[:3] {
		path := filepath.Join(tmpDir, name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be deleted, but it still exists", name)
		}
	}

	// Verify the two newest pre-existing logs still exist:
	for _, name := range existingLogs[3:5] {
		path := filepath.Join(tmpDir, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to still exist, but got error: %v", name, err)
		}
	}

	// Verify the new log was created and contains the expected content:
	if h.LogFile() == "" {
		t.Fatal("expected a materialized log file")
	}
	if _, err := os.Stat(h.LogFile()); err != nil {
		t.Fatalf("new log file should exist: %v", err)
	}
}

func TestSetLogContextRetentionZeroNoops(t *testing.T) {
	// When retain=0, no pruning should happen.
	dir := t.TempDir()
	h := New(dir)
	var out, errOut bytes.Buffer
	h.Stdout = &out
	h.Stderr = &errOut

	tmpDir := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create some pre-existing log files.
	existingLogs := []string{
		"test.2026-01-01_100000.log",
		"test.2026-01-02_100000.log",
	}
	for _, name := range existingLogs {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("old log"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Call set_log_context with retain=0, which should NOT prune.
	err := h.Exec(`
set_log_context("test", {retain=0})
info("entry")
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	// All files should still exist (no pruning happened).
	matches, err := filepath.Glob(filepath.Join(tmpDir, "test.*.log"))
	if err != nil {
		t.Fatal(err)
	}

	// We should have 3 files: the 2 pre-existing + the 1 new one.
	if len(matches) != 3 {
		t.Fatalf("expected 3 log files (no pruning), got %d: %v", len(matches), matches)
	}
}
