package kinds

import (
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestMultiTargetInlineSubtableDispatchesNamedTarget proves the pinned-
// footer doc's inline subtable shape ([actions.dev.cf] run = "...") is
// parsed and that passing the target name as the first Runner arg runs
// exactly that target.
func TestMultiTargetInlineSubtableDispatchesNamedTarget(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	action := manifest.Action{
		"kind": "multi-target",
		"cf":   map[string]any{"run": "touch cf.txt"},
		"lan":  map[string]any{"run": "touch lan.txt"},
	}
	runner, err := makeMultiTarget("dev", action)
	if err != nil {
		t.Fatalf("makeMultiTarget: %v", err)
	}
	if err := runner([]string{"cf"}); err != nil {
		t.Fatalf("runner([cf]) = %v, want nil", err)
	}
	assertFileExists(t, dir, "cf.txt")
	assertFileMissing(t, dir, "lan.txt")
}

// TestMultiTargetFlatTableStringShape proves the flat `targets = {name =
// "shell"}` form (the other shape validate_action supports) also dispatches.
func TestMultiTargetFlatTableStringShape(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	action := manifest.Action{
		"kind":    "multi-target",
		"targets": map[string]any{"cf": "touch cf.txt", "lan": "touch lan.txt"},
	}
	runner, err := makeMultiTarget("dev", action)
	if err != nil {
		t.Fatalf("makeMultiTarget: %v", err)
	}
	if err := runner([]string{"lan"}); err != nil {
		t.Fatalf("runner([lan]) = %v, want nil", err)
	}
	assertFileExists(t, dir, "lan.txt")
	assertFileMissing(t, dir, "cf.txt")
}

// TestMultiTargetOnlyTargetAutoPicks proves a single-target action needs no
// argument at all — matching admin_lib/apple.py's only_target announce-and-
// pick behaviour.
func TestMultiTargetOnlyTargetAutoPicks(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	action := manifest.Action{"kind": "multi-target", "solo": map[string]any{"run": "touch solo.txt"}}
	runner, err := makeMultiTarget("dev", action)
	if err != nil {
		t.Fatalf("makeMultiTarget: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner(nil) = %v, want nil (only target auto-picked)", err)
	}
	assertFileExists(t, dir, "solo.txt")
}

// TestMultiTargetNoTargetNoTTYIsClearError is this ticket's acceptance
// criterion verbatim: "fails with a clear error rather than blocking when
// given none with no TTY" — Runner's stdin, via luahost's Host, is not a
// *os.File here (newHost always builds a real os.Stdin-backed Host; this
// test instead exercises the same non-TTY path PickTarget already proves in
// internal/luahost by using >1 targets and no picked arg, which forces
// PickTarget to run — under `go test`, stdin is not a terminal, so it must
// return promptly with an error rather than blocking the test run forever).
//
// MUTATION EVIDENCE: replace the "no target given" error return with a
// blocking read (e.g. bufio.NewReader(os.Stdin).ReadString('\n') with no
// non-TTY guard), and this test would hang until the surrounding `go test`
// timeout kills it — the sub-second pass time below is itself the
// discriminating signal that no such blocking read is reachable.
func TestMultiTargetNoTargetNoTTYIsClearError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	action := manifest.Action{
		"kind": "multi-target",
		"cf":   map[string]any{"run": "touch cf.txt"},
		"lan":  map[string]any{"run": "touch lan.txt"},
	}
	runner, err := makeMultiTarget("dev", action)
	if err != nil {
		t.Fatalf("makeMultiTarget: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner(nil) with >1 targets and no TTY should error, not block or silently pick one")
	}
	assertFileMissing(t, dir, "cf.txt")
	assertFileMissing(t, dir, "lan.txt")
}

// TestMultiTargetUnknownTargetIsError proves an unrecognised target name is
// rejected rather than silently running nothing.
func TestMultiTargetUnknownTargetIsError(t *testing.T) {
	t.Chdir(t.TempDir())
	action := manifest.Action{"kind": "multi-target", "cf": map[string]any{"run": "true"}}
	runner, err := makeMultiTarget("dev", action)
	if err != nil {
		t.Fatalf("makeMultiTarget: %v", err)
	}
	if err := runner([]string{"nope"}); err == nil {
		t.Fatal("runner([nope]) should error for an unknown target")
	}
}

// TestMultiTargetCannotMixShapes proves the two target shapes (inline
// subtables vs flat `targets` table) are mutually exclusive at factory-build
// time, matching validate_action's "cannot mix targets with inline target
// subtables".
func TestMultiTargetCannotMixShapes(t *testing.T) {
	action := manifest.Action{
		"kind":    "multi-target",
		"cf":      map[string]any{"run": "true"},
		"targets": map[string]any{"lan": "true"},
	}
	if _, err := makeMultiTarget("dev", action); err == nil {
		t.Fatal("mixing inline subtables with a flat targets table should error")
	}
}

// TestMultiTargetNoTargetsIsError proves an action with kind="multi-target"
// but zero targets is rejected rather than silently doing nothing.
func TestMultiTargetNoTargetsIsError(t *testing.T) {
	if _, err := makeMultiTarget("dev", manifest.Action{"kind": "multi-target"}); err == nil {
		t.Fatal("makeMultiTarget with zero targets should error")
	}
}

// TestMultiTargetDelegationRunsThroughDispatch proves a target's `action`
// key (delegation to another declared action) works when invoked through
// RunAction, which wires up currentDispatch — the callback multi-target's
// closure needs to run another named action without holding its own
// Manifest/Registry reference.
func TestMultiTargetDelegationRunsThroughDispatch(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"dev": {
				"kind": "multi-target",
				"cf":   map[string]any{"action": "build-cf"},
			},
			"build-cf": {"kind": "shell", "run": "touch built.txt"},
		},
	}
	r := NewRegistry()
	r.MustRegister("multi-target", makeMultiTarget)
	r.MustRegister("shell", makeShell)

	if err := RunAction(m, r, "dev", []string{"cf"}); err != nil {
		t.Fatalf("RunAction(dev, [cf]) = %v, want nil", err)
	}
	assertFileExists(t, dir, "built.txt")
}

// TestMultiTargetDelegationWithoutDispatchIsError proves calling the raw
// factory Runner directly (bypassing RunAction) for a delegating target
// fails with a clear error rather than a nil-pointer panic.
func TestMultiTargetDelegationWithoutDispatchIsError(t *testing.T) {
	action := manifest.Action{"kind": "multi-target", "cf": map[string]any{"action": "build-cf"}}
	runner, err := makeMultiTarget("dev", action)
	if err != nil {
		t.Fatalf("makeMultiTarget: %v", err)
	}
	if err := runner([]string{"cf"}); err == nil {
		t.Fatal("runner([cf]) for a delegating target with no dispatch context should error")
	}
}
