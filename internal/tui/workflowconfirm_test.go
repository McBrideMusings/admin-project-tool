package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// stubResolveQuery swaps runResolveQuery for the duration of one test, so a
// resolve test never shells out to a real `bd` binary.
func stubResolveQuery(t *testing.T, targets []kinds.Target, err error) {
	t.Helper()
	orig := runResolveQuery
	runResolveQuery = func(context.Context, manifest.Action, string, string, string, string) ([]kinds.Target, error) {
		return targets, err
	}
	t.Cleanup(func() { runResolveQuery = orig })
}

// stubDispatchWorkflow swaps runDispatchWorkflow for the duration of one
// test, recording exactly what it was called with — this is what proves
// "confirming dispatches exactly the shown set" without a single real `git
// worktree add`.
func stubDispatchWorkflow(t *testing.T, results []kinds.TargetResult) *[]kinds.Target {
	t.Helper()
	var got []kinds.Target
	orig := runDispatchWorkflow
	runDispatchWorkflow = func(repoRoot, name string, action manifest.Action, targets []kinds.Target, _ kinds.DispatchSurface) []kinds.TargetResult {
		got = targets
		return results
	}
	t.Cleanup(func() { runDispatchWorkflow = orig })
	return &got
}

func queryWorkflowRow(name, query string) *workRowData {
	target := kinds.WorkflowTarget{Source: kinds.WorkflowSourceQuery, Query: query}
	return &workRowData{
		cmdName:        name,
		workflow:       &target,
		workflowAction: manifest.Action{"kind": "workflow", "over": map[string]any{"query": query}},
	}
}

// TestActivateCommandOnQueryWorkflowResolvesFirst is the acceptance
// criterion "activating it resolves the query and shows the resolved targets
// before dispatch": enter on the row must NOT dispatch anything directly —
// it opens the resolving phase and returns a resolve command, never a
// dispatch one.
func TestActivateCommandOnQueryWorkflowResolvesFirst(t *testing.T) {
	stubResolveQuery(t, []kinds.Target{{Name: "a-1"}}, nil)
	mm := workTestModel(nil)
	wd := queryWorkflowRow("retire-merged", "label:stale")

	got, cmd := mm.activateCommand(wd)
	m := got.(Model)

	if !m.wfResolving {
		t.Errorf("wfResolving = false, want true — enter must resolve before dispatch")
	}
	if m.wfOpen {
		t.Errorf("wfOpen = true during resolution, want false until the result lands")
	}
	if m.wfName != "retire-merged" {
		t.Errorf("wfName = %q, want %q", m.wfName, "retire-merged")
	}
	if cmd == nil {
		t.Fatal("activateCommand returned a nil cmd — nothing would ever resolve the query")
	}

	// Running the returned cmd must be the resolve, never a dispatch —
	// proven by type, not by side effect (ResolveQuery is a bd read, so this
	// is safe to actually execute).
	msg := cmd()
	if _, ok := msg.(workflowResolvedMsg); !ok {
		t.Errorf("activateCommand's cmd produced %T, want workflowResolvedMsg", msg)
	}
}

// TestActivateCommandRefusesSelectionSourcedWorkflow proves a
// row-sourced workflow can never become bare-runnable through this
// path — the epic's "over = selection is row-only" — even if it somehow
// reached activateCommand despite declaredCommands already excluding it from
// the Work tab list.
func TestActivateCommandRefusesSelectionSourcedWorkflow(t *testing.T) {
	mm := workTestModel(nil)
	target := kinds.WorkflowTarget{Source: kinds.WorkflowSourceRow}
	wd := &workRowData{cmdName: "review-branch", workflow: &target, workflowAction: manifest.Action{"kind": "workflow", "over": "row"}}

	got, cmd := mm.activateCommand(wd)
	m := got.(Model)

	if cmd != nil {
		t.Errorf("activateCommand returned a cmd for a row-sourced workflow, want nil")
	}
	if m.wfResolving || m.wfOpen {
		t.Errorf("wfResolving/wfOpen set for a row-sourced workflow: resolving=%v open=%v", m.wfResolving, m.wfOpen)
	}
}

