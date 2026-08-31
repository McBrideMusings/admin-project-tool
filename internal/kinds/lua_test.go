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

	runner, err := makeLua("touch", manifest.Action{"run": `run_cmd("touch marker.txt")`}, nil)
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
	}, nil)
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
	}, m)
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
	}, m)
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

// TestLuaErrorPropagates proves a Lua runtime error becomes a non-nil Go
// error rather than being swallowed.
func TestLuaErrorPropagates(t *testing.T) {
	t.Chdir(t.TempDir())
	runner, err := makeLua("boom", manifest.Action{"run": `error("kaboom")`}, nil)
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
	runner, err := makeLua("blank", manifest.Action{}, nil)
	if err != nil {
		t.Fatalf("makeLua: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() = nil, want an error for a missing 'run'")
	}
}
