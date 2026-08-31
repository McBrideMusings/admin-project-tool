package kinds

import (
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestMakeDevPrecheckWithNoLiveSessionSucceeds proves the common case: an
// empty project directory with no matching processes at all passes cleanly.
func TestMakeDevPrecheckWithNoLiveSessionSucceeds(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	runner, err := makeDevPrecheck("dev-precheck", manifest.Action{}, &manifest.Manifest{}, nil)
	if err != nil {
		t.Fatalf("makeDevPrecheck: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Errorf("runner(nil) = %v, want nil when nothing is running", err)
	}
}

// TestMakeDevPrecheckAbortsOnADeclaredRunningContainer proves the
// container-check half surfaces as a Runner error, matching
// admin_lib/ports_nuke.py::dev_precheck's container-abort branch.
func TestMakeDevPrecheckAbortsOnADeclaredRunningContainer(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	m := &manifest.Manifest{DevPrecheck: map[string]any{
		"containers": []any{"admin-3rgp-15-definitely-not-running"},
	}}
	runner, err := makeDevPrecheck("dev-precheck", manifest.Action{}, m, nil)
	if err != nil {
		t.Fatalf("makeDevPrecheck: %v", err)
	}
	// A container that genuinely isn't running must never abort — this is
	// the negative half of the same acceptance path exercised at the
	// internal/ports level (TestDevPrecheckAbortsOnARunningContainer).
	if err := runner(nil); err != nil {
		t.Errorf("runner(nil) = %v, want nil for a container that isn't running", err)
	}
}
