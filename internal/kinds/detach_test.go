// detach_test.go covers `detach = true`'s producer-independent contract:
// RunAction (every step, plain CLI or already-launched) never consults
// `detach` and never reaches the project's [terminal] launcher (ADR-0020);
// RunCommand's top-level dispatch does, through launchCommand, identically
// whether its CommandProducer is nil (the CLI) or non-nil (the dashboard).
// It replaces lifetime_test.go — the Lifetime kind-catalog attribute
// admin-ysxr.2 deleted.
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
// (ran.txt) if it executed inline — so "did the launcher intercept it" and
// "did it run inline" are both directly observable. The "dev" command lets a
// producer-path test dispatch through RunCommand; launchCommand keys the
// live entry on the command's first step ("dev-server"), the same name the
// action-level RunAction tests use, so existing livework lookups by that
// name work either way.
func detachManifest(terminal map[string]any) *manifest.Manifest {
	return &manifest.Manifest{
		Terminal: terminal,
		Custom: map[string]manifest.Command{
			"dev": {Steps: []string{"dev-server"}},
		},
		Actions: map[string]manifest.Action{
			"dev-server": {
				"kind":   "shell",
				"run":    "touch ran.txt",
				"detach": true,
			},
		},
	}
}

// TestRunActionCLIDetachRunsInlineNeverReachesLauncher proves RunAction
// itself — every step's own dispatch, whichever surface the enclosing
// command landed on — runs a detached action inline and never reaches the
// launcher, even with a spawnable [terminal] declared. The launcher decision
// is made once, in launchCommand, before any step runs.
func TestRunActionCLIDetachRunsInlineNeverReachesLauncher(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	logPath := filepath.Join(dir, "terminal-calls.log")
	m := detachManifest(withFakeTerminal(t, "fake-handle-1", logPath))

	stderr := captureStderr(t, func() {
		if err := RunAction(m, r, "dev-server", nil); err != nil {
			t.Fatalf("RunAction = %v, want nil", err)
		}
	})
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	assertFileExists(t, dir, "ran.txt")

	if logged, err := os.ReadFile(logPath); err == nil && len(logged) > 0 {
		t.Errorf("terminal log = %q, want empty — a CLI dispatch must never reach the launcher", string(logged))
	}
	if got := len(livework.Default.List()); got != before {
		t.Errorf("live-work registry grew from %d to %d — a CLI dispatch must never register a launcher entry", before, got)
	}
}

