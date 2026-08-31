package luahost

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
	if !strings.Contains(out.String(), "hi-from-run-cmd") {
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
}
