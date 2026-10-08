package kinds

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/guards"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestRunActionBlocksOnFailingRequires is the acceptance criterion "a
// failing guard blocks the action, prints its diagnostic and exits
// non-zero" (the "exits non-zero" half is CLI wiring; here the Runner never
// fires and RunAction returns the diagnostic as an error).
func TestRunActionBlocksOnFailingRequires(t *testing.T) {
	t.Cleanup(guards.Reset)
	guards.Reset()
	guards.RegisterGuard("always-fails", func() (bool, string) {
		return false, "always-fails: nope"
	})

	dir := t.TempDir()
	t.Chdir(dir)

	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"deploy": {"kind": "shell", "run": "touch deployed.txt", "requires": []any{"always-fails"}},
		},
	}
	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	err := RunAction(m, r, "deploy", nil)
	if err == nil {
		t.Fatal("RunAction(deploy) = nil, want an error from the failing requires guard")
	}
	if !strings.Contains(err.Error(), "always-fails: nope") {
		t.Errorf("error %q should carry the guard's diagnostic", err.Error())
	}
	assertFileMissing(t, dir, "deployed.txt")
}

// TestRunActionPassesWithSatisfiedRequires proves a passing guard lets the
// action run normally.
func TestRunActionPassesWithSatisfiedRequires(t *testing.T) {
	t.Cleanup(guards.Reset)
	guards.Reset()
	guards.RegisterGuard("always-ok", func() (bool, string) { return true, "" })

	dir := t.TempDir()
	t.Chdir(dir)

	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"deploy": {"kind": "shell", "run": "touch deployed.txt", "requires": []any{"always-ok"}},
		},
	}
	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	if err := RunAction(m, r, "deploy", nil); err != nil {
		t.Fatalf("RunAction(deploy) = %v, want nil", err)
	}
	assertFileExists(t, dir, "deployed.txt")
}

// TestMultiTargetChildRequiresOverridesParent is the acceptance criterion
// "a child target's requires overrides the parent's rather than merging
// with it": the parent's default guard always fails, but a child with its
// own (passing) requires must still run.
func TestMultiTargetChildRequiresOverridesParent(t *testing.T) {
	t.Cleanup(guards.Reset)
	guards.Reset()
	guards.RegisterGuard("parent-fails", func() (bool, string) { return false, "parent-fails: nope" })
	guards.RegisterGuard("child-ok", func() (bool, string) { return true, "" })

	dir := t.TempDir()
	t.Chdir(dir)

	action := manifest.Action{
		"kind":     "multi-target",
		"requires": []any{"parent-fails"},
		"cf":       map[string]any{"run": "touch cf.txt", "requires": []any{"child-ok"}},
	}
	runner, err := makeMultiTarget("deploy", action, nil, nil)
	if err != nil {
		t.Fatalf("makeMultiTarget: %v", err)
	}
	if err := runner([]string{"cf"}); err != nil {
		t.Fatalf("runner([cf]) = %v, want nil — the child's own requires (satisfied) replaces "+
			"the parent's (failing) default", err)
	}
	assertFileExists(t, dir, "cf.txt")
}

// TestMultiTargetChildEmptyRequiresIsExplicitOptOut is the acceptance
// criterion "requires = [] on a child runs no guards": an empty list must
// NOT merge with (and so not inherit) the parent's failing default.
func TestMultiTargetChildEmptyRequiresIsExplicitOptOut(t *testing.T) {
	t.Cleanup(guards.Reset)
	guards.Reset()
	guards.RegisterGuard("parent-fails", func() (bool, string) { return false, "parent-fails: nope" })

	dir := t.TempDir()
	t.Chdir(dir)

	action := manifest.Action{
		"kind":     "multi-target",
		"requires": []any{"parent-fails"},
		"devvit":   map[string]any{"run": "touch devvit.txt", "requires": []any{}},
	}
	runner, err := makeMultiTarget("deploy", action, nil, nil)
	if err != nil {
		t.Fatalf("makeMultiTarget: %v", err)
	}
	if err := runner([]string{"devvit"}); err != nil {
		t.Fatalf("runner([devvit]) = %v, want nil — requires = [] opts out of the parent default", err)
	}
	assertFileExists(t, dir, "devvit.txt")
}

