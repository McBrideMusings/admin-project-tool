// detach_test.go covers admin-ysxr.2: `detach = true` on an action
// dispatches through the project's [terminal] launcher (ADR-0020) instead
// of running inline. It replaces lifetime_test.go — the Lifetime
// kind-catalog attribute this ticket deletes.
package kinds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// detachManifest is the shared fixture for the acceptance tests: one
// detached action whose `run` would leave a real, checkable side effect
// (ran.txt) if it executed inline — so "did the launcher intercept it"
// and "did it run inline" are both directly observable.
func detachManifest(terminal map[string]any) *manifest.Manifest {
	return &manifest.Manifest{
		Terminal: terminal,
		Actions: map[string]manifest.Action{
			"dev-server": {
				"kind":   "shell",
				"run":    "touch ran.txt",
				"detach": true,
			},
		},
	}
}

// TestRunActionRoutedDetachDispatchesThroughLauncher is the ticket's first
// acceptance criterion: with a [terminal] declaring `spawn = 'echo
// fake-handle-1'`, dispatching a detached action prints nothing from the
// action itself (its Runner never executes in this process) and records a
// live entry whose handle is fake-handle-1.
func TestRunActionRoutedDetachDispatchesThroughLauncher(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	m := detachManifest(map[string]any{"spawn": "echo fake-handle-1"})

	stderr := captureStderr(t, func() {
		if err := RunActionRouted(m, r, "dev-server", nil); err != nil {
			t.Fatalf("RunActionRouted = %v, want nil", err)
		}
	})
	if stderr != "" {
		t.Errorf("stderr = %q, want empty — a successful spawn must print no warning", stderr)
	}
	assertFileMissing(t, dir, "ran.txt")

	entries := livework.Default.List()
	found := false
	for _, e := range entries {
		if e.Name == "dev-server" && e.Handle == "fake-handle-1" && e.Kind == "shell" {
			found = true
		}
	}
	if !found {
		t.Errorf("live-work registry = %+v, want an entry for dev-server with handle fake-handle-1", entries)
	}
	if got := len(entries); got != before+1 {
		t.Errorf("live-work registry grew from %d to %d, want exactly one new entry", before, got)
	}
}

// TestRunActionRoutedNoTerminalRunsInline is the ticket's second acceptance
// criterion: the same manifest, minus [terminal], runs the detached action
// inline exactly as before.
func TestRunActionRoutedNoTerminalRunsInline(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	m := detachManifest(nil)

	if err := RunActionRouted(m, r, "dev-server", nil); err != nil {
		t.Fatalf("RunActionRouted = %v, want nil (inline fallback)", err)
	}
	assertFileExists(t, dir, "ran.txt")

	after := livework.Default.List()
	if len(after) != before {
		t.Errorf("live-work registry grew from %d to %d entries — a detached action with no "+
			"[terminal] must fall back to inline and never register itself", before, len(after))
	}
}

// TestRunActionRoutedDetachAbsentRunsInline proves an action with no
// `detach` key at all is unaffected by a declared [terminal] — the launcher
// is opt-in per action, not activated by mere presence of the table.
func TestRunActionRoutedDetachAbsentRunsInline(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	m := &manifest.Manifest{
		Terminal: map[string]any{"spawn": "echo should-not-be-called"},
		Actions: map[string]manifest.Action{
			"build": {"kind": "shell", "run": "touch ran.txt"},
		},
	}
	if err := RunActionRouted(m, r, "build", nil); err != nil {
		t.Fatalf("RunActionRouted = %v, want nil", err)
	}
	assertFileExists(t, dir, "ran.txt")
}

// TestRunActionRoutedDetachSpawnFailureDegradesToInlineWithWarning mirrors
// the herdr-split-failure case this ticket's dispatchToPane replaced:
// [terminal] IS declared but spawn fails — the action must still run
// inline and print exactly one warning naming the action and the failure.
func TestRunActionRoutedDetachSpawnFailureDegradesToInlineWithWarning(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	m := detachManifest(map[string]any{"spawn": "exit 1"})

	stderr := captureStderr(t, func() {
		if err := RunActionRouted(m, r, "dev-server", nil); err != nil {
			t.Fatalf("RunActionRouted = %v, want nil (degrade to inline)", err)
		}
	})
	assertFileExists(t, dir, "ran.txt")

	after := livework.Default.List()
	if len(after) != before {
		t.Errorf("live-work registry grew from %d to %d entries — a failed spawn must fall back "+
			"to inline and never register itself", before, len(after))
	}
	if !strings.Contains(stderr, "dev-server") {
		t.Errorf("warning line = %q, want it to name the action", stderr)
	}
}

// -- ValidateDetach ---------------------------------------------------------

func TestValidateDetachAcceptsBool(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {"kind": "shell", "run": "true", "detach": true},
	}
	if err := ValidateDetach(actions); err != nil {
		t.Errorf("ValidateDetach() = %v, want nil", err)
	}
}

func TestValidateDetachRejectsNonBoolOnAction(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {"kind": "shell", "run": "true", "detach": "true"},
	}
	err := ValidateDetach(actions)
	if err == nil || !strings.Contains(err.Error(), "detach must be a boolean") {
		t.Errorf("ValidateDetach() = %v, want a 'detach must be a boolean' error", err)
	}
	if err != nil && !strings.Contains(err.Error(), `"dev"`) {
		t.Errorf("error = %v, want it to name the action", err)
	}
}

