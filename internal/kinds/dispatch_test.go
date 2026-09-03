package kinds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func newDispatchTestRegistry() *Registry {
	r := NewRegistry()
	r.MustRegister("shell", makeShell)
	r.MustRegister("multi-target", makeMultiTarget)
	return r
}

// TestRunCommandRunsStepsInOrder proves "a command with several steps runs
// them in order and stops at the first failure": three steps append to the
// same file, and all three markers must exist in the right order.
func TestRunCommandRunsStepsInOrder(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build": {Steps: []string{"one", "two", "three"}},
		},
		Actions: map[string]manifest.Action{
			"one":   {"kind": "shell", "run": "echo one >> order.txt"},
			"two":   {"kind": "shell", "run": "echo two >> order.txt"},
			"three": {"kind": "shell", "run": "echo three >> order.txt"},
		},
	}
	if err := RunCommand(m, newDispatchTestRegistry(), "build", nil, nil); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	assertFileExists(t, dir, "order.txt")
	got := readFile(t, dir, "order.txt")
	if got != "one\ntwo\nthree\n" {
		t.Fatalf("order.txt = %q, want steps run in declared order", got)
	}
}

// TestRunCommandStopsAtFirstFailure proves the second half of the same
// acceptance criterion: a failing step prevents any step after it from
// running.
//
// MUTATION EVIDENCE: remove RunCommand's `if err := RunAction(...); err !=
// nil { return ... }` early-return (loop through every step regardless of
// error), and this test fails — after.txt exists even though the middle
// step failed, because the loop kept going past it.
func TestRunCommandStopsAtFirstFailure(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build": {Steps: []string{"before", "boom", "after"}},
		},
		Actions: map[string]manifest.Action{
			"before": {"kind": "shell", "run": "touch before.txt"},
			"boom":   {"kind": "shell", "run": "exit 1"},
			"after":  {"kind": "shell", "run": "touch after.txt"},
		},
	}
	if err := RunCommand(m, newDispatchTestRegistry(), "build", nil, nil); err == nil {
		t.Fatal("RunCommand = nil, want an error from the failing middle step")
	}
	assertFileExists(t, dir, "before.txt")
	assertFileMissing(t, dir, "after.txt")
}

// TestRunCommandRejectsKindOnCommand is the acceptance criterion "a command
// carrying kind or run is rejected with an error naming the rule" —
// ADR-0003.
func TestRunCommandRejectsKindOnCommand(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"bad": {Steps: []string{"noop"}, Raw: map[string]any{"kind": "shell"}},
		},
		Actions: map[string]manifest.Action{"noop": {"kind": "shell", "run": "true"}},
	}
	err := RunCommand(m, newDispatchTestRegistry(), "bad", nil, nil)
	if err == nil {
		t.Fatal("RunCommand should reject a command carrying 'kind'")
	}
	if !strings.Contains(err.Error(), "ADR-0003") || !strings.Contains(err.Error(), "kind") {
		t.Errorf("error %q should name ADR-0003 and the offending key", err.Error())
	}
}

// TestRunCommandRejectsRunOnCommand mirrors the previous test for `run`.
func TestRunCommandRejectsRunOnCommand(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"bad": {Steps: []string{"noop"}, Raw: map[string]any{"run": "echo hi"}},
		},
		Actions: map[string]manifest.Action{"noop": {"kind": "shell", "run": "true"}},
	}
	if err := RunCommand(m, newDispatchTestRegistry(), "bad", nil, nil); err == nil {
		t.Fatal("RunCommand should reject a command carrying 'run'")
	}
}

// TestRunCommandUnknownCommandIsError proves a missing command name errors
// clearly rather than panicking.
func TestRunCommandUnknownCommandIsError(t *testing.T) {
	m := &manifest.Manifest{Custom: map[string]manifest.Command{}}
	if err := RunCommand(m, newDispatchTestRegistry(), "nope", nil, nil); err == nil {
		t.Fatal("RunCommand for an unknown command should error")
	}
}