// TestRunCommandDashboardProducerDetachDispatchesThroughLauncher is the
// producer-path twin: the identical detached action, dispatched through
// RunCommand with a non-nil CommandProducer, still reaches the launcher and
// never falls back to the producer's own default surface.
func TestRunCommandDashboardProducerDetachDispatchesThroughLauncher(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())
	launched := false

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	m := detachManifest(map[string]any{"spawn": "echo fake-handle-1"})

	if err := RunCommand(m, r, "dev", nil, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if launched {
		t.Error("a dashboard producer must not fall back to its own default surface when a launcher is declared")
	}
	assertFileMissing(t, dir, "ran.txt")

	entries := livework.Default.List()
	found := false
	for _, e := range entries {
		if e.Name == "dev-server" && e.Handle == "fake-handle-1" {
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

// TestRunActionNoTerminalRunsInline proves the same action with no
// [terminal] declared runs inline — the unaffected case, since a CLI
// dispatch never consulted the launcher in the first place.
func TestRunActionNoTerminalRunsInline(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	m := detachManifest(nil)

	if err := RunAction(m, r, "dev-server", nil); err != nil {
		t.Fatalf("RunAction = %v, want nil", err)
	}
	assertFileExists(t, dir, "ran.txt")

	after := livework.Default.List()
	if len(after) != before {
		t.Errorf("live-work registry grew from %d to %d entries — a plain-CLI dispatch must never "+
			"register a launcher entry", before, len(after))
	}
}

// TestRunActionDetachAbsentRunsInline proves an action with no `detach` key
// at all is unaffected by a declared [terminal] — irrelevant to a CLI
// dispatch either way, since `detach` is never consulted on this path.
func TestRunActionDetachAbsentRunsInline(t *testing.T) {
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
	if err := RunAction(m, r, "build", nil); err != nil {
		t.Fatalf("RunAction = %v, want nil", err)
	}
	assertFileExists(t, dir, "ran.txt")
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

// TestMultiTargetPerTargetDetachRunsInlineFromCLI proves the multi-target
// factory's Runner no longer has a launcher branch of its own at all: a
// picked target's own `detach = true` override is never consulted from a
// plain-CLI dispatch (RunAction), whatever the parent action's default is.
func TestMultiTargetPerTargetDetachRunsInlineFromCLI(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())

	r := NewRegistry()
	r.MustRegister("multi-target", makeMultiTarget)
	r.MustRegister("shell", makeShell)

	logPath := filepath.Join(dir, "terminal-calls.log")
	m := &manifest.Manifest{
		Terminal: withFakeTerminal(t, "fake-handle-2", logPath),
		Actions: map[string]manifest.Action{
			"dev": {
				"kind":   "multi-target",
				"detach": false,
				"cf":     map[string]any{"run": "touch cf-ran.txt", "detach": true},
				"local":  map[string]any{"run": "touch local-ran.txt"},
			},
		},
	}

	if err := RunAction(m, r, "dev", []string{"cf"}); err != nil {
		t.Fatalf("RunAction(cf) = %v, want nil", err)
	}
	assertFileExists(t, dir, "cf-ran.txt")

	if logged, err := os.ReadFile(logPath); err == nil && len(logged) > 0 {
		t.Errorf("terminal log = %q, want empty — a CLI dispatch must never reach the launcher", string(logged))
	}
	if got := len(livework.Default.List()); got != before {
		t.Errorf("live-work registry grew from %d to %d — a CLI dispatch must never register a launcher entry", before, got)
	}
}

// TestMultiTargetPerTargetDetachDashboardProducerDispatchesThroughLauncher is
// the producer-path twin: the identical per-target override, dispatched
// through RunCommand with a non-nil CommandProducer, still reaches the
// launcher via launchCommand's own dispatchDetach resolution.
func TestMultiTargetPerTargetDetachDashboardProducerDispatchesThroughLauncher(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())
	launched := false

	r := NewRegistry()
	r.MustRegister("multi-target", makeMultiTarget)
	r.MustRegister("shell", makeShell)

	m := &manifest.Manifest{
		Terminal: map[string]any{"spawn": "echo fake-handle-2"},
		Custom: map[string]manifest.Command{
			"dev": {Steps: []string{"pick"}},
		},
		Actions: map[string]manifest.Action{
			"pick": {
				"kind":   "multi-target",
				"detach": false,
				"cf":     map[string]any{"run": "touch cf-ran.txt", "detach": true},
				"local":  map[string]any{"run": "touch local-ran.txt"},
			},
		},
	}

	if err := RunCommand(m, r, "dev", []string{"cf"}, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand(cf) = %v, want nil", err)
	}
	if launched {
		t.Error("a dashboard producer must not fall back to its own default surface when a launcher is declared")
	}
	assertFileMissing(t, dir, "cf-ran.txt")

	entries := livework.Default.List()
	found := false
	for _, e := range entries {
		if e.Name == "pick" && e.Handle == "fake-handle-2" {
			found = true
		}
	}
	if !found {
		t.Errorf("live-work registry = %+v, want an entry named pick with handle fake-handle-2", entries)
	}
	if got := len(entries); got != before+1 {
		t.Errorf("live-work registry has %d entries, want exactly %d", got, before+1)
	}
}

// TestDetachedEntryAttachRunsLauncherAttachWithHandle is admin-ysxr.3's
// acceptance criterion: pressing attach on a launcher-dispatched row runs
// that launcher's attach command with ADMIN_HANDLE set to the handle spawn
// printed. The fake launcher's attach writes ADMIN_HANDLE to a file rather
// than actually attaching to anything, so the observable proof is that
// file's content. Dispatched through a dashboard CommandProducer here, but a
// nil (CLI) producer reaches the identical launcher entry.
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

	if err := RunCommand(m, r, "dev", nil, dashboardProducer(new(bool))); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
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
// ADMIN_HANDLE set to the spawned handle. Reached through the producer path,
// for the same reason TestDetachedEntryAttachRunsLauncherAttachWithHandle is.
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

	if err := RunCommand(m, r, "dev", nil, dashboardProducer(new(bool))); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
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
