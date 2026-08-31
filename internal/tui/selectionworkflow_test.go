package tui

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// trackerCursorTo moves m's cursor onto the row whose trackerRowID is id,
// searching the real rows() output rather than a hardcoded index — robust to
// however beadRows/issueRows lay out sections, headers and blank rows.
func trackerCursorTo(t *testing.T, m *Model, id string) {
	t.Helper()
	rows := m.visibleRows()
	for i, r := range rows {
		if trackerRowID(r) == id {
			m.cursor[m.active] = i
			return
		}
	}
	t.Fatalf("no row with tracker id %q among %d rows", id, len(rows))
}

// selectionWorkflowManifest is the fixture selectionWorkflows tests share: one
// query-sourced and one selection-sourced workflow, plus a plain shell
// command — proving the picker offers exactly the selection-sourced one and
// nothing else.
func selectionWorkflowManifest() *manifest.Manifest {
	return &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"retire-merged": {Desc: "retire merged branches", Steps: []string{"retire"}},
			"review-branch": {Desc: "review a picked branch", Steps: []string{"review"}},
			"build":         {Desc: "compile", Steps: []string{"build"}},
		},
		Actions: map[string]manifest.Action{
			"retire": {"kind": "workflow", "over": map[string]any{"query": "label:stale"}},
			"review": {"kind": "workflow", "over": "selection"},
			"build":  {"kind": "shell"},
		},
	}
}

// TestSelectionWorkflowsOffersOnlySelectionSourced is work.go's
// selectionWorkflows(): the picker's candidate list is exactly the
// kind="workflow" commands whose `over` is "selection" — a query-sourced
// workflow (already offered bare on the Work tab) and a plain command are
// both excluded.
func TestSelectionWorkflowsOffersOnlySelectionSourced(t *testing.T) {
	mm := workTestModel(selectionWorkflowManifest())
	got := mm.selectionWorkflows()
	if len(got) != 1 || got[0].name != "review-branch" {
		t.Fatalf("selectionWorkflows() = %v, want exactly [review-branch]", got)
	}
}

// TestDeclaredCommandsAndSelectionWorkflowsPartitionExactly proves
// declaredCommands and selectionWorkflows are true complements over every
// workflow command — nothing falls through both filters, and nothing is
// offered by both, which is the whole reason collectCommands takes an
// `include` predicate instead of two independently-hand-rolled loops.
func TestDeclaredCommandsAndSelectionWorkflowsPartitionExactly(t *testing.T) {
	mm := workTestModel(selectionWorkflowManifest())
	inWork := map[string]bool{}
	for _, c := range mm.declaredCommands() {
		inWork[c.name] = true
	}
	inPicker := map[string]bool{}
	for _, c := range mm.selectionWorkflows() {
		inPicker[c.name] = true
	}
	if inWork["review-branch"] {
		t.Errorf("review-branch (selection-sourced) appeared in declaredCommands")
	}
	if !inWork["retire-merged"] || !inWork["build"] {
		t.Errorf("declaredCommands dropped a non-selection-sourced command: %v", inWork)
	}
	if inPicker["retire-merged"] || inPicker["build"] {
		t.Errorf("selectionWorkflows offered a non-selection-sourced command: %v", inPicker)
	}
}

// TestToggleTrackerSelectionOnBeadRow proves space toggles a bead row in and
// out of trackerSelected by its bead id, and trackerSelectMode tracks the
// map's length exactly like the Branches pane's own selectMode.
func TestToggleTrackerSelectionOnBeadRow(t *testing.T) {
	m := Model{
		active: tabBeads, width: 120, height: 40,
		trackerSelected: map[string]bool{},
		beads:           []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}},
	}
	trackerCursorTo(t, &m, "proj-1")

	if m.trackerSelectMode() {
		t.Fatalf("trackerSelectMode true before anything was toggled")
	}
	m.toggleTrackerSelection()
	if !m.trackerSelected["proj-1"] {
		t.Errorf("proj-1 not selected after toggle")
	}
	if !m.trackerSelectMode() {
		t.Errorf("trackerSelectMode false with a nonempty selection")
	}
	m.toggleTrackerSelection()
	if m.trackerSelected["proj-1"] {
		t.Errorf("proj-1 still selected after a second toggle")
	}
	if m.trackerSelectMode() {
		t.Errorf("trackerSelectMode true after the selection emptied back out")
	}
}

