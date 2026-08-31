package kinds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestMakeInteractiveShellRunsAndSucceeds proves kind="interactive-shell"
// actually executes its `run` command end to end through
// internal/keybindings.Run and reports success for a clean exit.
func TestMakeInteractiveShellRunsAndSucceeds(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	runner, err := makeInteractiveShell("dev", manifest.Action{"run": "touch marker.txt"}, nil)
	if err != nil {
		t.Fatalf("makeInteractiveShell: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err != nil {
		t.Fatalf("marker.txt was not created by the wrapped process: %v", err)
	}
}

// TestMakeInteractiveShellNonZeroExitIsError proves a wrapped command that
// exits non-zero (and isn't a user-interrupt code) is reported as a
// failure.
func TestMakeInteractiveShellNonZeroExitIsError(t *testing.T) {
	t.Chdir(t.TempDir())
	runner, err := makeInteractiveShell("dev", manifest.Action{"run": "exit 7"}, nil)
	if err != nil {
		t.Fatalf("makeInteractiveShell: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() = nil, want an error for a nonzero exit")
	}
}

// TestMakeInteractiveShellRequiresRun proves a missing `run` is refused at
// factory-build time (before a Runner is even returned), matching
// admin_lib/interp.py::_make_interactive_shell's eager InterpError.
func TestMakeInteractiveShellRequiresRun(t *testing.T) {
	if _, err := makeInteractiveShell("dev", manifest.Action{}, nil); err == nil {
		t.Fatal("makeInteractiveShell = nil error, want one for a missing run")
	}
}

// TestMakeInteractiveShellRejectsBadKeys proves a malformed `keys` table is
// rejected at factory-build time rather than surfacing only once the
// action is actually run.
func TestMakeInteractiveShellRejectsBadKeys(t *testing.T) {
	action := manifest.Action{
		"run":  "true",
		"keys": map[string]any{"rr": map[string]any{"run": "echo hi"}}, // "rr" is not one char
	}
	if _, err := makeInteractiveShell("dev", action, nil); err == nil {
		t.Fatal("makeInteractiveShell = nil error, want one for an invalid keys table")
	}
}

// TestActionKeysAbsentIsNilNotError proves an action with no `keys` table
// at all is fine — nothing declared is not a validation failure.
func TestActionKeysAbsentIsNilNotError(t *testing.T) {
	keys, err := actionKeys(manifest.Action{"run": "true"}, "dev")
	if err != nil {
		t.Fatalf("actionKeys = %v, want nil error", err)
	}
	if keys != nil {
		t.Fatalf("actionKeys = %v, want nil map", keys)
	}
}

// TestActionKeysParsesDeclaredBindings proves a well-formed `keys` table
// round-trips through validate.ParseKeys via actionKeys.
func TestActionKeysParsesDeclaredBindings(t *testing.T) {
	action := manifest.Action{
		"run":  "true",
		"keys": map[string]any{"r": map[string]any{"action": "restart"}},
	}
	keys, err := actionKeys(action, "dev")
	if err != nil {
		t.Fatalf("actionKeys: %v", err)
	}
	if keys["r"].Action != "restart" {
		t.Fatalf("keys[r] = %+v, want Action=restart", keys["r"])
	}
}

// TestBuildLogBridgeConfigAbsentIsNil proves a manifest with no
// [log_bridge] table (or one with no hosts) yields a nil Config — no
// listener starts for a project that never opted in.
func TestBuildLogBridgeConfigAbsentIsNil(t *testing.T) {
	if cfg := buildLogBridgeConfig(nil); cfg != nil {
		t.Fatalf("buildLogBridgeConfig(nil manifest) = %+v, want nil", cfg)
	}
	m := &manifest.Manifest{}
	if cfg := buildLogBridgeConfig(m); cfg != nil {
		t.Fatalf("buildLogBridgeConfig(no log_bridge) = %+v, want nil", cfg)
	}
	m.LogBridge = map[string]any{"hosts": []any{}}
	if cfg := buildLogBridgeConfig(m); cfg != nil {
		t.Fatalf("buildLogBridgeConfig(empty hosts) = %+v, want nil", cfg)
	}
}

// TestBuildLogBridgeConfigResolvesHostsAndPortRange proves hosts are
// resolved through manifest.ResolveEnv (a ${VAR} placeholder expands) and
// port_range is carried through as [2]int.
func TestBuildLogBridgeConfigResolvesHostsAndPortRange(t *testing.T) {
	t.Setenv("MY_DEV_HOST", "dev.example.com")
	m := &manifest.Manifest{
		ProjectName: "myproj",
		LogBridge: map[string]any{
			"hosts":      []any{"${MY_DEV_HOST}", "localhost"},
			"port_range": []any{int64(9000), int64(9010)},
		},
	}
	cfg := buildLogBridgeConfig(m)
	if cfg == nil {
		t.Fatal("buildLogBridgeConfig = nil, want a Config")
	}
	if len(cfg.Hosts) != 2 || cfg.Hosts[0] != "dev.example.com" || cfg.Hosts[1] != "localhost" {
		t.Fatalf("Hosts = %v, want [dev.example.com localhost]", cfg.Hosts)
	}
	if cfg.PortRange != [2]int{9000, 9010} {
		t.Fatalf("PortRange = %v, want [9000 9010]", cfg.PortRange)
	}
	if cfg.Project != "myproj" {
		t.Fatalf("Project = %q, want myproj", cfg.Project)
	}
}

// TestScopeLogBridgeHostsSetsEnvVar proves the bridge-listen `hosts`
// resolution actually sets ADMIN_LOG_BRIDGE_HOSTS, which
// internal/logbridge.Start reads to narrow which hosts one listener
// advertises.
//
// MUTATION EVIDENCE: with scopeLogBridgeHosts's os.Setenv call removed,
// this test fails with `env = "", want "scoped.example.com"` — see the
// mutation_evidence field in this ticket's verdict for the pasted failure.
func TestScopeLogBridgeHostsSetsEnvVar(t *testing.T) {
	t.Setenv("ADMIN_LOG_BRIDGE_HOSTS", "")
	resolved, err := scopeLogBridgeHosts("scoped.example.com")
	if err != nil {
		t.Fatalf("scopeLogBridgeHosts: %v", err)
	}
	if resolved != "scoped.example.com" {
		t.Fatalf("resolved = %q", resolved)
	}
	if got := os.Getenv("ADMIN_LOG_BRIDGE_HOSTS"); got != "scoped.example.com" {
		t.Fatalf("env = %q, want %q", got, "scoped.example.com")
	}
}

// TestScopeLogBridgeHostsPropagatesResolveError proves an unresolvable
// ${VAR} in `hosts` is surfaced as an error rather than silently scoping to
// a literal placeholder string.
func TestScopeLogBridgeHostsPropagatesResolveError(t *testing.T) {
	os.Unsetenv("ADMIN_KEYBINDINGS_TEST_UNSET_HOST")
	if _, err := scopeLogBridgeHosts("${ADMIN_KEYBINDINGS_TEST_UNSET_HOST}"); err == nil {
		t.Fatal("scopeLogBridgeHosts = nil error, want one for an unresolved required var")
	}
}

// TestMakeBridgeListenRejectsBadKeys mirrors
// TestMakeInteractiveShellRejectsBadKeys for kind="bridge-listen".
func TestMakeBridgeListenRejectsBadKeys(t *testing.T) {
	action := manifest.Action{"keys": map[string]any{"rr": map[string]any{"run": "echo hi"}}}
	if _, err := makeBridgeListen("bridge", action, nil); err == nil {
		t.Fatal("makeBridgeListen = nil error, want one for an invalid keys table")
	}
}

// TestMakeBridgeListenStopsOnHostResolveError proves a bridge-listen action
// whose `hosts` references an unset required var fails fast — before ever
// starting the (deliberately infinite) parked subprocess — so this test
// completes instantly instead of hanging.
func TestMakeBridgeListenStopsOnHostResolveError(t *testing.T) {
	t.Chdir(t.TempDir())
	os.Unsetenv("ADMIN_KEYBINDINGS_TEST_UNSET_HOST_2")
	runner, err := makeBridgeListen("bridge", manifest.Action{"hosts": "${ADMIN_KEYBINDINGS_TEST_UNSET_HOST_2}"}, nil)
	if err != nil {
		t.Fatalf("makeBridgeListen: %v", err)
	}
	if err := runner(nil); err == nil || !strings.Contains(err.Error(), "bridge") {
		t.Fatalf("runner() = %v, want an error naming the action", err)
	}
}
