package kinds

import (
	"os"
	"path/filepath"
	"strings"
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
	runner, err := makeMultiTarget("dev", action, nil, nil)
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
	runner, err := makeMultiTarget("dev", action, nil, nil)
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
	runner, err := makeMultiTarget("dev", action, nil, nil)
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
	runner, err := makeMultiTarget("dev", action, nil, nil)
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
	runner, err := makeMultiTarget("dev", action, nil, nil)
	if err != nil {
		t.Fatalf("makeMultiTarget: %v", err)
	}
	if err := runner([]string{"nope"}); err == nil {
		t.Fatal("runner([nope]) should error for an unknown target")
	}
}

// TestMultiTargetDetachedReentrySentinelIsNowAnUnknownTarget proves
// admin-ysxr.7's sentinel removal: __admin-detached-reentry used to be
// stripped off args[0] as a marker meaning "this dispatch already went
// through the launcher once" — makeMultiTarget no longer knows that string
// at all, so a target literally named that reports the ordinary
// unknown-target error instead of being silently swallowed.
func TestMultiTargetDetachedReentrySentinelIsNowAnUnknownTarget(t *testing.T) {
	t.Chdir(t.TempDir())
	action := manifest.Action{"kind": "multi-target", "cf": map[string]any{"run": "true"}}
	runner, err := makeMultiTarget("dev", action, nil, nil)
	if err != nil {
		t.Fatalf("makeMultiTarget: %v", err)
	}
	err = runner([]string{"__admin-detached-reentry", "cf"})
	if err == nil {
		t.Fatal("runner([__admin-detached-reentry, cf]) should error — the sentinel is no longer special-cased")
	}
	if !strings.Contains(err.Error(), `unknown target "__admin-detached-reentry"`) {
		t.Errorf("error = %v, want it to name __admin-detached-reentry as an unknown target", err)
	}
}

// TestMultiTargetDetachedTargetUnderReentryRunsInline proves the
// replacement signal: a target carrying `detach = true`, dispatched while
// this process is already inside a launched run (ADMIN_REENTRY set), runs
// inline and never reaches the launcher — the process-wide inReentry() gate
// that replaces the old per-dispatch argv sentinel.
func TestMultiTargetDetachedTargetUnderReentryRunsInline(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-1", logPath)

	action := manifest.Action{
		"kind": "multi-target",
		"cf":   map[string]any{"run": "touch cf.txt", "detach": true},
	}
	m := &manifest.Manifest{Terminal: terminal}
	runner, err := makeMultiTarget("dev", action, m, nil)
	if err != nil {
		t.Fatalf("makeMultiTarget: %v", err)
	}

	MarkReentry()
	t.Cleanup(func() { os.Unsetenv(ReentryEnv) })

	if err := runner([]string{"cf"}); err != nil {
		t.Fatalf("runner([cf]) = %v, want nil", err)
	}
	assertFileExists(t, dir, "cf.txt")
	if logged, err := os.ReadFile(logPath); err == nil && len(logged) > 0 {
		t.Errorf("terminal log = %q, want empty — a re-entered dispatch must run the detached target inline", string(logged))
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
	if _, err := makeMultiTarget("dev", action, nil, nil); err == nil {
		t.Fatal("mixing inline subtables with a flat targets table should error")
	}
}

// TestMultiTargetNoTargetsIsError proves an action with kind="multi-target"
// but zero targets is rejected rather than silently doing nothing.
func TestMultiTargetNoTargetsIsError(t *testing.T) {
	if _, err := makeMultiTarget("dev", manifest.Action{"kind": "multi-target"}, nil, nil); err == nil {
		t.Fatal("makeMultiTarget with zero targets should error")
	}
}

// TestMultiTargetDelegationRunsThroughDispatch proves a target's `action`
// key (delegation to another declared action) works when invoked through
// RunAction, which builds the Surface passed to the factory — the callback multi-target's
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
	runner, err := makeMultiTarget("dev", action, nil, nil)
	if err != nil {
		t.Fatalf("makeMultiTarget: %v", err)
	}
	if err := runner([]string{"cf"}); err == nil {
		t.Fatal("runner([cf]) for a delegating target with no dispatch context should error")
	}
}

// -- resolveTargetFrame: parent default, child override, child opt-out ------

func TestResolveTargetFrameParentURLsBecomeTheDefault(t *testing.T) {
	parentURLs := []any{"local"}
	urls, hasFooter, footerVal := resolveTargetFrame(parentURLs, false, false, targetFrameMeta{})
	if len(urls) != 1 || urls[0] != "local" {
		t.Fatalf("got urls=%v", urls)
	}
	if hasFooter || footerVal {
		t.Fatalf("got hasFooter=%v footerVal=%v, want both false (nothing declared)", hasFooter, footerVal)
	}
}