// TestWorkflowResolvedOpensOverlayWithTargets proves a real resolved set
// opens the overlay ready to confirm — wfErr empty, targets carried through.
func TestWorkflowResolvedOpensOverlayWithTargets(t *testing.T) {
	m := &Model{wfResolving: true, wfName: "retire-merged"}
	targets := []kinds.Target{{Name: "a-1"}, {Name: "a-2"}}
	m.workflowResolved(workflowResolvedMsg{
		name: "retire-merged", targets: targets, run: "claude $ADMIN_TARGET", ceiling: 3,
	})

	if m.wfResolving {
		t.Errorf("wfResolving still true after the result landed")
	}
	if !m.wfOpen {
		t.Fatalf("wfOpen = false, want true — the resolved set must be shown")
	}
	if m.wfErr != "" {
		t.Errorf("wfErr = %q, want empty for a real resolved set", m.wfErr)
	}
	if len(m.wfTargets) != 2 {
		t.Errorf("wfTargets = %v, want the 2 resolved targets", m.wfTargets)
	}
}

// TestWorkflowResolvedEmptySetSaysSoAndCannotDispatch is the acceptance
// criterion "a query resolving to nothing says so and dispatches nothing":
// the overlay opens, but with an error message and no dispatchable path.
func TestWorkflowResolvedEmptySetSaysSoAndCannotDispatch(t *testing.T) {
	m := &Model{wfResolving: true, wfName: "retire-merged"}
	m.workflowResolved(workflowResolvedMsg{name: "retire-merged", query: "label:nothing", targets: nil})

	if !m.wfOpen {
		t.Fatalf("wfOpen = false, want true — an empty result is still shown, not silently dropped")
	}
	if !strings.Contains(m.wfErr, "matched nothing") {
		t.Errorf("wfErr = %q, want it to say the query matched nothing", m.wfErr)
	}

	got, cmd := m.workflowConfirmKey("enter")
	gm := got.(Model)
	if cmd != nil {
		t.Errorf("enter over an empty result returned a dispatch cmd, want nil")
	}
	if gm.wfOpen {
		t.Errorf("overlay still open after enter over an empty result")
	}
}

// TestWorkflowResolvedErrorSurfaces proves a resolution failure (a bad query,
// an unreadable tracker) is shown rather than treated as an empty match.
func TestWorkflowResolvedErrorSurfaces(t *testing.T) {
	m := &Model{wfResolving: true, wfName: "retire-merged"}
	m.workflowResolved(workflowResolvedMsg{name: "retire-merged", err: errors.New("no beads workspace")})

	if m.wfErr != "no beads workspace" {
		t.Errorf("wfErr = %q, want the resolution error", m.wfErr)
	}
}

// TestWorkflowResolvedIgnoresStaleMessage proves a resolution result for a
// workflow the user already backed out of (wfResolving already false) does
// not reopen the overlay.
func TestWorkflowResolvedIgnoresStaleMessage(t *testing.T) {
	m := &Model{wfResolving: false, wfOpen: false, wfName: "retire-merged"}
	m.workflowResolved(workflowResolvedMsg{name: "retire-merged", targets: []kinds.Target{{Name: "a-1"}}})

	if m.wfOpen {
		t.Errorf("a stale resolution message reopened the overlay")
	}
}

// TestWorkflowConfirmKeyCancelDispatchesNothing is the acceptance criterion
// "cancelling at the confirmation dispatches nothing": any non-confirm key
// over a real resolved set closes the overlay and never calls
// runDispatchWorkflow.
func TestWorkflowConfirmKeyCancelDispatchesNothing(t *testing.T) {
	got := stubDispatchWorkflow(t, nil)
	m := &Model{
		wfOpen: true, wfName: "retire-merged", wfAction: manifest.Action{"kind": "workflow"},
		wfTargets: []kinds.Target{{Name: "a-1"}, {Name: "a-2"}},
	}

	rm, cmd := m.workflowConfirmKey("esc")
	rmm := rm.(Model)
	if rmm.wfOpen {
		t.Errorf("wfOpen still true after esc, want the overlay closed")
	}
	if cmd != nil {
		if _, isMsg := cmd().(workflowDispatchDoneMsg); isMsg {
			t.Fatal("esc produced a dispatch command")
		}
	}
	if *got != nil {
		t.Errorf("runDispatchWorkflow was called on cancel: %v", *got)
	}
}