// TestRunCommandCustomShadowsOverride proves resolveCommand's "custom
// shadows archetype-provided" rule: a name present in both Custom and
// Overrides resolves to the Custom one.
func TestRunCommandCustomShadowsOverride(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := &manifest.Manifest{
		Overrides: map[string]manifest.Command{
			"build": {Steps: []string{"from-archetype"}},
		},
		Custom: map[string]manifest.Command{
			"build": {Steps: []string{"from-custom"}},
		},
		Actions: map[string]manifest.Action{
			"from-archetype": {"kind": "shell", "run": "touch archetype.txt"},
			"from-custom":    {"kind": "shell", "run": "touch custom.txt"},
		},
	}
	if err := RunCommand(m, newDispatchTestRegistry(), "build", nil, nil); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	assertFileExists(t, dir, "custom.txt")
	assertFileMissing(t, dir, "archetype.txt")
}

// TestRunActionDefaultsToShellKind proves an action with no `kind` key
// defaults to "shell", matching admin_lib/validate.py's
// `data.get("kind", "shell")`.
func TestRunActionDefaultsToShellKind(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{"implicit": {"run": "touch implicit.txt"}},
	}
	if err := RunAction(m, newDispatchTestRegistry(), "implicit", nil); err != nil {
		t.Fatalf("RunAction = %v, want nil", err)
	}
	assertFileExists(t, dir, "implicit.txt")
}

// TestRunCommandNamesLogUnconditionally is admin-rc82's central acceptance
// criterion: a command whose step bodies never call set_log_context
// themselves still produces exactly one log file, under
// <LOG_DIR>/<commandName>.<timestamp>.log, carrying every step's output in
// chronological order — and the path luahost announces (captured off
// os.Stderr, since that's the only channel a hosted-pty child could use
// too) is the exact path that ends up on disk.
func TestRunCommandNamesLogUnconditionally(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// The process log context is shared across every test in this
	// binary and Configure no longer clears it (admin-rc82.1) — reset it
	// so this test's assertions see only its own run.
	luahost.ResetProcessLogContext(dir)

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build": {Steps: []string{"one", "two"}},
		},
		Actions: map[string]manifest.Action{
			// No `urls`, so kind=shell never calls SetLogContext itself —
			// the command-level name from nameCommandLog must be the one
			// that survives to materialization.
			"one": {"kind": "shell", "run": "echo step-one-marker"},
			"two": {"kind": "shell", "run": "echo step-two-marker"},
		},
	}

	announced := captureStderr(t, func() {
		if err := RunCommand(m, newDispatchTestRegistry(), "build", nil, nil); err != nil {
			t.Fatalf("RunCommand = %v, want nil", err)
		}
	})

	matches, err := filepath.Glob(filepath.Join(dir, "tmp", "build.*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 log file, got %d: %v", len(matches), matches)
	}

	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	content := string(data)
	firstIdx := strings.Index(content, "step-one-marker")
	secondIdx := strings.Index(content, "step-two-marker")
	if firstIdx < 0 || secondIdx < 0 {
		t.Fatalf("log file missing step markers: %q", content)
	}
	if firstIdx > secondIdx {
		t.Fatalf("log file has steps out of order: %q", content)
	}

	announcedPath, ok := luahost.ParseLogAnnounce(announced)
	if !ok {
		t.Fatalf("expected an announced log path on stderr, got %q", announced)
	}
	if announcedPath != matches[0] {
		t.Fatalf("announced path %q != stat-able path %q", announcedPath, matches[0])
	}
}

// TestRunCommandLogFalseWritesNoFile proves `[actions.X]`-level `log =
// false` on the command disables logging for the run: no file should ever
// appear under LOG_DIR.
func TestRunCommandLogFalseWritesNoFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"quiet": {Steps: []string{"one"}, Log: false, HasLog: true},
		},
		Actions: map[string]manifest.Action{
			"one": {"kind": "shell", "run": "echo hi"},
		},
	}
	if err := RunCommand(m, newDispatchTestRegistry(), "quiet", nil, nil); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "tmp", "*.log"))
	if len(matches) != 0 {
		t.Fatalf("expected no log files with log=false, got: %v", matches)
	}
}

// TestRunCommandLogFileUsesExplicitPath proves `log_file` routes through
// SetLogContextPath rather than the timestamped stem naming.
func TestRunCommandLogFileUsesExplicitPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// The process log context is shared across every test in this
	// binary and Configure no longer clears it (admin-rc82.1) — reset it
	// so this test's assertions see only its own run.
	luahost.ResetProcessLogContext(dir)

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build": {Steps: []string{"one"}, LogFile: "custom.log", HasLogFile: true},
		},
		Actions: map[string]manifest.Action{
			"one": {"kind": "shell", "run": "echo hi"},
		},
	}
	if err := RunCommand(m, newDispatchTestRegistry(), "build", nil, nil); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	assertFileExists(t, dir, "custom.log")
	if got := readFile(t, dir, "custom.log"); !strings.Contains(got, "hi") {
		t.Fatalf("custom.log missing expected content: %q", got)
	}
}