// TestMultiTargetChildInheritsParentRequiresWhenAbsent proves the default
// direction of inheritance: a target with no `requires` key at all runs the
// parent's list, so a forgotten override is a loud failure rather than a
// silent skip (ADR-0005's "dangerous failure mode is the harmless one").
//
// MUTATION EVIDENCE: change makeMultiTarget's `effectiveRequires :=
// parentRequires` to `effectiveRequires := nil` (never fall back to the
// parent default) and re-run — the real failure was:
//
//	requires_test.go:139: runner([mainframe]) = <nil>, want an error — a
//	target with no requires override must inherit the parent's (failing)
//	default
//
// i.e. the mutant silently drops the parent default for every inheriting
// target, exactly the "forgotten annotation" failure mode ADR-0005 says
// must be loud, not silent.
func TestMultiTargetChildInheritsParentRequiresWhenAbsent(t *testing.T) {
	t.Cleanup(guards.Reset)
	guards.Reset()
	guards.RegisterGuard("parent-fails", func() (bool, string) { return false, "parent-fails: nope" })

	dir := t.TempDir()
	t.Chdir(dir)

	action := manifest.Action{
		"kind":      "multi-target",
		"requires":  []any{"parent-fails"},
		"mainframe": map[string]any{"run": "touch mainframe.txt"}, // no requires: inherits
	}
	runner, err := makeMultiTarget("deploy", action, nil, nil)
	if err != nil {
		t.Fatalf("makeMultiTarget: %v", err)
	}
	if err := runner([]string{"mainframe"}); err == nil {
		t.Fatal("runner([mainframe]) = nil, want an error — a target with no requires override " +
			"must inherit the parent's (failing) default")
	}
	assertFileMissing(t, dir, "mainframe.txt")
}

// TestRunCommandGlobalPreflightBlocksBeforeAnyStepRuns is the acceptance
// criterion "the preflight runs once per invocation" exercised through
// RunCommand: a stale node workspace at the project root must block the
// command before its first step runs, regardless of `requires`.
func TestRunCommandGlobalPreflightBlocksBeforeAnyStepRuns(t *testing.T) {
	t.Cleanup(guards.ResetPreflight)
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "package.json", `{"workspaces": ["packages/*"]}`)
	writeFile(t, dir, "packages/app/package.json",
		`{"name": "app", "dependencies": {"shared": "workspace:*"}}`)
	guards.ResetPreflight()

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build": {Steps: []string{"step"}},
		},
		Actions: map[string]manifest.Action{
			"step": {"kind": "shell", "run": "touch ran.txt"},
		},
	}
	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	if err := RunCommandInline(m, r, "build", nil); err == nil {
		t.Fatal("RunCommandInline(build) = nil, want the deps-fresh preflight failure")
	}
	assertFileMissing(t, dir, "ran.txt")
}

// TestRunCommandPreflightExemptForDeclaredCommand is the other half of that
// criterion: a command declaring `preflight = false` in admin.toml (rather
// than the tool matching a hardcoded name like "logs" or "kill" —
// admin-tlhw.3) must run its steps even with a stale workspace.
func TestRunCommandPreflightExemptForDeclaredCommand(t *testing.T) {
	t.Cleanup(guards.ResetPreflight)
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "package.json", `{"workspaces": ["packages/*"]}`)
	writeFile(t, dir, "packages/app/package.json",
		`{"name": "app", "dependencies": {"shared": "workspace:*"}}`)
	guards.ResetPreflight()

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"logs": {Steps: []string{"step"}, Preflight: false, HasPreflight: true},
		},
		Actions: map[string]manifest.Action{
			"step": {"kind": "shell", "run": "touch ran.txt"},
		},
	}
	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	if err := RunCommandInline(m, r, "logs", nil); err != nil {
		t.Fatalf("RunCommandInline(logs) = %v, want nil — logs declares preflight = false", err)
	}
	assertFileExists(t, dir, "ran.txt")
}

// TestRunCommandEnvRequiresBlocksBeforeAnyStepRuns: a name in [env].requires
// that is unset refuses the command before its first step, and the error
// names the variable; a preflight = false command runs anyway.
func TestRunCommandEnvRequiresBlocksBeforeAnyStepRuns(t *testing.T) {
	t.Cleanup(guards.ResetPreflight)
	dir := t.TempDir()
	t.Chdir(dir)
	guards.ResetPreflight()
	t.Setenv("ADMIN_TEST_REQUIRED_SECRET", "")

	m := &manifest.Manifest{
		EnvRequires: []string{"ADMIN_TEST_REQUIRED_SECRET"},
		Custom: map[string]manifest.Command{
			"build": {Steps: []string{"step"}},
			"kill":  {Steps: []string{"step"}, Preflight: false, HasPreflight: true},
		},
		Actions: map[string]manifest.Action{
			"step": {"kind": "shell", "run": "touch ran.txt"},
		},
	}
	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	err := RunCommandInline(m, r, "build", nil)
	if err == nil || !strings.Contains(err.Error(), "ADMIN_TEST_REQUIRED_SECRET") {
		t.Fatalf("RunCommandInline(build) = %v, want an error naming ADMIN_TEST_REQUIRED_SECRET", err)
	}
	assertFileMissing(t, dir, "ran.txt")

	if err := RunCommandInline(m, r, "kill", nil); err != nil {
		t.Fatalf("RunCommandInline(kill) = %v, want nil — kill declares preflight = false", err)
	}
	assertFileExists(t, dir, "ran.txt")
}
