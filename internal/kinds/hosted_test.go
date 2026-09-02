// hosted_test.go covers admin-ysxr.4's collapsed command-dispatch entry
// point: RunCommand takes a *CommandProducer now, and a nil producer (the
// plain CLI, which can host a run itself) versus a non-nil one (the
// dashboard, which cannot) must land the identical detached step on
// opposite surfaces. The paired tests below are the whole point — the same
// manifest, the same fake [terminal] launcher, the same detached step,
// dispatched through each producer, must land on opposite surfaces.
package kinds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// longRunningManifest is the shared fixture for the paired entry-point
// tests: one command whose single step is an action carrying `detach =
// true` (ADR-0020) plus a declared [terminal] table, so the routing
// decision under test is genuine rather than a stub kind invented for the
// test. terminal is the raw table this fixture's manifest.Manifest.Terminal
// carries — built by withFakeTerminal in each test, since the log path it
// writes to differs per test.
func longRunningManifest(terminal map[string]any) *manifest.Manifest {
	return longRunningManifestNamed(terminal, "hosted-dev-server")
}

// longRunningManifestNamed is longRunningManifest with the step (and
// therefore the live-work registry ID a real launcher dispatch registers
// under) parameterized — every test that actually completes a launcher
// dispatch shares the process-wide livework.Default registry, so two tests
// reusing the same step name collide: the second Add overwrites the first
// entry in place instead of growing the registry (Add keeps the original
// insertion position for an existing ID). Each such test must use its own
// step name.
func longRunningManifestNamed(terminal map[string]any, step string) *manifest.Manifest {
	return &manifest.Manifest{
		Terminal: terminal,
		Custom: map[string]manifest.Command{
			"dev": {Steps: []string{step}},
		},
		Actions: map[string]manifest.Action{
			step: {"kind": "vite-dev", "detach": true},
		},
	}
}

// registryRecordingInline registers a stub factory for the detached kind
// that flips *ran when its Runner actually executes in this process, so a
// test can tell "ran inline here" from "was handed to the launcher".
func registryRecordingInline(ran *bool) *Registry {
	r := NewRegistry()
	r.MustRegister("vite-dev", func(name string, action manifest.Action, m *manifest.Manifest, surf *Surface) (Runner, error) {
		return func(args []string) error {
			*ran = true
			return nil
		}, nil
	})
	r.MustRegister("shell", makeShell)
	r.MustRegister("multi-target", makeMultiTarget)
	return r
}

// dashboardProducer builds a *CommandProducer whose Launch flips *launched
// rather than spawning a real hosted pty — the fixture every dashboard-leg
// test below shares.
func dashboardProducer(launched *bool) *CommandProducer {
	return &CommandProducer{
		Launch: func(m *manifest.Manifest, name string, args []string) error {
			*launched = true
			return nil
		},
	}
}

// TestRunCommandOpensPaneForLongRunningStep is the control half of the
// pair: a plain-CLI dispatch (producer nil, which CAN host the run itself)
// keeps its existing behavior — a declared [terminal] plus a detached step
// means a launcher dispatch, not an inline run.
func TestRunCommandOpensPaneForLongRunningStep(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	inlineRan := false
	if err := RunCommand(longRunningManifest(terminal), registryRecordingInline(&inlineRan), "dev", nil, nil); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if inlineRan {
		t.Error("RunCommand must hand a detached step to the launcher, not run it inline")
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading terminal log: %v", err)
	}
	if !strings.Contains(string(logged), "spawn") {
		t.Errorf("terminal log = %q, want a `spawn` call", string(logged))
	}
}