// TestRunCommandHonoursLoggingDirConfig proves `[logging] dir` re-roots
// where a stem-named log lands.
func TestRunCommandHonoursLoggingDirConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// The process log context is shared across every test in this
	// binary and Configure no longer clears it (admin-rc82.1) — reset it
	// so this test's assertions see only its own run.
	luahost.ResetProcessLogContext(dir)

	m := &manifest.Manifest{
		Logging: map[string]any{"dir": "customlogs"},
		Custom: map[string]manifest.Command{
			"build": {Steps: []string{"one"}},
		},
		Actions: map[string]manifest.Action{
			"one": {"kind": "shell", "run": "echo hi"},
		},
	}
	if err := RunCommand(m, newDispatchTestRegistry(), "build", nil, nil); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "customlogs", "build.*.log"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 log file under customlogs/, got %d", len(matches))
	}
	defaultMatches, _ := filepath.Glob(filepath.Join(dir, "tmp", "*.log"))
	if len(defaultMatches) != 0 {
		t.Fatalf("expected nothing written to the default tmp/ dir, got: %v", defaultMatches)
	}
}

// captureStderr (used above) is defined in testhelpers_test.go — this
// package already has one, and it does exactly what the announce-path
// assertion needs: redirect os.Stderr's current *os.File for fn's duration
// and return what landed on it.

func readFile(t *testing.T, dir, relPath string) string {
	t.Helper()
	data := mustReadFile(t, dir, relPath)
	return string(data)
}

// blockingFactory returns a Factory whose Runner signals reached, blocks
// until release is closed, then delegates to nestedName on its OWN surf —
// the shape admin-3rgp.69 depends on: the surf a Runner closes over must be
// the one built for ITS OWN dispatch, never whatever another goroutine's
// RunAction call happened to leave behind in shared state. Registered under
// "http" purely because that kind name is declared in the catalog and
// otherwise unused by these tests; the factory ignores the action entirely.
func blockingFactory(reached chan<- struct{}, release <-chan struct{}, nestedName string) Factory {
	return func(name string, action manifest.Action, m *manifest.Manifest, surf *Surface) (Runner, error) {
		return func(args []string) error {
			reached <- struct{}{}
			<-release
			return surf.Action(nestedName, nil)
		}, nil
	}
}

// TestConcurrentRunActionsDoNotClobberSurfaces is this ticket's central
// acceptance criterion: two RunAction calls running concurrently in one
// process each resolve nested delegation against their own manifest and
// registry. Before this ticket, dispatch.go parked the nested-dispatch
// surface in a package-level `currentSurface` var: goroutine A's RunAction
// call sets it, blocks mid-run (mirroring a hosted dashboard's overlapping
// commands, admin-3rgp.61), and goroutine B's RunAction call — started while
// A is still blocked — overwrites the same var for its own dispatch. When A
// is finally released and calls its own nested delegation, it would read
// whatever B (still mid-run, not yet restored) last set, and run B's
// "finish" action against B's manifest instead of its own.
//
// MUTATION EVIDENCE: reintroduce the package-level currentSurface var and
// have runAction read/write it instead of building and passing a *Surface
// per call, and this test fails — a.txt is missing (or b.txt exists early)
// because A's nested delegation resolved against manifest B's "finish"
// action while B was still mid-run holding the shared var.
func TestConcurrentRunActionsDoNotClobberSurfaces(t *testing.T) {
	dir := t.TempDir()
	markerA := filepath.Join(dir, "a.txt")
	markerB := filepath.Join(dir, "b.txt")

	reachedA, releaseA := make(chan struct{}), make(chan struct{})
	reachedB, releaseB := make(chan struct{}), make(chan struct{})

	rA := NewRegistry()
	rA.MustRegister("http", blockingFactory(reachedA, releaseA, "finish"))
	rA.MustRegister("shell", makeShell)
	mA := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"dev":    {"kind": "http"},
			"finish": {"kind": "shell", "run": "touch " + markerA},
		},
	}

	rB := NewRegistry()
	rB.MustRegister("http", blockingFactory(reachedB, releaseB, "finish"))
	rB.MustRegister("shell", makeShell)
	mB := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"dev":    {"kind": "http"},
			"finish": {"kind": "shell", "run": "touch " + markerB},
		},
	}

	errA := make(chan error, 1)
	errB := make(chan error, 1)
	go func() { errA <- RunAction(mA, rA, "dev", nil) }()
	<-reachedA // A is now blocked mid-run, holding surfA.

	go func() { errB <- RunAction(mB, rB, "dev", nil) }()
	<-reachedB // B is now ALSO blocked mid-run, holding surfB — both overlap.

	// Release A first while B is still mid-run: with the old package-level
	// var this is exactly the window where A's nested delegation would read
	// B's surface instead of its own.
	close(releaseA)
	if err := <-errA; err != nil {
		t.Fatalf("RunAction(mA) = %v, want nil", err)
	}
	assertFileExists(t, dir, "a.txt")
	assertFileMissing(t, dir, "b.txt")

	close(releaseB)
	if err := <-errB; err != nil {
		t.Fatalf("RunAction(mB) = %v, want nil", err)
	}
	assertFileExists(t, dir, "b.txt")
}

