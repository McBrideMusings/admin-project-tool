package kinds

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestLuaRunsHostAPI proves kind="lua" actually dispatches through
// internal/luahost.Host.Exec: a Lua body calling run_cmd (a host function,
// not Lua stdlib) can create a real file on disk.
func TestLuaRunsHostAPI(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	runner, err := makeLua("touch", manifest.Action{"run": `run_cmd("touch marker.txt")`})
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
	})
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

// TestLuaErrorPropagates proves a Lua runtime error becomes a non-nil Go
// error rather than being swallowed.
func TestLuaErrorPropagates(t *testing.T) {
	t.Chdir(t.TempDir())
	runner, err := makeLua("boom", manifest.Action{"run": `error("kaboom")`})
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
	runner, err := makeLua("blank", manifest.Action{})
	if err != nil {
		t.Fatalf("makeLua: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() = nil, want an error for a missing 'run'")
	}
}
