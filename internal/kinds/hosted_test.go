// hosted_test.go covers admin-3rgp.59: RunCommandHosted, the second
// command-dispatch entry point from which launcher dispatch is structurally
// unreachable. The paired tests below are the whole point — the same
// manifest, the same fake [terminal] launcher, the same detached step,
// dispatched through each entry point, must land on opposite surfaces.
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
	return &manifest.Manifest{
		Terminal: terminal,
		Custom: map[string]manifest.Command{
			"dev": {Steps: []string{"dev-server"}},
		},
		Actions: map[string]manifest.Action{
			"dev-server": {"kind": "vite-dev", "detach": true},
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

// TestRunCommandOpensPaneForLongRunningStep is the control half of the
// pair: the existing CLI entry point keeps its current behavior — a
// declared [terminal] plus a detached step means a launcher dispatch, not
// an inline run.
func TestRunCommandOpensPaneForLongRunningStep(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	inlineRan := false
	if err := RunCommand(longRunningManifest(terminal), registryRecordingInline(&inlineRan), "dev", nil); err != nil {
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

// TestRunCommandHostedNeverOpensAPane is the acceptance criterion: the
// same manifest, the same fake [terminal] launcher, the same detached
// step — the hosted entry point must run the step in this process and must
// not call the launcher at all.
func TestRunCommandHostedNeverOpensAPane(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	before := len(livework.Default.List())

	inlineRan := false
	if err := RunCommandHosted(longRunningManifest(terminal), registryRecordingInline(&inlineRan), "dev", nil); err != nil {
		t.Fatalf("RunCommandHosted = %v, want nil", err)
	}
	if !inlineRan {
		t.Error("RunCommandHosted must run a detached step inline, in this process")
	}
	if logged, err := os.ReadFile(logPath); err == nil && len(logged) > 0 {
		t.Errorf("terminal log = %q, want empty — the hosted entry point must never reach the launcher", string(logged))
	}
	if got := len(livework.Default.List()); got != before {
		t.Errorf("live-work registry grew from %d to %d — a hosted run registers no launcher handle", before, got)
	}
}

// TestRunCommandHostedNestedDelegationStaysInline closes the one call-graph
// hole a shared step loop alone would leave open: a multi-target target
// delegating with `action = "other"` reaches its callee through the Surface
// built for its own dispatch, not through the step loop. Under the hosted
// entry point
// that nested hop must stay inline too, or the pane path is reachable one
// level down.
func TestRunCommandHostedNestedDelegationStaysInline(t *testing.T) {
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

	inlineRan := false
	if err := RunCommandHosted(m, registryRecordingInline(&inlineRan), "dev", []string{"server"}); err != nil {
		t.Fatalf("RunCommandHosted = %v, want nil", err)
	}
	if !inlineRan {
		t.Error("a delegated target under the hosted entry point must run inline")
	}
	if logged, err := os.ReadFile(logPath); err == nil && len(logged) > 0 {
		t.Errorf("terminal log = %q, want empty — nested delegation must not reach the launcher either", string(logged))
	}
}

// TestRunCommandHostedRunsStepsInOrderAndStops proves the hosted entry
// point shares the existing step loop's semantics: declared order,
// stop-at-first-failure, and an error naming the command.
func TestRunCommandHostedRunsStepsInOrderAndStops(t *testing.T) {
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
	err := RunCommandHosted(m, newDispatchTestRegistry(), "build", nil)
	if err == nil {
		t.Fatal("RunCommandHosted = nil, want the failing middle step's error")
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

// TestRunCommandHostedRejectsActionKeysOnCommand proves the hosted entry
// point carries the same ADR-0003 pure-routing check, and the same
// unknown-command and empty-steps errors, as the existing one.
func TestRunCommandHostedRejectsActionKeysOnCommand(t *testing.T) {
	t.Chdir(t.TempDir())

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"bad":   {Steps: []string{"one"}, Raw: map[string]any{"kind": "shell"}},
			"empty": {},
		},
		Actions: map[string]manifest.Action{"one": {"kind": "shell", "run": "true"}},
	}
	err := RunCommandHosted(m, newDispatchTestRegistry(), "bad", nil)
	if err == nil || !strings.Contains(err.Error(), "ADR-0003") {
		t.Errorf("RunCommandHosted(bad) = %v, want the ADR-0003 pure-routing error", err)
	}
	if err := RunCommandHosted(m, newDispatchTestRegistry(), "empty", nil); err == nil {
		t.Error("RunCommandHosted(empty) = nil, want a no-steps error")
	}
	if err := RunCommandHosted(m, newDispatchTestRegistry(), "nope", nil); err == nil {
		t.Error("RunCommandHosted(nope) = nil, want an unknown-command error")
	}
}