// TestConcurrentMultiTargetDelegationResolvesOwnManifest is the multi-target
// half of the same acceptance criterion: a target's `action = "other"`
// delegation (multitarget.go:299's `surf.action(...)` call) resolves against
// its own command's manifest while a second, unrelated RunAction dispatch is
// still mid-run in the same process.
func TestConcurrentMultiTargetDelegationResolvesOwnManifest(t *testing.T) {
	dir := t.TempDir()
	markerA := filepath.Join(dir, "a.txt")
	markerB := filepath.Join(dir, "b.txt")

	reachedA, releaseA := make(chan struct{}), make(chan struct{})

	// A: an unrelated dispatch that blocks mid-run and never touches B's
	// manifest or registry at all.
	rA := NewRegistry()
	rA.MustRegister("http", func(name string, action manifest.Action, m *manifest.Manifest, surf *Surface) (Runner, error) {
		return func(args []string) error {
			reachedA <- struct{}{}
			<-releaseA
			return os.WriteFile(markerA, []byte("a\n"), 0o644)
		}, nil
	})
	mA := &manifest.Manifest{Actions: map[string]manifest.Action{"dev": {"kind": "http"}}}

	// B: a multi-target action whose single target delegates to another
	// declared action via `action = "other"`.
	rB := NewRegistry()
	rB.MustRegister("multi-target", makeMultiTarget)
	rB.MustRegister("shell", makeShell)
	mB := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"dev": {
				"kind": "multi-target",
				"only": map[string]any{"action": "finish"},
			},
			"finish": {"kind": "shell", "run": "touch " + markerB},
		},
	}

	errA := make(chan error, 1)
	go func() { errA <- RunAction(mA, rA, "dev", nil) }()
	<-reachedA // A is mid-run and stays there for the rest of this test.

	if err := RunAction(mB, rB, "dev", nil); err != nil {
		t.Fatalf("RunAction(mB) = %v, want nil", err)
	}
	assertFileExists(t, dir, "b.txt")

	select {
	case err := <-errA:
		t.Fatalf("RunAction(mA) finished (err=%v) before release — it should still be mid-run", err)
	default:
	}

	close(releaseA)
	if err := <-errA; err != nil {
		t.Fatalf("RunAction(mA) = %v, want nil", err)
	}
	assertFileExists(t, dir, "a.txt")
}

// TestNilSurfaceReturnsUnchangedDispatchError proves a factory's Runner
// invoked directly, outside any dispatch — the case where surf is nil — still
// returns the pre-existing error text naming kinds.RunAction/RunCommand, on
// both the action and command halves of Surface.
func TestNilSurfaceReturnsUnchangedDispatchError(t *testing.T) {
	var s *Surface

	err := s.Action("some-action", nil)
	if err == nil {
		t.Fatal("(*Surface)(nil).Action should error")
	}
	want := `action "some-action": dispatching a nested action requires running through ` +
		`kinds.RunAction/RunCommand rather than invoking a factory's Runner directly`
	if err.Error() != want {
		t.Errorf("Action error = %q, want %q", err.Error(), want)
	}

	err = s.Command("some-command", nil)
	if err == nil {
		t.Fatal("(*Surface)(nil).Command should error")
	}
	wantCmd := `command "some-command": dispatching a nested command requires running through ` +
		`kinds.RunAction/RunCommand rather than invoking a factory's Runner directly`
	if err.Error() != wantCmd {
		t.Errorf("Command error = %q, want %q", err.Error(), wantCmd)
	}
}

