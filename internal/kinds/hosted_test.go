// hosted_test.go covers the command-dispatch entry point: RunCommand takes a
// required *CommandProducer, and `locale` is dashboard-producer-only
// (restored by admin-wwrg, withdrawing admin-m6m8's producer-independent
// contract). The plain CLI dispatches through RunCommandInline instead,
// which always runs inline and never touches the project's declared
// [terminal] launcher, whatever the first step's `locale` says. A
// CommandProducer (the dashboard, which cannot host the run itself)
// resolves `locale`: it reaches the launcher for a detached first step and
// falls back to its own default surface (Launch) when no launcher dispatch
// happens. The tests below exercise both halves: the same manifest, the
// same fake [terminal] launcher, the same detached step, dispatched through
// RunCommandInline and through RunCommand with a dashboard producer.
package kinds

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// longRunningManifest is the shared fixture for the paired entry-point
// tests: one command whose single step is an action carrying `locale =
// "window"` (ADR-0020) plus a declared [terminal] table, so the routing
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
			step: {"kind": "vite-dev", "locale": "window"},
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

// TestRunCommandInlinePlainCLILocaleRunsInlineNeverReachesLauncher is the CLI
// half of the pair: a plain-CLI dispatch, which now always goes through
// RunCommandInline (admin-wwrg), with a declared [terminal] and a detached
// first step must run inline and never reach the launcher — `locale` is
// dashboard-producer-only.
func TestRunCommandInlinePlainCLILocaleRunsInlineNeverReachesLauncher(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	before := len(livework.Default.List())
	inlineRan := false
	if err := RunCommandInline(longRunningManifest(terminal), registryRecordingInline(&inlineRan), "dev", nil); err != nil {
		t.Fatalf("RunCommandInline = %v, want nil", err)
	}
	if !inlineRan {
		t.Error("RunCommandInline must run inline, even with a declared [terminal] and locale=window")
	}
	logged, err := os.ReadFile(logPath)
	if err == nil && len(logged) > 0 {
		t.Errorf("terminal log = %q, want empty — RunCommandInline must never reach the launcher", string(logged))
	}
	if got := len(livework.Default.List()); got != before {
		t.Errorf("live-work registry grew from %d to %d — RunCommandInline must never register a launcher entry", before, got)
	}
}

// TestRunCommandDashboardProducerUsesLauncherNotDefaultSurface is the
// acceptance criterion for a [terminal]-declaring project: a dashboard
// producer (which cannot host the run itself) must dispatch a detached
// first step through the launcher and must never call its own Launch
// fallback.
func TestRunCommandDashboardProducerUsesLauncherNotDefaultSurface(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
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
		if e.ID == "dev" && e.Handle == "fake-handle-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("live-work registry = %+v, want an entry keyed on the command (dev) with handle fake-handle-1", entries)
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
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
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
// picked target's own `locale` override, not just the parent action's
// default, before deciding whether to reach the launcher or fall back to
// the producer's own default surface. parentPresent/parentVal control the
// action's own top-level `locale`; targetPresent/targetVal control the
// "playtest" target's own inline `locale`. step is parameterized for the
// same reason longRunningManifestNamed's is — livework.Default is a shared
// registry a completed launcher dispatch grows, so two tests reusing the
// same step name would collide.
func multiTargetDashboardManifestNamed(terminal map[string]any, step string, parentPresent, parentVal, targetPresent, targetVal bool) *manifest.Manifest {
	action := manifest.Action{"kind": "multi-target"}
	if parentPresent {
		action["locale"] = localeOf(parentVal)
	}
	target := map[string]any{"run": "true"}
	if targetPresent {
		target["locale"] = localeOf(targetVal)
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

// TestRunCommandDashboardProducerMultiTargetOverridesParentLocaleHosted is
// the first of the two admin-ysxr.4 regression failures: a multi-target
// parent declares `locale = "window"` as its default, but the dispatched target
// ("playtest") explicitly opts out with `locale = "hosted"`. Before the fix,
// launchCommand read only the parent's own actionLocale and reached the
// launcher regardless — silently ignoring the target's explicit opt-out.
func TestRunCommandDashboardProducerMultiTargetOverridesParentLocaleHosted(t *testing.T) {
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
		t.Error("target locale=hosted must override the parent's locale=window default and run on the producer's own default surface")
	}
	logged, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading terminal log: %v", err)
	}
	if strings.Contains(string(logged), "spawn") {
		t.Errorf("terminal log = %q, want no `spawn` call — the target opted out of locale", string(logged))
	}
}

// TestRunCommandDashboardProducerMultiTargetOverridesParentLocaleWindow is the
// inverse regression: no parent-level `locale` (falls back to false), but
// the dispatched target sets `locale = "window"`. Before the fix, launchCommand
// saw the parent's absent/false locale and fell through to p.Launch, never
// reaching the launcher the target asked for.
func TestRunCommandDashboardProducerMultiTargetOverridesParentLocaleWindow(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
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
		t.Error("target locale=window must override the parent's absent/false default and reach the launcher, not the producer's own default surface")
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading terminal log: %v", err)
	}
	if !strings.Contains(string(logged), "spawn") {
		t.Errorf("terminal log = %q, want a `spawn` call — the target opted into locale", string(logged))
	}
	entries := livework.Default.List()
	found := false
	for _, e := range entries {
		if e.ID == "dev" && e.Handle == "fake-handle-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("live-work registry = %+v, want an entry keyed on the command (dev) with handle fake-handle-1", entries)
	}
	if got := len(entries); got != before+1 {
		t.Errorf("live-work registry grew from %d to %d, want exactly one new entry", before, got)
	}
}

