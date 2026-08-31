package kinds

import (
	"strings"
	"testing"

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
	if err := RunCommand(m, newDispatchTestRegistry(), "build", nil); err != nil {
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
	if err := RunCommand(m, newDispatchTestRegistry(), "build", nil); err == nil {
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
	err := RunCommand(m, newDispatchTestRegistry(), "bad", nil)
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
	if err := RunCommand(m, newDispatchTestRegistry(), "bad", nil); err == nil {
		t.Fatal("RunCommand should reject a command carrying 'run'")
	}
}

// TestRunCommandUnknownCommandIsError proves a missing command name errors
// clearly rather than panicking.
func TestRunCommandUnknownCommandIsError(t *testing.T) {
	m := &manifest.Manifest{Custom: map[string]manifest.Command{}}
	if err := RunCommand(m, newDispatchTestRegistry(), "nope", nil); err == nil {
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
	if err := RunCommand(m, newDispatchTestRegistry(), "build", nil); err != nil {
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

func readFile(t *testing.T, dir, relPath string) string {
	t.Helper()
	data := mustReadFile(t, dir, relPath)
	return string(data)
}