// makePreHookTestKind is a test kind wrapping runPreHook directly — the
// function docker.go/cleandev.go/vitedev.go each call with their config
// table's `pre_kill` string (admin-tlhw.3). action["hook_name"] is what
// gets passed as the declared reference, so a test can exercise every shape:
// empty (no hook declared), a name resolving to a command, one resolving
// only to an action, and one resolving to neither.
func makePreHookTestKind(_ string, action manifest.Action, m *manifest.Manifest, surf *Surface) (Runner, error) {
	name, _ := action["hook_name"].(string)
	return func(args []string) error {
		runPreHook(m, surf, name, args, func(msg string) {
			luahost.Process().Write("warn: " + msg + "\n")
		})
		luahost.Process().Write("ran\n")
		return nil
	}, nil
}

func newPreHookTestRegistry() *Registry {
	r := NewRegistry()
	r.MustRegister("shell", makeShell)
	r.MustRegister("multi-target", makePreHookTestKind)
	return r
}

// TestRunPreHookEmptyNameIsNoop proves "a project that declares neither
// behaves correctly: no pre-run hook fires" — an empty `pre_kill` (the
// unset case) never touches the Surface at all, so it works even with a nil
// Surface, exactly as makeCleanDev's own tests already rely on.
func TestRunPreHookEmptyNameIsNoop(t *testing.T) {
	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"probe": {"kind": "multi-target", "hook_name": ""},
		},
	}
	if err := RunAction(m, newPreHookTestRegistry(), "probe", nil); err != nil {
		t.Fatalf("RunAction = %v, want nil", err)
	}
}

// TestRunPreHookDispatchesADeclaredCommand proves a `pre_kill` naming a
// project command dispatches it via surf.Command — the same hop the three
// shipped dev kinds take, now driven by a project-chosen name instead of
// the literal "kill".
func TestRunPreHookDispatchesADeclaredCommand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"stop-stale": {Steps: []string{"stop-step"}},
			"probe-run":  {Steps: []string{"probe"}},
		},
		Actions: map[string]manifest.Action{
			"probe":     {"kind": "multi-target", "hook_name": "stop-stale"},
			"stop-step": {"kind": "shell", "run": "touch hooked.txt"},
		},
	}
	if err := RunCommand(m, newPreHookTestRegistry(), "probe-run", nil, nil); err != nil {
		t.Fatalf("RunCommand(probe-run) = %v, want nil", err)
	}
	assertFileExists(t, dir, "hooked.txt")
}

// TestRunPreHookDispatchesADeclaredAction proves the second declared shape:
// `pre_kill` naming a bare [actions.X] with no wrapping command still runs,
// via RunAction rather than surf.Command.
func TestRunPreHookDispatchesADeclaredAction(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"probe-run": {Steps: []string{"probe"}},
		},
		Actions: map[string]manifest.Action{
			"probe":             {"kind": "multi-target", "hook_name": "stop-stale-action"},
			"stop-stale-action": {"kind": "shell", "run": "touch hooked.txt"},
		},
	}
	if err := RunCommand(m, newPreHookTestRegistry(), "probe-run", nil, nil); err != nil {
		t.Fatalf("RunCommand(probe-run) = %v, want nil", err)
	}
	assertFileExists(t, dir, "hooked.txt")
}

// TestRunPreHookUnresolvedNameIsNoop proves a `pre_kill` naming neither a
// command nor an action does not error at dispatch time — admin check's
// checkPreRunRefs (cmd/check.go) is what reports that, not this path, which
// must degrade to a no-op rather than guess or fail the whole dev loop.
func TestRunPreHookUnresolvedNameIsNoop(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"probe-run": {Steps: []string{"probe"}},
		},
		Actions: map[string]manifest.Action{
			"probe": {"kind": "multi-target", "hook_name": "no-such-thing"},
		},
	}
	if err := RunCommand(m, newPreHookTestRegistry(), "probe-run", nil, nil); err != nil {
		t.Fatalf("RunCommand(probe-run) = %v, want nil — an unresolved pre_kill degrades to a no-op", err)
	}
}