// TestWorkflowConfirmKeyConfirmDispatchesExactSet is the acceptance
// criterion "confirming dispatches exactly the shown set": enter hands
// runDispatchWorkflow precisely m.wfTargets, no more, no less, and the
// overlay closes.
func TestWorkflowConfirmKeyConfirmDispatchesExactSet(t *testing.T) {
	targets := []kinds.Target{{Name: "a-1", Run: "r a-1"}, {Name: "a-2", Run: "r a-2"}}
	got := stubDispatchWorkflow(t, []kinds.TargetResult{{Target: "a-1"}, {Target: "a-2"}})
	m := &Model{
		wfOpen: true, wfName: "retire-merged", repoRoot: "/repo",
		wfAction: manifest.Action{"kind": "workflow"}, wfTargets: targets,
	}

	rm, cmd := m.workflowConfirmKey("enter")
	rmm := rm.(Model)
	if rmm.wfOpen {
		t.Errorf("wfOpen still true after confirming, want the overlay closed")
	}
	if cmd == nil {
		t.Fatal("confirming returned a nil cmd — nothing would ever dispatch")
	}

	msg, ok := cmd().(workflowDispatchDoneMsg)
	if !ok {
		t.Fatalf("confirm cmd produced %T, want workflowDispatchDoneMsg", msg)
	}
	if len(*got) != len(targets) {
		t.Fatalf("runDispatchWorkflow got %d targets, want exactly the %d shown", len(*got), len(targets))
	}
	for i, tg := range *got {
		if tg.Name != targets[i].Name {
			t.Errorf("target[%d] = %q, want %q", i, tg.Name, targets[i].Name)
		}
	}
	if len(msg.results) != 2 {
		t.Errorf("workflowDispatchDoneMsg carried %d results, want 2", len(msg.results))
	}
}

// TestWorkflowConfirmKeySwallowsEverythingWhileResolving proves no key can
// short-circuit the resolve-before-confirm ordering.
func TestWorkflowConfirmKeySwallowsEverythingWhileResolving(t *testing.T) {
	got := stubDispatchWorkflow(t, nil)
	m := &Model{wfResolving: true, wfName: "retire-merged"}

	rm, cmd := m.workflowConfirmKey("enter")
	rmm := rm.(Model)
	if !rmm.wfResolving {
		t.Errorf("wfResolving cleared by a keypress mid-resolution")
	}
	if cmd != nil {
		t.Errorf("a keypress mid-resolution returned a cmd, want nil")
	}
	if *got != nil {
		t.Errorf("runDispatchWorkflow was called mid-resolution: %v", *got)
	}
}

// TestRenderWorkflowConfirmBodyShowsTheResolvedSet proves the overlay
// actually shows what will run — the whole point of this ticket per its own
// description ("a confirmation the user cannot read before saying yes is not
// a confirmation").
func TestRenderWorkflowConfirmBodyShowsTheResolvedSet(t *testing.T) {
	m := Model{
		wfOpen: true, wfName: "retire-merged", wfQuery: "label:stale",
		wfRun: "claude $ADMIN_TARGET", wfCeiling: 3,
		wfTargets: []kinds.Target{{Name: "a-1"}, {Name: "a-2"}},
	}
	lines := m.renderWorkflowConfirmBody(80, 24)
	if len(lines) != 24 {
		t.Fatalf("renderWorkflowConfirmBody returned %d lines, want exactly height (24)", len(lines))
	}
	body := strings.Join(lines, "\n")
	for _, want := range []string{"retire-merged", "label:stale", "claude", "a-1", "a-2", "enter dispatch"} {
		if !strings.Contains(body, want) {
			t.Errorf("overlay body missing %q:\n%s", want, body)
		}
	}
}

// TestRenderWorkflowConfirmBodyEmptyResultHasNoDispatchHint proves the
// footer never offers "enter dispatch" when there is nothing to dispatch.
func TestRenderWorkflowConfirmBodyEmptyResultHasNoDispatchHint(t *testing.T) {
	m := Model{wfOpen: true, wfName: "retire-merged", wfErr: `query "label:x" matched nothing`}
	body := strings.Join(m.renderWorkflowConfirmBody(80, 24), "\n")
	if strings.Contains(body, "enter dispatch") {
		t.Errorf("overlay offered to dispatch an empty result:\n%s", body)
	}
	if !strings.Contains(body, "matched nothing") {
		t.Errorf("overlay did not say the query matched nothing:\n%s", body)
	}
}

// TestWorkflowDispatchDoneReportsFailures proves a mix of ok/failed target
// results is distinguished on the status line rather than reported as one
// blanket outcome.
func TestWorkflowDispatchDoneReportsFailures(t *testing.T) {
	m := &Model{}
	m.workflowDispatchDone(workflowDispatchDoneMsg{
		name: "retire-merged",
		results: []kinds.TargetResult{
			{Target: "a-1"},
			{Target: "a-2", Err: errors.New("boom")},
		},
	})
	if !strings.Contains(m.status, "1 finished") || !strings.Contains(m.status, "1 failed") {
		t.Errorf("status = %q, want it to report 1 finished and 1 failed", m.status)
	}
}
