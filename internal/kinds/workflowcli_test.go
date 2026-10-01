// workflowcli_test.go covers admin-spjp.2: the bare-CLI invocation path for
// kind="workflow". The query-path tests exercise runWorkflowCLI through its
// three seams (resolveWorkflowQuery, confirmDispatch, dispatchWorkflowTargets)
// exactly as workflowdispatch_test.go and internal/tui's own tests swap out
// startAgent/runAgent/agentStatus and runResolveQuery/runDispatchWorkflow —
// no real `bd`/`gh` binary, no real git worktree, no real terminal.
package kinds

import (
	"context"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestWorkflowCLIRowNamesRowGesture proves the row-sourced path
// (runWorkflowCLI, not just makeWorkflow's build-time check) fails naming
// the action and the row gesture — this is the acceptance criterion at the
// Runner-invocation level, in case anything ever bypasses makeWorkflow's own
// wiring.
func TestWorkflowCLIRowNamesRowGesture(t *testing.T) {
	action := manifest.Action{"kind": "workflow", "over": "row"}
	err := runWorkflowCLI("retire-merged", action, nil, WorkflowTarget{Source: WorkflowSourceRow}, nil)
	if err == nil {
		t.Fatal("expected an error for a row-sourced workflow run from the bare CLI")
	}
	if !strings.Contains(err.Error(), "retire-merged") {
		t.Fatalf("error %q does not name the action", err.Error())
	}
	if !strings.Contains(err.Error(), "cursor") || !strings.Contains(err.Error(), "dashboard") {
		t.Fatalf("error %q does not name the row gesture", err.Error())
	}
}

// withWorkflowCLISeams swaps resolveWorkflowQuery/confirmDispatch/
// dispatchWorkflowTargets for the duration of a test and restores the
// originals after, mirroring lifetime_test.go's withFakeHerdr pattern.
func withWorkflowCLISeams(t *testing.T, targets []Target, resolveErr error, confirmResult bool) (confirmCalled *bool, dispatchCalls *int, dispatchedTargets *[]Target) {
	t.Helper()
	origResolve, origConfirm, origDispatch := resolveWorkflowQuery, confirmDispatch, dispatchWorkflowTargets

	called := false
	calls := 0
	var got []Target

	resolveWorkflowQuery = func(_ context.Context, _ manifest.Action, _, _, _, _ string) ([]Target, error) {
		return targets, resolveErr
	}
	confirmDispatch = func(string) bool {
		called = true
		return confirmResult
	}
	dispatchWorkflowTargets = func(_ string, _ string, _ manifest.Action, ts []Target, _ DispatchSurface) []TargetResult {
		calls++
		got = ts
		results := make([]TargetResult, len(ts))
		for i, tt := range ts {
			results[i] = TargetResult{Target: tt.Name}
		}
		return results
	}

	t.Cleanup(func() {
		resolveWorkflowQuery, confirmDispatch, dispatchWorkflowTargets = origResolve, origConfirm, origDispatch
	})
	return &called, &calls, &got
}

func queryAction() manifest.Action {
	return manifest.Action{
		"kind": "workflow",
		"over": map[string]any{"query": "label:review-me"},
		"run":  "claude-run $ADMIN_TARGET",
	}
}

// TestWorkflowCLIQueryPrintsResolvedSetAndConfirms proves a query-sourced
// run prints the resolved query, run command, ceiling and every target name to
// stderr before asking to confirm.
func TestWorkflowCLIQueryPrintsResolvedSetAndConfirms(t *testing.T) {
	targets := []Target{{Name: "admin-1"}, {Name: "admin-2"}}
	confirmCalled, dispatchCalls, _ := withWorkflowCLISeams(t, targets, nil, true)
	t.Chdir(t.TempDir())

	action := queryAction()
	wt := WorkflowTarget{Source: WorkflowSourceQuery, Query: "label:review-me"}

	stderr := captureStderr(t, func() {
		if err := runWorkflowCLI("review-waiting", action, nil, wt, nil); err != nil {
			t.Fatalf("runWorkflowCLI = %v, want nil", err)
		}
	})

	if !*confirmCalled {
		t.Error("confirmDispatch was never called")
	}
	if *dispatchCalls != 1 {
		t.Errorf("dispatchWorkflowTargets called %d times, want 1", *dispatchCalls)
	}
	for _, want := range []string{"label:review-me", "claude-run", "admin-1", "admin-2"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
}

// TestWorkflowCLIDeclineDispatchesNothing proves declining the confirmation
// dispatches nothing and returns nil.
func TestWorkflowCLIDeclineDispatchesNothing(t *testing.T) {
	targets := []Target{{Name: "admin-1"}}
	_, dispatchCalls, _ := withWorkflowCLISeams(t, targets, nil, false)
	t.Chdir(t.TempDir())

	action := queryAction()
	wt := WorkflowTarget{Source: WorkflowSourceQuery, Query: "label:review-me"}

	err := captureStderrErr(t, func() error {
		return runWorkflowCLI("review-waiting", action, nil, wt, nil)
	})
	if err != nil {
		t.Fatalf("runWorkflowCLI = %v, want nil on decline", err)
	}
	if *dispatchCalls != 0 {
		t.Errorf("dispatchWorkflowTargets called %d times, want 0 after declining", *dispatchCalls)
	}
}

// TestWorkflowCLIAcceptDispatchesThroughDispatchTargets proves accepting
// dispatches exactly once, with the resolved targets, through
// dispatchWorkflowTargets (kinds.DispatchTargets in production) — the same
// path internal/tui's confirm overlay uses.
func TestWorkflowCLIAcceptDispatchesThroughDispatchTargets(t *testing.T) {
	targets := []Target{{Name: "admin-1"}, {Name: "admin-2"}}
	_, dispatchCalls, dispatched := withWorkflowCLISeams(t, targets, nil, true)
	t.Chdir(t.TempDir())

	action := queryAction()
	wt := WorkflowTarget{Source: WorkflowSourceQuery, Query: "label:review-me"}

	err := captureStderrErr(t, func() error {
		return runWorkflowCLI("review-waiting", action, nil, wt, nil)
	})
	if err != nil {
		t.Fatalf("runWorkflowCLI = %v, want nil", err)
	}
	if *dispatchCalls != 1 {
		t.Fatalf("dispatchWorkflowTargets called %d times, want exactly 1", *dispatchCalls)
	}
	if len(*dispatched) != 2 || (*dispatched)[0].Name != "admin-1" || (*dispatched)[1].Name != "admin-2" {
		t.Errorf("dispatched targets = %+v, want the resolved set unchanged", *dispatched)
	}
}

// TestWorkflowCLIMissingRunErrorsBeforeConfirming proves a
// missing run command fails loudly before the confirm seam is ever touched —
// the same "fail loud, don't waste a confirmation on a set that would
// immediately fail dispatch" contract internal/tui/workflowconfirm.go's
// workflowResolved already enforces.
func TestWorkflowCLIMissingRunErrorsBeforeConfirming(t *testing.T) {
	targets := []Target{{Name: "admin-1"}}
	confirmCalled, dispatchCalls, _ := withWorkflowCLISeams(t, targets, nil, true)
	t.Chdir(t.TempDir())

	action := manifest.Action{"kind": "workflow", "over": map[string]any{"query": "label:review-me"}}
	wt := WorkflowTarget{Source: WorkflowSourceQuery, Query: "label:review-me"}

	err := captureStderrErr(t, func() error {
		return runWorkflowCLI("review-waiting", action, nil, wt, nil)
	})
	if err == nil {
		t.Fatal("expected an error for a workflow with no run command")
	}
	if !strings.Contains(err.Error(), "run") {
		t.Fatalf("error %q does not mention the missing run", err.Error())
	}
	if *confirmCalled {
		t.Error("confirmDispatch was called despite no run command being configured")
	}
	if *dispatchCalls != 0 {
		t.Errorf("dispatchWorkflowTargets called %d times, want 0", *dispatchCalls)
	}
}

// TestWorkflowCLIQueryMatchingNothingDispatchesNothing proves a query
// resolving to zero targets is reported, not treated as an error, and
// dispatches nothing.
func TestWorkflowCLIQueryMatchingNothingDispatchesNothing(t *testing.T) {
	_, dispatchCalls, _ := withWorkflowCLISeams(t, nil, nil, true)
	t.Chdir(t.TempDir())

	action := queryAction()
	wt := WorkflowTarget{Source: WorkflowSourceQuery, Query: "label:nothing-matches"}

	stderr := captureStderr(t, func() {
		if err := runWorkflowCLI("review-waiting", action, nil, wt, nil); err != nil {
			t.Fatalf("runWorkflowCLI = %v, want nil", err)
		}
	})
	if !strings.Contains(stderr, "matched nothing") {
		t.Errorf("stderr = %q, want it to say the query matched nothing", stderr)
	}
	if *dispatchCalls != 0 {
		t.Errorf("dispatchWorkflowTargets called %d times, want 0", *dispatchCalls)
	}
}

// captureStderrErr runs fn with stderr captured (discarded — these tests
// only need fn's return value) so tests that don't assert on printed output
// still don't leak it into the test binary's own stderr.
func captureStderrErr(t *testing.T, fn func() error) error {
	t.Helper()
	var err error
	captureStderr(t, func() {
		err = fn()
	})
	return err
}