// inlineTargetSpec describes one inline target subtable for
// multiTargetManifestInline: name is the target key, present/val control
// whether that target declares its own `locale` override.
type inlineTargetSpec struct {
	name           string
	present, value bool
}

// multiTargetManifestInline builds a kind="multi-target" action with N
// inline target subtables (admin-ysxr.4 round 3: dispatchLocale's
// sole-target auto-pick, mirroring TargetKeys/makeMultiTarget's own
// `order`, needs fixtures with exactly one AND with more than one inline
// target — multiTargetDashboardManifestNamed above is pinned to exactly
// one target named "playtest" and stays untouched for round 2's tests, so
// this is a sibling rather than a rewrite). Each target's run command is
// always "true"; parentPresent/parentVal set the parent action's own
// top-level `locale`. step is parameterized for the same livework.Default
// collision reason every fixture here documents.
func multiTargetManifestInline(terminal map[string]any, step string, parentPresent, parentVal bool, targets ...inlineTargetSpec) *manifest.Manifest {
	action := manifest.Action{"kind": "multi-target"}
	if parentPresent {
		action["locale"] = localeOf(parentVal)
	}
	for _, ts := range targets {
		tbl := map[string]any{"run": "true"}
		if ts.present {
			tbl["locale"] = localeOf(ts.value)
		}
		action[ts.name] = tbl
	}
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

// multiTargetManifestFlat builds a kind="multi-target" action whose targets
// live in a flat `targets` table (t1..tN, each "true" as a shell command)
// rather than inline subtables — the other shape dispatchLocale's
// sole-target auto-pick must resolve (admin-ysxr.4 round 3): TargetKeys
// enumerates a flat table exactly like an inline one when there are no
// inline subtables to prefer, so a single-entry flat table auto-picks the
// same way. A flat entry can never carry its own `locale` override (only
// an inline subtable can, per targetLocaleOf), so there is no per-target
// override to parameterize here — only the count and the parent default.
func multiTargetManifestFlat(terminal map[string]any, step string, parentPresent, parentVal bool, n int) *manifest.Manifest {
	action := manifest.Action{"kind": "multi-target"}
	if parentPresent {
		action["locale"] = localeOf(parentVal)
	}
	flat := map[string]any{}
	for i := 1; i <= n; i++ {
		flat[fmt.Sprintf("t%d", i)] = "true"
	}
	action["targets"] = flat
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

// TestRunCommandDashboardProducerMultiTargetNoTargetArgFallsBackToLaunch is
// the case dispatchLocale still cannot resolve: a multi-target command with
// MORE THAN ONE target, dispatched with no target argument. The picked
// target isn't known until the Runner itself runs and raises a picker, so
// launchCommand must not guess — it falls back to the producer's own
// default surface exactly as an absent `locale` would, however the
// parent's own default is set. (Round 3: this fixture used to build only
// ONE inline target, which is the shape the Runner's own auto-pick DOES
// resolve deterministically — see
// TestRunCommandDashboardProducerMultiTargetSoleInlineTargetAutoPicksLauncher
// below. Re-pointed at two targets so this test pins the genuinely
// unresolvable shape it was written to test; assertions and intent are
// unchanged.)
func TestRunCommandDashboardProducerMultiTargetNoTargetArgFallsBackToLaunch(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	launched := false
	m := multiTargetManifestInline(terminal, "dev-no-target-arg", true, true,
		inlineTargetSpec{name: "playtest"}, inlineTargetSpec{name: "record"})
	if err := RunCommand(m, registryRecordingInline(new(bool)), "dev", nil, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if !launched {
		t.Error("a multi-target command with more than one target, dispatched with no target argument, must fall back to the producer's own default surface — the picked target isn't known up front")
	}
	logged, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading terminal log: %v", err)
	}
	if strings.Contains(string(logged), "spawn") {
		t.Errorf("terminal log = %q, want no `spawn` call — no target argument means the launcher decision can't be resolved up front", string(logged))
	}
}

// TestRunCommandDashboardProducerMultiTargetSoleInlineTargetAutoPicksLauncher
// is admin-ysxr.4 round 3's actual defect: a multi-target action declaring
// exactly ONE inline target is auto-picked by the Runner itself
// (multitarget.go's `if len(order) == 1` branch) even with no target
// argument, so its effective locale IS knowable up front. Before the fix,
// dispatchLocale treated every empty-picked dispatch as unresolvable and
// fell through to p.Launch regardless.
func TestRunCommandDashboardProducerMultiTargetSoleInlineTargetAutoPicksLauncher(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	before := len(livework.Default.List())
	launched := false
	m := multiTargetManifestInline(terminal, "dev-sole-inline-target", true, true,
		inlineTargetSpec{name: "only"})
	if err := RunCommand(m, registryRecordingInline(new(bool)), "dev", nil, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if launched {
		t.Error("a sole inline target is auto-picked by the Runner and must reach the launcher, not the producer's own default surface")
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading terminal log: %v", err)
	}
	if !strings.Contains(string(logged), "spawn") {
		t.Errorf("terminal log = %q, want a `spawn` call — the sole target's locale is knowable up front", string(logged))
	}
	entries := livework.Default.List()
	found := false
	for _, e := range entries {
		if e.ID == "dev" && e.Handle == "fake-handle-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("live-work registry = %+v, want an entry keyed on the command (dev) with handle fake-handle-1", entries)
	}
	if got := len(entries); got != before+1 {
		t.Errorf("live-work registry grew from %d to %d, want exactly one new entry", before, got)
	}
}

// TestRunCommandDashboardProducerMultiTargetSoleInlineTargetOverrideFalseFallsBackToLaunch
// is the auto-pick's target-override half: the sole target explicitly opts
// out with `locale = "hosted"`, overriding the parent's `locale = "window"`
// default — the auto-pick must still honour that override, not blanket-
// locale because there is only one target to choose from.
func TestRunCommandDashboardProducerMultiTargetSoleInlineTargetOverrideFalseFallsBackToLaunch(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	launched := false
	m := multiTargetManifestInline(terminal, "dev-sole-inline-target-opts-out", true, true,
		inlineTargetSpec{name: "only", present: true, value: false})
	if err := RunCommand(m, registryRecordingInline(new(bool)), "dev", nil, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if !launched {
		t.Error("the sole target's own locale=hosted must override the parent's locale=window default even under auto-pick")
	}
	logged, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading terminal log: %v", err)
	}
	if strings.Contains(string(logged), "spawn") {
		t.Errorf("terminal log = %q, want no `spawn` call — the sole target opted out of locale", string(logged))
	}
}

// TestRunCommandDashboardProducerMultiTargetFlatSingleTargetAutoPicksLauncher
// is the flat-`targets`-table twin of the inline auto-pick test: TargetKeys
// enumerates a flat table's keys exactly like an inline table's when there
// are no inline subtables, so a single-entry flat table is resolvable the
// same way and inherits the parent's `locale` (a flat entry can never
// declare its own override).
func TestRunCommandDashboardProducerMultiTargetFlatSingleTargetAutoPicksLauncher(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	before := len(livework.Default.List())
	launched := false
	m := multiTargetManifestFlat(terminal, "dev-flat-sole-target", true, true, 1)
	if err := RunCommand(m, registryRecordingInline(new(bool)), "dev", nil, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if launched {
		t.Error("a flat `targets` table with exactly one entry is auto-picked by the Runner and must reach the launcher, not the producer's own default surface")
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading terminal log: %v", err)
	}
	if !strings.Contains(string(logged), "spawn") {
		t.Errorf("terminal log = %q, want a `spawn` call — the sole flat target's locale is knowable up front", string(logged))
	}
	entries := livework.Default.List()
	found := false
	for _, e := range entries {
		if e.ID == "dev" && e.Handle == "fake-handle-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("live-work registry = %+v, want an entry keyed on the command (dev) with handle fake-handle-1", entries)
	}
	if got := len(entries); got != before+1 {
		t.Errorf("live-work registry grew from %d to %d, want exactly one new entry", before, got)
	}
}

// TestRunCommandDashboardProducerMultiTargetFlatTwoTargetsFallsBackToLaunch
// is the flat-table twin of the two-target unresolvable case: with more
// than one entry the Runner itself must raise the picker, so dispatchLocale
// stays unresolvable and launchCommand falls back to p.Launch.
func TestRunCommandDashboardProducerMultiTargetFlatTwoTargetsFallsBackToLaunch(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	launched := false
	m := multiTargetManifestFlat(terminal, "dev-flat-two-targets", true, true, 2)
	if err := RunCommand(m, registryRecordingInline(new(bool)), "dev", nil, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if !launched {
		t.Error("a flat `targets` table with more than one entry, dispatched with no target argument, must fall back to the producer's own default surface")
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
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
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

// TestRunCommandInReentryMultiTargetLocaleStaysInline is the multi-target
// twin of the ordering-hazard test above: a multi-target TARGET itself
// (not a delegated action) carrying `locale = "window"` must run inline once
// already inReentry — makeMultiTarget's Runner has no launcher branch of its
// own at all, so a target's `locale` is only ever consulted once, up front,
// by launchCommand, and inReentry short-circuits RunCommand before that.
func TestRunCommandInReentryMultiTargetLocaleStaysInline(t *testing.T) {
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
				"server": map[string]any{"run": "touch ran.txt", "locale": "window"},
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

// TestRunCommandInlineRunsStepsInOrderAndStops proves RunCommandInline
// shares the existing step loop's semantics: declared order,
// stop-at-first-failure, and an error naming the command.
func TestRunCommandInlineRunsStepsInOrderAndStops(t *testing.T) {
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
	err := RunCommandInline(m, newDispatchTestRegistry(), "build", nil)
	if err == nil {
		t.Fatal("RunCommandInline = nil, want the failing middle step's error")
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

// TestLauncherSpawnReceivesProjectRootNotInvocationDir guards ADMIN_CWD: a
// detached command dispatched while the process cwd sits below the project
// root (admin found its manifest by walking up) must hand the launcher the
// root, per ADR-0020, never the subdirectory admin was typed in.
func TestLauncherSpawnReceivesProjectRootNotInvocationDir(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	root := t.TempDir()
	deeper := filepath.Join(root, "sub", "deeper")
	if err := os.MkdirAll(deeper, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(deeper)
	cwdPath := filepath.Join(t.TempDir(), "admin-cwd")
	terminal := map[string]any{
		"spawn":  fmt.Sprintf(`printf "%%s" "$ADMIN_CWD" > %s; echo fake-handle-cwd`, shellQuote(cwdPath)),
		"attach": "true",
		"kill":   "true",
	}

	m := longRunningManifestNamed(terminal, "cwd-dev-server")
	m.Root = root
	if err := RunCommand(m, registryRecordingInline(new(bool)), "dev", nil, dashboardProducer(new(bool))); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	got, err := os.ReadFile(cwdPath)
	if err != nil {
		t.Fatalf("reading recorded ADMIN_CWD: %v", err)
	}
	if string(got) != root {
		t.Errorf("ADMIN_CWD = %q, want the project root %q (process cwd was %q)", got, root, deeper)
	}
}