// makeHopKind is a test kind that reproduces the pre-run hop three shipped
// kinds take before starting — vitedev.go, docker.go and cleandev.go each
// call surf.Command(<project-declared name>, …) via runPreHook. It writes a
// marker to the log through the process context on either side of the hop,
// so a test can see whether both markers landed in the same file.
func makeHopKind(_ string, action manifest.Action, _ *manifest.Manifest, surf *Surface) (Runner, error) {
	target, _ := action["hop_to"].(string)
	return func(args []string) error {
		luahost.Process().Write("before-hop\n")
		if err := surf.Command(target, args); err != nil {
			return err
		}
		luahost.Process().Write("after-hop\n")
		return nil
	}, nil
}

// newHopTestRegistry binds the hop double to "multi-target" rather than
// inventing a kind name. catalog.go is the single source of truth for which
// kinds exist and Register refuses an undeclared one — that guard is design
// principle 5 and a test is not a reason to weaken it. "multi-target" is a
// real declared kind that already receives a Surface, so the double slots in
// where a factory legitimately lives.
func newHopTestRegistry() *Registry {
	r := NewRegistry()
	r.MustRegister("shell", makeShell)
	r.MustRegister("multi-target", makeHopKind)
	return r
}

// TestNestedCommandWritesOneLogFile is admin-rc82.1: a command whose step
// hops into a second command through surf.Command must still write exactly
// one log file, named for the OUTER command, with everything after the hop
// appended to it.
//
// MUTATION EVIDENCE: restore the `c.pending = nil; c.logFile = "";
// c.active = false` lines to LogContext.Configure and this fails with two
// files — outer.<ts>.log holding only "before-hop" and inner.<ts>.log
// holding "after-hop".
func TestNestedCommandWritesOneLogFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	luahost.ResetProcessLogContext(dir)

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"outer": {Steps: []string{"hopper"}},
			"inner": {Steps: []string{"quiet"}},
		},
		Actions: map[string]manifest.Action{
			"hopper": {"kind": "multi-target", "hop_to": "inner"},
			"quiet":  {"kind": "shell", "run": "echo inner-ran"},
		},
	}

	if err := RunCommand(m, newHopTestRegistry(), "outer", nil, nil); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}

	all, err := filepath.Glob(filepath.Join(dir, "tmp", "*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("one invocation wrote %d log files, want exactly 1: %v", len(all), all)
	}
	if base := filepath.Base(all[0]); !strings.HasPrefix(base, "outer.") {
		t.Fatalf("log file is %q, want it named for the outer command", base)
	}

	body, err := os.ReadFile(all[0])
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, marker := range []string{"before-hop", "after-hop"} {
		if !strings.Contains(got, marker) {
			t.Fatalf("log is missing %q — output after the hop went elsewhere:\n%s", marker, got)
		}
	}
	if strings.Index(got, "before-hop") > strings.Index(got, "after-hop") {
		t.Fatalf("log is out of chronological order:\n%s", got)
	}
}

// TestNestedLogFalseDoesNotSilenceOuter is the second half of admin-rc82.1:
// Python's _make_step_runner is a bare `pass` for `log = false`, so a nested
// command declaring it must not switch logging off for the outer command
// still running.
//
// MUTATION EVIDENCE: restore `ctx.SetEnabled(false)` to nameCommandLog's
// log-false branch and this fails — "after-hop" never reaches the file.
func TestNestedLogFalseDoesNotSilenceOuter(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	luahost.ResetProcessLogContext(dir)

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"outer": {Steps: []string{"hopper"}},
			"inner": {Steps: []string{"quiet"}, Log: false, HasLog: true},
		},
		Actions: map[string]manifest.Action{
			"hopper": {"kind": "multi-target", "hop_to": "inner"},
			"quiet":  {"kind": "shell", "run": "echo inner-ran"},
		},
	}

	if err := RunCommand(m, newHopTestRegistry(), "outer", nil, nil); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "tmp", "outer.*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 outer log, got %d: %v", len(matches), matches)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "after-hop") {
		t.Fatalf("nested `log = false` silenced the outer command:\n%s", string(body))
	}
}
