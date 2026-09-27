package kinds

import (
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestMakeNukeDevWithNoConfigIsANoOp proves a nuke-dev command declared
// with no [dev_kill] table configured must be a no-op, not an error.
func TestMakeNukeDevWithNoConfigIsANoOp(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	runner, err := makeNukeDev("nuke-dev", manifest.Action{}, &manifest.Manifest{}, nil)
	if err != nil {
		t.Fatalf("makeNukeDev: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Errorf("runner(nil) = %v, want nil when [dev_kill] is unconfigured", err)
	}
}

// TestMakeNukeDevOnAFreeDeclaredPortIsANoOp proves the acceptance criterion
// through the kind factory directly: "a port already free is a no-op that
// exits zero."
func TestMakeNukeDevOnAFreeDeclaredPortIsANoOp(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	port := freeTCPPortForKillPort(t)
	m := &manifest.Manifest{Settings: map[string]map[string]any{"dev_kill": {
		"ports": []any{int64(port)},
	}}}
	runner, err := makeNukeDev("nuke-dev", manifest.Action{}, m, nil)
	if err != nil {
		t.Fatalf("makeNukeDev: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Errorf("runner(nil) = %v, want nil for an already-free declared port", err)
	}
}

// TestMakeNukeDevRejectsAnUnresolvablePortEnvVar proves buildPortSpecs
// surfaces a bad ${VAR} reference as an error rather than silently skipping
// the port (which would make the whole sweep quietly no-op on a
// misconfigured manifest).
//
// MUTATION EVIDENCE: in resolvePortValue's string case, replace the
// `strconv.Atoi` error return with `return 0, nil` (swallow the error) and
// this test fails — a bad port would resolve to a phantom port 0 with no
// error surfaced.
func TestMakeNukeDevRejectsAnUnresolvablePortEnvVar(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	m := &manifest.Manifest{Settings: map[string]map[string]any{"dev_kill": {
		"ports": []any{"not-a-port"},
	}}}
	runner, err := makeNukeDev("nuke-dev", manifest.Action{}, m, nil)
	if err != nil {
		t.Fatalf("makeNukeDev: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Error("runner(nil) = nil, want an error for an unresolvable port value")
	}
}
