package kinds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestLuaRunsHostAPI proves kind="lua" actually dispatches through
// internal/luahost.Host.Exec: a Lua body calling run_cmd (a host function,
// not Lua stdlib) can create a real file on disk.
func TestLuaRunsHostAPI(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	runner, err := makeLua("touch", manifest.Action{"run": `run_cmd("touch marker.txt")`}, nil, nil)
	if err != nil {
		t.Fatalf("makeLua: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err != nil {
		t.Fatalf("marker.txt was not created by the Lua body: %v", err)
	}
}

// TestLuaConfigReachesBody proves the inline `config` table on the action
// reaches the Lua body as the `config` global.
//
// MUTATION EVIDENCE: change makeLua's config extraction from
// action["config"].(map[string]any) to always pass nil, and this test
// fails: config.value is nil in Lua, err("missing") fires, run_cmd's marker
// file is never created, and the Runner returns a non-nil error.
func TestLuaConfigReachesBody(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	runner, err := makeLua("cfg", manifest.Action{
		"run":    `if config.value == "hi" then run_cmd("touch seen.txt") else err("missing") end`,
		"config": map[string]any{"value": "hi"},
	}, nil, nil)
	if err != nil {
		t.Fatalf("makeLua: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "seen.txt")); err != nil {
		t.Fatalf("config.value did not reach the Lua body: %v", err)
	}
}

// TestLuaManifestConfigReachesBody proves admin-3rgp.43's actual acceptance
// criterion: a Lua body can read a manifest-level archetype config table
// ([go] here) that its action carries no inline copy of — the gap
// admin-3rgp.36's docs/architecture/lua-host-api.md flagged as "manifest-
// level archetype config tables reaching a Lua body automatically" needing
// "either a wider Factory signature or a second dispatch-time pass".
//
// MUTATION EVIDENCE: change makeLua's `cfg := archetypeConfigTables(m)` to
// `cfg := map[string]any{}` (drop the manifest-level tables entirely), and
// this test fails: config.go is nil in Lua, so `config.go.binary` is
// indexing a nil value, the Lua body raises a runtime error ("attempt to
// index a non-table object"), Exec returns a non-nil error, and the Runner
// returns non-nil where the test expects nil — marker.txt is never created.
func TestLuaManifestConfigReachesBody(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := &manifest.Manifest{Go: map[string]any{"binary": "bin/admin"}}
	runner, err := makeLua("read-go-config", manifest.Action{
		"run": `if config.go.binary == "bin/admin" then run_cmd("touch marker.txt") ` +
			`else err("got " .. tostring(config.go.binary)) end`,
	}, m, nil)
	if err != nil {
		t.Fatalf("makeLua: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err != nil {
		t.Fatalf("the manifest-level [go] table did not reach the Lua body: %v", err)
	}
}

// TestLuaInlineConfigOverridesManifestTable proves the merge order documented
// on makeLua: an inline `[actions.X.config]` key wins over a manifest-level
// domain table of the same name.
func TestLuaInlineConfigOverridesManifestTable(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := &manifest.Manifest{Go: map[string]any{"binary": "from-manifest"}}
	runner, err := makeLua("override", manifest.Action{
		"run":    `if config.go == "from-inline" then run_cmd("touch marker.txt") else err("wrong") end`,
		"config": map[string]any{"go": "from-inline"},
	}, m, nil)
	if err != nil {
		t.Fatalf("makeLua: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err != nil {
		t.Fatalf("inline config did not override the manifest-level table: %v", err)
	}
}

// TestLuaArgsReachBody proves the Runner's args slice reaches the Lua body
// as the `args` global — admin-3rgp.37's addition, needed by every rewritten
// body that does its own target/flag parsing (`./admin build windows`).
//
// MUTATION EVIDENCE: change makeLua's `h.Exec(source, cfg, args...)` to
// `h.Exec(source, cfg)` (drop the args), and this test fails: `args[1]` is
// nil in Lua, `args[1] == "windows"` is false, err("missing") fires,
// marker.txt is never created, and the Runner returns a non-nil error.
func TestLuaArgsReachBody(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	runner, err := makeLua("build", manifest.Action{
		"run": `if args[1] == "windows" and #args == 1 then run_cmd("touch marker.txt") else err("missing") end`,
	}, nil, nil)
	if err != nil {
		t.Fatalf("makeLua: %v", err)
	}
	if err := runner([]string{"windows"}); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err != nil {
		t.Fatalf("args did not reach the Lua body: %v", err)
	}
}

// TestLuaErrorMessageDropsStackTrace proves luaErrorMessage strips
// gopher-lua's stack trace from a body's error("...") call, keeping just the
// message a body's `error()` raised — the message an unknown-target/failed-
// build path prints, not a "[G]: in function 'error'" traceback dump that
// would make every deliberate, expected failure look like an interpreter
// bug.
//
// The assertion is on the RETURNED error, not on an unwrapped inner one. The
// returned error is what the CLI prints, and it used to be the raw
// *lua.ApiError via %w — so the clean h.Err line was immediately followed by a
// "stack traceback: [G]: in function 'error'" dump on the error line beneath
// it. makeLua now formats the stripped message with %s, so there is
// deliberately nothing left to unwrap.
//
// MUTATION EVIDENCE: change luaErrorMessage's `apiErr.Object.String()` to
// `err.Error()` unconditionally, or makeLua's `%s` back to `%w`, and this test
// fails: the returned message contains "stack traceback".
func TestLuaErrorMessageDropsStackTrace(t *testing.T) {
	t.Chdir(t.TempDir())
	runner, mkErr := makeLua("build", manifest.Action{"run": `error("build failed")`}, nil, nil)
	if mkErr != nil {
		t.Fatalf("makeLua: %v", mkErr)
	}
	runErr := runner(nil)
	if runErr == nil {
		t.Fatal("runner() = nil, want an error")
	}
	got := runErr.Error()
	// Lua's error() prepends "<chunk>:line: " position info at its default
	// level — kept (it is not a stack trace); only the "stack traceback"
	// block is what this fix drops.
	if !strings.HasSuffix(got, "build failed") {
		t.Fatalf("error = %q, want it to end with %q", got, "build failed")
	}
	if strings.Contains(got, "traceback") {
		t.Fatalf("error leaked a stack trace: %q", got)
	}
	if !strings.Contains(got, `action "build"`) {
		t.Errorf("error = %q, want it to name the action", got)
	}
}

// luaErrorMessage must tolerate a nil error rather than dereferencing it — it
// is called on whatever Exec hands back, and a nil there should not turn a
// clean run into a segfault.
func TestLuaErrorMessageHandlesNil(t *testing.T) {
	if got := luaErrorMessage(nil); got != "" {
		t.Errorf("luaErrorMessage(nil) = %q, want empty", got)
	}
}

// TestLuaErrorPropagates proves a Lua runtime error becomes a non-nil Go
// error rather than being swallowed.
func TestLuaErrorPropagates(t *testing.T) {
	t.Chdir(t.TempDir())
	runner, err := makeLua("boom", manifest.Action{"run": `error("kaboom")`}, nil, nil)
	if err != nil {
		t.Fatalf("makeLua: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() = nil, want an error from the failing Lua body")
	}
}

// TestLuaEmptyRunIsRefused proves a blank `run` is rejected before ever
// reaching the Lua VM.
func TestLuaEmptyRunIsRefused(t *testing.T) {
	t.Chdir(t.TempDir())
	runner, err := makeLua("blank", manifest.Action{}, nil, nil)
	if err != nil {
		t.Fatalf("makeLua: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() = nil, want an error for a missing 'run'")
	}
}