func TestResolveTargetFrameChildOverrideReplacesParentURLs(t *testing.T) {
	parentURLs := []any{"local"}
	childURLs := []any{"local", "lan"}
	urls, _, _ := resolveTargetFrame(parentURLs, false, false, targetFrameMeta{urls: childURLs, hasURLs: true})
	if len(urls) != 2 || urls[0] != "local" || urls[1] != "lan" {
		t.Fatalf("got %v, want %v", urls, childURLs)
	}
}

func TestResolveTargetFrameChildCanOptOutWithAnEmptyList(t *testing.T) {
	urls, _, _ := resolveTargetFrame([]any{"local"}, false, false, targetFrameMeta{urls: []any{}, hasURLs: true})
	if urls == nil || len(urls) != 0 {
		t.Fatalf("got %v, want an empty (non-nil-semantics) override", urls)
	}
}

func TestResolveTargetFrameChildFooterOverrideIsRecorded(t *testing.T) {
	_, hasFooter, footerVal := resolveTargetFrame(nil, false, false, targetFrameMeta{footer: true, hasFooter: true})
	if !hasFooter || !footerVal {
		t.Fatalf("got hasFooter=%v footerVal=%v, want true,true", hasFooter, footerVal)
	}
}

func TestResolveTargetFrameSiblingWithNoOverrideInheritsParentFooter(t *testing.T) {
	_, hasFooter, footerVal := resolveTargetFrame(nil, true, true, targetFrameMeta{})
	if !hasFooter || !footerVal {
		t.Fatalf("got hasFooter=%v footerVal=%v, want the parent's true,true", hasFooter, footerVal)
	}
}

func TestResolveTargetFrameChildCanSuppressAnInheritedFooter(t *testing.T) {
	// urls=[] on the parent could never express "no footer at all" once
	// the parent declared urls — this is the thing footer=false is for.
	parentURLs := []any{"local"}
	_, hasFooter, footerVal := resolveTargetFrame(parentURLs, false, false, targetFrameMeta{footer: false, hasFooter: true})
	if !hasFooter || footerVal {
		t.Fatalf("got hasFooter=%v footerVal=%v, want true,false", hasFooter, footerVal)
	}
}

// TestMultiTargetReservedKeysIncludeURLsAndFooter proves a parent-level
// `urls`/`footer` key is never misidentified as an inline target subtable —
// it can't be today regardless (urls is a list, footer is a bool, neither
// is a map[string]any), but this asserts the documented invariant directly
// rather than relying on that incidental type mismatch.
func TestMultiTargetReservedKeysIncludeURLsAndFooter(t *testing.T) {
	for _, k := range []string{"urls", "footer"} {
		if !multiTargetReserved[k] {
			t.Errorf("expected %q to be a reserved (non-target) key", k)
		}
	}
}

// TestTargetDescriptionPrefersDescOverRun pins the order the Work tab's target
// rows read: a target's own desc, then the delegated action's desc, then the
// delegated action's run — never the bare "@name" while something better exists.
func TestTargetDescriptionPrefersDescOverRun(t *testing.T) {
	m := &manifest.Manifest{Actions: map[string]manifest.Action{
		"documented": {"kind": "shell", "run": "make docs", "desc": "Build the docs"},
		"bare":       {"kind": "shell", "run": "make bare"},
	}}
	parent := manifest.Action{
		"kind": "multi-target",
		"targets": map[string]any{
			"docs": "@documented", "bare": "@bare", "gone": "@missing", "raw": "echo hi",
		},
	}
	for key, want := range map[string]string{
		"docs": "Build the docs", "bare": "make bare", "gone": "@missing", "raw": "echo hi",
	} {
		if got := TargetDescription(m, parent, key); got != want {
			t.Errorf("flat target %q = %q, want %q", key, got, want)
		}
	}
	inline := manifest.Action{
		"kind":    "multi-target",
		"own":     map[string]any{"run": "go test", "desc": "Run the tests"},
		"forward": map[string]any{"action": "documented"},
	}
	if got := TargetDescription(m, inline, "own"); got != "Run the tests" {
		t.Errorf("inline own desc = %q", got)
	}
	if got := TargetDescription(m, inline, "forward"); got != "Build the docs" {
		t.Errorf("inline delegate = %q", got)
	}
}