// TestToggleTrackerSelectionOnIssueRow proves the same mechanic works on the
// Issues tab, keyed by the issue number as a string.
func TestToggleTrackerSelectionOnIssueRow(t *testing.T) {
	m := Model{
		active: tabIssues, width: 120, height: 40,
		trackerSelected: map[string]bool{},
	}
	m.remote.Issues = []gh.IssueItem{{Number: 42, Title: "fix the thing"}}
	trackerCursorTo(t, &m, "42")

	m.toggleTrackerSelection()
	if !m.trackerSelected["42"] {
		t.Errorf("issue 42 not selected after toggle")
	}
}

// TestToggleTrackerSelectionIgnoresNonTrackerRows proves a header/rule row
// under the cursor (nothing selectable pointed at) leaves the selection
// untouched rather than toggling a blank id.
func TestToggleTrackerSelectionIgnoresNonTrackerRows(t *testing.T) {
	m := Model{active: tabBeads, width: 120, height: 40, trackerSelected: map[string]bool{}}
	// No beads at all: beadRows() still returns rows (an error/empty text
	// row), never a rowBead, so cursor 0 sits on something unselectable.
	m.cursor[tabBeads] = 0
	m.toggleTrackerSelection()
	if len(m.trackerSelected) != 0 {
		t.Errorf("toggling a non-tracker row changed the selection: %v", m.trackerSelected)
	}
}

// TestStartWorkflowPickerRefusesEmptySelection is the acceptance criterion
// "with no rows selected, the workflow key says so rather than dispatching
// nothing silently": no overlay opens and the status line explains why.
func TestStartWorkflowPickerRefusesEmptySelection(t *testing.T) {
	mm := workTestModel(selectionWorkflowManifest())
	mm.active = tabBeads
	mm.trackerSelected = map[string]bool{}

	got, cmd := mm.startWorkflowPicker()
	m := got.(Model)
	if m.swOpen {
		t.Errorf("swOpen = true with an empty selection, want the picker refused")
	}
	if cmd != nil {
		t.Errorf("startWorkflowPicker returned a cmd for an empty selection, want nil")
	}
	if !strings.Contains(m.status, "no rows selected") {
		t.Errorf("status = %q, want it to say no rows are selected", m.status)
	}
}

// TestStartWorkflowPickerRefusesWithNoCandidates proves the same silent-
// nothing refusal when the manifest declares no selection-sourced workflow
// at all, even with a real selection.
func TestStartWorkflowPickerRefusesWithNoCandidates(t *testing.T) {
	m := workTestModel(&manifest.Manifest{
		Custom:  map[string]manifest.Command{"build": {Desc: "compile", Steps: []string{"build"}}},
		Actions: map[string]manifest.Action{"build": {"kind": "shell"}},
	})
	m.active = tabBeads
	m.trackerSelected = map[string]bool{"proj-1": true}

	got, cmd := m.startWorkflowPicker()
	got2 := got.(Model)
	if got2.swOpen || cmd != nil {
		t.Errorf("startWorkflowPicker opened with zero selection-sourced workflows declared")
	}
	if !strings.Contains(got2.status, "no selection-sourced workflow") {
		t.Errorf("status = %q, want it to say none is declared", got2.status)
	}
}

// TestStartWorkflowPickerOpensWithCandidates proves a real selection plus a
// real candidate opens the picker over exactly that candidate list.
func TestStartWorkflowPickerOpensWithCandidates(t *testing.T) {
	mm := workTestModel(selectionWorkflowManifest())
	mm.active = tabBeads
	mm.trackerSelected = map[string]bool{"proj-1": true}

	got, _ := mm.startWorkflowPicker()
	m := got.(Model)
	if !m.swOpen {
		t.Fatalf("swOpen = false, want the picker open")
	}
	if len(m.swOptions) != 1 || m.swOptions[0].name != "review-branch" {
		t.Errorf("swOptions = %v, want exactly [review-branch]", m.swOptions)
	}
}