// TestRunCommandDashboardProducerUsesLauncherNotDefaultSurface is the
// acceptance criterion for a [terminal]-declaring project: a dashboard
// producer (which cannot host the run itself) must dispatch a detached
// first step through the launcher and must never call its own Launch
// fallback.
func TestRunCommandDashboardProducerUsesLauncherNotDefaultSurface(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	before := len(livework.Default.List())
	launched := false

	m := longRunningManifestNamed(terminal, "dashboard-dev-server")
	if err := RunCommand(m, registryRecordingInline(new(bool)), "dev", nil, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if launched {
		t.Error("a dashboard producer must not fall back to its own default surface when a launcher is declared")
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading terminal log: %v", err)
	}
	if !strings.Contains(string(logged), "spawn") {
		t.Errorf("terminal log = %q, want a `spawn` call", string(logged))
	}
	entries := livework.Default.List()
	found := false
	for _, e := range entries {
		// launchCommand keys the entry on the command's first step, per
		// StartHostedPTY's own convention, so `a` (attach) resolves the
		// same row regardless of which surface the command landed on.
		if e.ID == "dashboard-dev-server" && e.Handle == "fake-handle-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("live-work registry = %+v, want an entry keyed on the first step (dashboard-dev-server) with handle fake-handle-1", entries)
	}
	if got := len(entries); got != before+1 {
		t.Errorf("live-work registry grew from %d to %d, want exactly one new entry", before, got)
	}
}

// TestRunCommandDashboardProducerNoTerminalFallsBackToLaunch is the
// acceptance criterion for a project declaring no [terminal] at all: the
// dashboard producer's own Launch fallback runs, and the launcher's
// registry never grows — byte-for-byte today's no-launcher behaviour.
func TestRunCommandDashboardProducerNoTerminalFallsBackToLaunch(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())
	launched := false

	m := longRunningManifest(nil)
	if err := RunCommand(m, registryRecordingInline(new(bool)), "dev", nil, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if !launched {
		t.Error("a dashboard producer with no [terminal] declared must fall back to its own default surface")
	}
	if got := len(livework.Default.List()); got != before {
		t.Errorf("live-work registry grew from %d to %d — no launcher dispatch happened, so nothing should register", before, got)
	}
}

// multiTargetDashboardManifestNamed builds a manifest whose single command
// step is a kind="multi-target" action with one inline target "playtest" —
// the admin-ysxr.4 regression fixture: launchCommand must resolve the
// picked target's own `detach` override, not just the parent action's
// default, before deciding whether to reach the launcher or fall back to
// the producer's own default surface. parentPresent/parentVal control the
// action's own top-level `detach`; targetPresent/targetVal control the
// "playtest" target's own inline `detach`. step is parameterized for the
// same reason longRunningManifestNamed's is — livework.Default is a shared
// registry a completed launcher dispatch grows, so two tests reusing the
// same step name would collide.
func multiTargetDashboardManifestNamed(terminal map[string]any, step string, parentPresent, parentVal, targetPresent, targetVal bool) *manifest.Manifest {
	action := manifest.Action{"kind": "multi-target"}
	if parentPresent {
		action["detach"] = parentVal
	}
	target := map[string]any{"run": "true"}
	if targetPresent {
		target["detach"] = targetVal
	}
	action["playtest"] = target
	return &manifest.Manifest{
		Terminal: terminal,
		Custom: map[string]manifest.Command{
			"dev": {Steps: []string{step}},
		},
		Actions: map[string]manifest.Action{
			step: action,
		},
	}
}

// TestRunCommandDashboardProducerMultiTargetOverridesParentDetachFalse is
// the first of the two admin-ysxr.4 regression failures: a multi-target
// parent declares `detach = true` as its default, but the dispatched target
// ("playtest") explicitly opts out with `detach = false`. Before the fix,
// launchCommand read only the parent's own actionDetach and reached the
// launcher regardless — silently ignoring the target's explicit opt-out.
func TestRunCommandDashboardProducerMultiTargetOverridesParentDetachFalse(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	launched := false
	m := multiTargetDashboardManifestNamed(terminal, "dev-target-opts-out", true, true, true, false)
	if err := RunCommand(m, registryRecordingInline(new(bool)), "dev", []string{"playtest"}, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if !launched {
		t.Error("target detach=false must override the parent's detach=true default and run on the producer's own default surface")
	}
	logged, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading terminal log: %v", err)
	}
	if strings.Contains(string(logged), "spawn") {
		t.Errorf("terminal log = %q, want no `spawn` call — the target opted out of detach", string(logged))
	}
}

// TestRunCommandDashboardProducerMultiTargetOverridesParentDetachTrue is the
// inverse regression: no parent-level `detach` (falls back to false), but
// the dispatched target sets `detach = true`. Before the fix, launchCommand
// saw the parent's absent/false detach and fell through to p.Launch, never
// reaching the launcher the target asked for.
func TestRunCommandDashboardProducerMultiTargetOverridesParentDetachTrue(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	before := len(livework.Default.List())
	launched := false
	m := multiTargetDashboardManifestNamed(terminal, "dev-target-opts-in", false, false, true, true)
	if err := RunCommand(m, registryRecordingInline(new(bool)), "dev", []string{"playtest"}, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if launched {
		t.Error("target detach=true must override the parent's absent/false default and reach the launcher, not the producer's own default surface")
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading terminal log: %v", err)
	}
	if !strings.Contains(string(logged), "spawn") {
		t.Errorf("terminal log = %q, want a `spawn` call — the target opted into detach", string(logged))
	}
	entries := livework.Default.List()
	found := false
	for _, e := range entries {
		if e.ID == "dev-target-opts-in" && e.Handle == "fake-handle-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("live-work registry = %+v, want an entry keyed on the first step (dev-target-opts-in) with handle fake-handle-1", entries)
	}
	if got := len(entries); got != before+1 {
		t.Errorf("live-work registry grew from %d to %d, want exactly one new entry", before, got)
	}
}

// TestRunCommandDashboardProducerMultiTargetNoTargetArgFallsBackToLaunch is
// the case dispatchDetach cannot resolve at all: a multi-target command
// dispatched with no target argument. The picked target isn't known until
// the Runner itself runs (and raises a picker, or auto-picks a sole
// target), so launchCommand must not guess — it falls back to the
// producer's own default surface exactly as an absent `detach` would,
// however the parent's own default is set.
func TestRunCommandDashboardProducerMultiTargetNoTargetArgFallsBackToLaunch(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	launched := false
	m := multiTargetDashboardManifestNamed(terminal, "dev-no-target-arg", true, true, false, false)
	if err := RunCommand(m, registryRecordingInline(new(bool)), "dev", nil, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if !launched {
		t.Error("a multi-target command dispatched with no target argument must fall back to the producer's own default surface — the picked target isn't known up front")
	}
	logged, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading terminal log: %v", err)
	}
	if strings.Contains(string(logged), "spawn") {
		t.Errorf("terminal log = %q, want no `spawn` call — no target argument means the launcher decision can't be resolved up front", string(logged))
	}
}

// TestRunCommandInReentryRunsInlineRegardlessOfProducer is the ordering
// hazard the ticket asks for: a process already running inside a launched
// dispatch (ADMIN_REENTRY set, as cmd/root.go's two re-entry verbs do
// before dispatching) must run every step inline and must never reach a
// launcher — whatever producer is passed, nil or not.
func TestRunCommandInReentryRunsInlineRegardlessOfProducer(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	before := len(livework.Default.List())

	MarkReentry()
	t.Cleanup(func() { os.Unsetenv(ReentryEnv) })

	inlineRan := false
	launched := false
	if err := RunCommand(longRunningManifest(terminal), registryRecordingInline(&inlineRan), "dev", nil, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if !inlineRan {
		t.Error("a re-entered dispatch must run its step inline, in this process")
	}
	if launched {
		t.Error("a re-entered dispatch must never fall back to the producer's own default surface either — it runs inline")
	}
	if logged, err := os.ReadFile(logPath); err == nil && len(logged) > 0 {
		t.Errorf("terminal log = %q, want empty — a re-entered dispatch must never reach the launcher", string(logged))
	}
	if got := len(livework.Default.List()); got != before {
		t.Errorf("live-work registry grew from %d to %d — a re-entered run registers no launcher handle", before, got)
	}
}

// TestRunCommandInReentryNestedDelegationStaysInline closes the one
// call-graph hole a shared step loop alone would leave open: a multi-target
// target delegating with `action = "other"` reaches its callee through the
// Surface built for its own dispatch, not through the step loop. Under a
// re-entered dispatch that nested hop must stay inline too, or the launcher
// path is reachable one level down.
func TestRunCommandInReentryNestedDelegationStaysInline(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	m := &manifest.Manifest{
		Terminal: terminal,
		Custom: map[string]manifest.Command{
			"dev": {Steps: []string{"pick"}},
		},
		Actions: map[string]manifest.Action{
			"pick": {
				"kind":   "multi-target",
				"server": map[string]any{"action": "dev-server"},
			},
			"dev-server": {"kind": "vite-dev"},
		},
	}

	MarkReentry()
	t.Cleanup(func() { os.Unsetenv(ReentryEnv) })

	inlineRan := false
	if err := RunCommand(m, registryRecordingInline(&inlineRan), "dev", []string{"server"}, nil); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if !inlineRan {
		t.Error("a delegated target under a re-entered dispatch must run inline")
	}
	if logged, err := os.ReadFile(logPath); err == nil && len(logged) > 0 {
		t.Errorf("terminal log = %q, want empty — nested delegation must not reach the launcher either", string(logged))
	}
}

// TestRunCommandInReentryMultiTargetDetachStaysInline is the multi-target
// twin of the ordering-hazard test above: a multi-target TARGET itself
// (not a delegated action) carrying `detach = true`, reached from inside an
// already-launched run, must run inline rather than dispatching a second
// launcher call — this is the hole admin-ysxr.7 folds in fixing: before this
// ticket, a detached multi-target target reached from inside the
// dashboard's hosted pty child DID reach dispatchToLauncher, because
// runActionInline's Surface never gated makeMultiTarget's own detach branch.
func TestRunCommandInReentryMultiTargetDetachStaysInline(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	m := &manifest.Manifest{
		Terminal: terminal,
		Custom: map[string]manifest.Command{
			"dev": {Steps: []string{"pick"}},
		},
		Actions: map[string]manifest.Action{
			"pick": {
				"kind":   "multi-target",
				"server": map[string]any{"run": "touch ran.txt", "detach": true},
			},
		},
	}
	r := NewRegistry()
	r.MustRegister("shell", makeShell)
	r.MustRegister("multi-target", makeMultiTarget)

	MarkReentry()
	t.Cleanup(func() { os.Unsetenv(ReentryEnv) })

	if err := RunCommand(m, r, "dev", []string{"server"}, nil); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	assertFileExists(t, dir, "ran.txt")
	if logged, err := os.ReadFile(logPath); err == nil && len(logged) > 0 {
		t.Errorf("terminal log = %q, want empty — a detached target reached from inside an already-launched run must stay inline", string(logged))
	}
}

// TestRunCommandRunsStepsInOrderAndStops proves the collapsed entry point
// shares the existing step loop's semantics regardless of producer:
// declared order, stop-at-first-failure, and an error naming the command.
func TestRunCommandRunsStepsInOrderAndStops(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build": {Steps: []string{"one", "two", "three"}},
		},
		Actions: map[string]manifest.Action{
			"one":   {"kind": "shell", "run": "echo one >> order.txt"},
			"two":   {"kind": "shell", "run": "exit 3"},
			"three": {"kind": "shell", "run": "echo three >> order.txt"},
		},
	}
	err := RunCommand(m, newDispatchTestRegistry(), "build", nil, nil)
	if err == nil {
		t.Fatal("RunCommand = nil, want the failing middle step's error")
	}
	if !strings.Contains(err.Error(), `command "build"`) {
		t.Errorf("error = %v, want it to name the command", err)
	}
	got, readErr := os.ReadFile(filepath.Join(dir, "order.txt"))
	if readErr != nil {
		t.Fatalf("reading order.txt: %v", readErr)
	}
	if strings.TrimSpace(string(got)) != "one" {
		t.Errorf("order.txt = %q, want only %q — step three must never run", string(got), "one")
	}
}

// TestRunCommandDashboardProducerRejectsActionKeysOnCommand proves the
// dashboard-producer branch (launchCommand) carries the same ADR-0003
// pure-routing check, and the same unknown-command and empty-steps errors,
// as the plain-CLI branch — it resolves the command itself, up front, since
// it has no runSteps call on its own path to discover these otherwise.
func TestRunCommandDashboardProducerRejectsActionKeysOnCommand(t *testing.T) {
	t.Chdir(t.TempDir())

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"bad":   {Steps: []string{"one"}, Raw: map[string]any{"kind": "shell"}},
			"empty": {},
		},
		Actions: map[string]manifest.Action{"one": {"kind": "shell", "run": "true"}},
	}
	launched := false
	p := dashboardProducer(&launched)

	err := RunCommand(m, newDispatchTestRegistry(), "bad", nil, p)
	if err == nil || !strings.Contains(err.Error(), "ADR-0003") {
		t.Errorf("RunCommand(bad) = %v, want the ADR-0003 pure-routing error", err)
	}
	if err := RunCommand(m, newDispatchTestRegistry(), "empty", nil, p); err == nil {
		t.Error("RunCommand(empty) = nil, want a no-steps error")
	}
	if err := RunCommand(m, newDispatchTestRegistry(), "nope", nil, p); err == nil {
		t.Error("RunCommand(nope) = nil, want an unknown-command error")
	}
	if launched {
		t.Error("none of these should ever reach Launch — every one is rejected before dispatch")
	}
}