func TestValidateDetachRejectsNonBoolOnTarget(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {
			"kind": "multi-target",
			"cf":   map[string]any{"run": "true", "detach": "yes"},
		},
	}
	err := ValidateDetach(actions)
	if err == nil || !strings.Contains(err.Error(), "detach must be a boolean") {
		t.Errorf("ValidateDetach() = %v, want a 'detach must be a boolean' error", err)
	}
	if err != nil && !strings.Contains(err.Error(), `"cf"`) {
		t.Errorf("error = %v, want it to name the target", err)
	}
}

// -- resolveTargetDetach inheritance -----------------------------------------

func TestResolveTargetDetachInheritance(t *testing.T) {
	cases := []struct {
		name                     string
		parentVal, parentPresent bool
		targetVal, targetPresent bool
		want                     bool
	}{
		{"nothing declared anywhere", false, false, false, false, false},
		{"parent true, target silent inherits", true, true, false, false, true},
		{"parent true, target overrides false", true, true, false, true, false},
		{"parent absent, target true", false, false, true, true, true},
		{"parent false, target true overrides", false, true, true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveTargetDetach(c.parentVal, c.parentPresent, c.targetVal, c.targetPresent)
			if got != c.want {
				t.Errorf("resolveTargetDetach(%v, %v, %v, %v) = %v, want %v",
					c.parentVal, c.parentPresent, c.targetVal, c.targetPresent, got, c.want)
			}
		})
	}
}

// TestMultiTargetPerTargetDetachDispatchesThroughLauncher proves the
// per-target override end to end: an action with a parent default of
// detach=false and one target overriding detach=true dispatches only that
// target through the launcher, recording a live entry named "<action>:
// <target>".
func TestMultiTargetPerTargetDetachDispatchesThroughLauncher(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())

	r := NewRegistry()
	r.MustRegister("multi-target", makeMultiTarget)

	m := &manifest.Manifest{
		Terminal: map[string]any{"spawn": "echo fake-handle-2"},
		Actions: map[string]manifest.Action{
			"dev": {
				"kind":   "multi-target",
				"detach": false,
				"cf":     map[string]any{"run": "touch cf-ran.txt", "detach": true},
				"local":  map[string]any{"run": "touch local-ran.txt"},
			},
		},
	}

	if err := RunActionRouted(m, r, "dev", []string{"cf"}); err != nil {
		t.Fatalf("RunActionRouted(cf) = %v, want nil", err)
	}
	assertFileMissing(t, dir, "cf-ran.txt")

	entries := livework.Default.List()
	found := false
	for _, e := range entries {
		if e.Name == "dev:cf" && e.Handle == "fake-handle-2" {
			found = true
		}
	}
	if !found {
		t.Errorf("live-work registry = %+v, want an entry named dev:cf with handle fake-handle-2", entries)
	}

	if err := RunActionRouted(m, r, "dev", []string{"local"}); err != nil {
		t.Fatalf("RunActionRouted(local) = %v, want nil", err)
	}
	assertFileExists(t, dir, "local-ran.txt")

	if got := len(livework.Default.List()); got != before+1 {
		t.Errorf("live-work registry has %d entries, want exactly %d (only the detached target registers)",
			got, before+1)
	}
}

// TestDetachedEntryAttachRunsLauncherAttachWithHandle is admin-ysxr.3's
// acceptance criterion: pressing attach on a launcher-dispatched row runs
// that launcher's attach command with ADMIN_HANDLE set to the handle spawn
// printed. The fake launcher's attach writes ADMIN_HANDLE to a file rather
// than actually attaching to anything, so the observable proof is that
// file's content.
func TestDetachedEntryAttachRunsLauncherAttachWithHandle(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	m := detachManifest(map[string]any{
		"spawn":  "echo fake-handle-1",
		"attach": `echo "$ADMIN_HANDLE" > attached.txt`,
		"kill":   `echo "$ADMIN_HANDLE" > killed.txt`,
	})

	if err := RunActionRouted(m, r, "dev-server", nil); err != nil {
		t.Fatalf("RunActionRouted = %v, want nil", err)
	}

	if err := livework.Default.Attach("dev-server"); err != nil {
		t.Fatalf("Attach(dev-server) = %v, want nil", err)
	}
	assertFileExists(t, dir, "attached.txt")
	body, err := os.ReadFile(filepath.Join(dir, "attached.txt"))
	if err != nil {
		t.Fatalf("reading attached.txt: %v", err)
	}
	if got := strings.TrimSpace(string(body)); got != "fake-handle-1" {
		t.Errorf("attached.txt = %q, want the spawned handle %q", got, "fake-handle-1")
	}
}

// TestDetachedEntryKillRunsLauncherKillWithHandle proves killing a
// launcher-dispatched row runs the launcher's kill command, also with
// ADMIN_HANDLE set to the spawned handle.
func TestDetachedEntryKillRunsLauncherKillWithHandle(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	m := detachManifest(map[string]any{
		"spawn":  "echo fake-handle-1",
		"attach": `echo "$ADMIN_HANDLE" > attached.txt`,
		"kill":   `echo "$ADMIN_HANDLE" > killed.txt`,
	})

	if err := RunActionRouted(m, r, "dev-server", nil); err != nil {
		t.Fatalf("RunActionRouted = %v, want nil", err)
	}

	if err := livework.Default.Kill("dev-server"); err != nil {
		t.Fatalf("Kill(dev-server) = %v, want nil", err)
	}
	assertFileExists(t, dir, "killed.txt")
	body, err := os.ReadFile(filepath.Join(dir, "killed.txt"))
	if err != nil {
		t.Fatalf("reading killed.txt: %v", err)
	}
	if got := strings.TrimSpace(string(body)); got != "fake-handle-1" {
		t.Errorf("killed.txt = %q, want the spawned handle %q", got, "fake-handle-1")
	}
}