// TestWorkflowPickerKeyMovesCursor proves up/down (and j/k) move swCursor
// without touching anything else.
func TestWorkflowPickerKeyMovesCursor(t *testing.T) {
	m := Model{swOpen: true, swOptions: []workCmdRow{{name: "a"}, {name: "b"}, {name: "c"}}}

	got, _ := m.workflowPickerKey("down")
	m = got.(Model)
	if m.swCursor != 1 {
		t.Fatalf("swCursor = %d after down, want 1", m.swCursor)
	}
	got, _ = m.workflowPickerKey("down")
	m = got.(Model)
	got, _ = m.workflowPickerKey("down") // one past the end must clamp, not wrap
	m = got.(Model)
	if m.swCursor != 2 {
		t.Fatalf("swCursor = %d past the end, want clamped to 2", m.swCursor)
	}
	got, _ = m.workflowPickerKey("up")
	m = got.(Model)
	if m.swCursor != 1 {
		t.Fatalf("swCursor = %d after up, want 1", m.swCursor)
	}
	if !m.swOpen {
		t.Errorf("swOpen closed by plain cursor movement")
	}
}

// TestWorkflowPickerKeyEscCancelsWithoutOpeningConfirm proves esc (or any
// non-enter key) closes the picker having opened nothing else.
func TestWorkflowPickerKeyEscCancelsWithoutOpeningConfirm(t *testing.T) {
	m := Model{swOpen: true, swOptions: []workCmdRow{{name: "review-branch"}}, trackerSelected: map[string]bool{"proj-1": true}}
	got, cmd := m.workflowPickerKey("esc")
	rm := got.(Model)
	if rm.swOpen {
		t.Errorf("swOpen still true after esc")
	}
	if rm.wfOpen {
		t.Errorf("esc on the picker opened the confirm overlay")
	}
	if cmd != nil {
		t.Errorf("esc on the picker returned a cmd, want nil")
	}
	if len(rm.trackerSelected) != 1 {
		t.Errorf("esc on the picker touched the tracker selection: %v", rm.trackerSelected)
	}
}

// TestPickWorkflowFromBeadsBuildsTargetsAndOpensConfirm is the ticket's core
// path end to end: two selected beads, a chosen selection-sourced workflow,
// and the confirm overlay opening with exactly those two targets, each
// carrying the same /implement prompt ResolveQuery would have built for a
// query-sourced dispatch of the same beads.
func TestPickWorkflowFromBeadsBuildsTargetsAndOpensConfirm(t *testing.T) {
	m := workTestModel(selectionWorkflowManifest())
	m.active = tabBeads
	m.beads = []bd.Bead{
		{ID: "proj-1", Title: "one", Status: "open"},
		{ID: "proj-2", Title: "two", Status: "open"},
		{ID: "proj-3", Title: "three", Status: "open"},
	}
	// Selected out of order (3 then 1) — trackerTargets must still hand back
	// targets in m.beads' own order, not selection order.
	m.trackerSelected = map[string]bool{"proj-3": true, "proj-1": true}

	got, _ := m.pickWorkflow(workCmdRow{name: "review-branch", cmd: m.manifest.Custom["review-branch"]})
	rm := got.(Model)

	if !rm.wfOpen || rm.wfResolving {
		t.Fatalf("wfOpen=%v wfResolving=%v, want the confirm overlay open and settled", rm.wfOpen, rm.wfResolving)
	}
	if !rm.wfFromSelection {
		t.Errorf("wfFromSelection = false, want true for a picker-originated confirm")
	}
	if rm.wfName != "review-branch" {
		t.Errorf("wfName = %q, want review-branch", rm.wfName)
	}
	if len(rm.wfTargets) != 2 {
		t.Fatalf("wfTargets = %v, want exactly the 2 selected beads", rm.wfTargets)
	}
	if rm.wfTargets[0].Name != "proj-1" || rm.wfTargets[1].Name != "proj-3" {
		t.Errorf("wfTargets = %v, want [proj-1, proj-3] in m.beads' own order", rm.wfTargets)
	}
	if rm.wfTargets[0].Prompt != kinds.ImplementPrompt("proj-1") {
		t.Errorf("target prompt = %q, want the same shape ResolveQuery builds", rm.wfTargets[0].Prompt)
	}
}

// TestPickWorkflowFromIssuesBuildsTargets proves the same builder works over
// the Issues tab's own data, keyed by issue number.
func TestPickWorkflowFromIssuesBuildsTargets(t *testing.T) {
	m := workTestModel(selectionWorkflowManifest())
	m.active = tabIssues
	m.remote.Issues = []gh.IssueItem{{Number: 7, Title: "seven"}, {Number: 9, Title: "nine"}}
	m.trackerSelected = map[string]bool{"9": true}

	got, _ := m.pickWorkflow(workCmdRow{name: "review-branch", cmd: m.manifest.Custom["review-branch"]})
	rm := got.(Model)
	if len(rm.wfTargets) != 1 || rm.wfTargets[0].Name != "9" {
		t.Fatalf("wfTargets = %v, want exactly [9]", rm.wfTargets)
	}
}

// TestWorkflowConfirmKeyClearsSelectionOnlyWhenDispatchingFromPicker proves
// the trade selectionworkflow.go documents: confirming clears
// trackerSelected (so the picker's own selection doesn't linger after the
// workflow it named actually runs), but esc/cancel leaves it alone for a
// second try, and a query-sourced confirm (wfFromSelection false) never
// touches trackerSelected at all even if it happened to be nonempty.
func TestWorkflowConfirmKeyClearsSelectionOnlyWhenDispatchingFromPicker(t *testing.T) {
	stubDispatchWorkflow(t, nil)

	confirmed := Model{
		wfOpen: true, wfFromSelection: true, wfName: "review-branch",
		wfAction: manifest.Action{"kind": "workflow"}, wfTargets: []kinds.Target{{Name: "proj-1"}},
		trackerSelected: map[string]bool{"proj-1": true},
	}
	got, _ := confirmed.workflowConfirmKey("enter")
	rm := got.(Model)
	if len(rm.trackerSelected) != 0 {
		t.Errorf("trackerSelected = %v after a confirmed picker dispatch, want cleared", rm.trackerSelected)
	}

	cancelled := Model{
		wfOpen: true, wfFromSelection: true, wfName: "review-branch",
		wfAction: manifest.Action{"kind": "workflow"}, wfTargets: []kinds.Target{{Name: "proj-1"}},
		trackerSelected: map[string]bool{"proj-1": true},
	}
	got, _ = cancelled.workflowConfirmKey("esc")
	rm = got.(Model)
	if !rm.trackerSelected["proj-1"] {
		t.Errorf("trackerSelected cleared on cancel, want it left alone for a retry")
	}

	queryConfirmed := Model{
		wfOpen: true, wfFromSelection: false, wfName: "retire-merged",
		wfAction: manifest.Action{"kind": "workflow"}, wfTargets: []kinds.Target{{Name: "a-1"}},
		trackerSelected: map[string]bool{"proj-1": true},
	}
	got, _ = queryConfirmed.workflowConfirmKey("enter")
	rm = got.(Model)
	if !rm.trackerSelected["proj-1"] {
		t.Errorf("a query-sourced dispatch touched trackerSelected, want it untouched")
	}
}

// TestRenderWorkflowPickerBodyListsCandidates proves the overlay actually
// shows what it's letting you pick from.
func TestRenderWorkflowPickerBodyListsCandidates(t *testing.T) {
	m := Model{
		swOpen: true, trackerSelected: map[string]bool{"proj-1": true, "proj-2": true},
		swOptions: []workCmdRow{{name: "review-branch", cmd: manifest.Command{Desc: "review a picked branch"}}},
	}
	body := strings.Join(m.renderWorkflowPickerBody(80, 24), "\n")
	for _, want := range []string{"review-branch", "review a picked branch", "2 selected"} {
		if !strings.Contains(body, want) {
			t.Errorf("picker body missing %q:\n%s", want, body)
		}
	}
}
