package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
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
			"review": {"kind": "workflow", "over": "selection", "run": "claude $ADMIN_TARGET"},
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

// TestStartWorkflowPickerOffersTheDogfoodImplementDeclaration is
// admin-7wy5's acceptance criterion 3: with a bead selected and this
// repo's own dogfood declaration in the manifest ([commands.implement]
// routing to a kind="workflow" action with over="selection" and
// agent="claude" on the action), pressing `w` opens the picker with
// "implement" among its candidates — replacing "no selection-sourced
// workflow is declared in admin.toml", the status the ticket's body quotes
// as today's behavior with nothing declared.
func TestStartWorkflowPickerOffersTheDogfoodImplementDeclaration(t *testing.T) {
	mm := workTestModel(dogfoodImplementManifest())
	mm.active = tabBeads
	mm.trackerSelected = map[string]bool{"proj-1": true}

	got, _ := mm.startWorkflowPicker()
	m := got.(Model)
	if !m.swOpen {
		t.Fatalf("swOpen = false, want the picker open")
	}
	if len(m.swOptions) != 1 || m.swOptions[0].name != "implement" {
		t.Errorf("swOptions = %v, want exactly [implement]", m.swOptions)
	}
	if strings.Contains(m.status, "no selection-sourced workflow is declared") {
		t.Errorf("status = %q, want the picker to open instead of refusing", m.status)
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
// carrying the action's run string.
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
	if rm.wfTargets[0].Run != "claude $ADMIN_TARGET" {
		t.Errorf("target run = %q, want the action's run string", rm.wfTargets[0].Run)
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

// --- admin-4pps: the Branches pane and the PRs tab as workflow sources ---

// TestTrackerRowIDOnPR proves trackerRowID reads the same identity field
// rowID already keys keymap.PR under, so a selection id and a row's own id
// can never disagree. rowBranch is deliberately not asserted here: it never
// reaches trackerRowID (toggleTrackerSelection routes the Branches pane to
// toggleBranchSelection first), so there is no case for it to pin.
func TestTrackerRowIDOnPR(t *testing.T) {
	if got := trackerRowID(rowItem{kind: rowPR, prNum: 42}); got != "42" {
		t.Errorf("trackerRowID(rowPR) = %q, want 42", got)
	}
}

// TestActiveSelectionPicksByTabAndPane proves activeSelection returns
// m.selected on the Branches pane and m.trackerSelected everywhere else —
// the single switch every reader and writer in this file goes through.
func TestActiveSelectionPicksByTabAndPane(t *testing.T) {
	m := Model{
		active: tabLocal, pane: paneBranches,
		selected: map[string]bool{"feature-1": true}, trackerSelected: map[string]bool{"proj-1": true},
	}
	if got := m.activeSelection(); len(got) != 1 || !got["feature-1"] {
		t.Errorf("activeSelection() on Branches pane = %v, want m.selected", got)
	}
	m.active, m.pane = tabPRs, paneBranches // pane is irrelevant off tabLocal
	if got := m.activeSelection(); len(got) != 1 || !got["proj-1"] {
		t.Errorf("activeSelection() on PRs tab = %v, want m.trackerSelected", got)
	}
	m.active, m.pane = tabLocal, paneCommits
	if got := m.activeSelection(); len(got) != 1 || !got["proj-1"] {
		t.Errorf("activeSelection() on Local/Commits = %v, want m.trackerSelected (not the Branches map)", got)
	}
}

// TestTrackerTargetsOverBranchesPaneReadsLocalBranches proves the Branches
// tab's targets come from m.local.branches gated on m.selected — including a
// selected branch minesOnly currently hides, so scrolling or filtering it
// out of view cannot silently drop it from a dispatch.
func TestTrackerTargetsOverBranchesPaneReadsLocalBranches(t *testing.T) {
	m := Model{
		active: tabLocal, pane: paneBranches, def: "main", current: "main",
		local: localData{branches: []git.BranchInfo{
			{Name: "main", Current: true},
			{Name: "feature-1"},
			{Name: "feature-2"},
		}},
		selected:  map[string]bool{"feature-1": true},
		minesOnly: true, // hides feature-1 from branchRows(); trackerTargets must not care
	}
	got := m.trackerTargets(manifest.Action{"run": "r $ADMIN_TARGET"})
	if len(got) != 1 || got[0].Name != "feature-1" {
		t.Fatalf("trackerTargets() = %v, want exactly [feature-1]", got)
	}
	if got[0].Run != "r $ADMIN_TARGET" {
		t.Errorf("target run = %q, want the action's run string", got[0].Run)
	}
}

// TestTrackerTargetsOverPRsReadsRemotePRs proves the PRs tab builds targets
// from m.remote.PRs keyed by number, the same shape Beads/Issues already use.
func TestTrackerTargetsOverPRsReadsRemotePRs(t *testing.T) {
	m := Model{
		active:          tabPRs,
		remote:          data.Remote{PRs: []gh.OpenPR{{Number: 7}, {Number: 9}}},
		trackerSelected: map[string]bool{"9": true},
	}
	got := m.trackerTargets(nil)
	if len(got) != 1 || got[0].Name != "9" {
		t.Fatalf("trackerTargets() = %v, want exactly [9]", got)
	}
}

// TestTrackerTargetsStaleBeadsSelectionCannotHijackBranches is the acceptance
// criterion "a stale Beads selection cannot hijack a `w` pressed on a
// Branches row": trackerSelected holds a leftover id from the Beads tab, but
// the active pane is Branches with nothing in m.selected, so trackerTargets
// (and therefore rowKeyWorkflow's `len(targets) > 0` gate) must see zero.
func TestTrackerTargetsStaleBeadsSelectionCannotHijackBranches(t *testing.T) {
	m := Model{
		active: tabLocal, pane: paneBranches,
		local:           localData{branches: []git.BranchInfo{{Name: "feature-1"}}},
		selected:        map[string]bool{},
		trackerSelected: map[string]bool{"proj-1": true},
	}
	if got := m.trackerTargets(nil); len(got) != 0 {
		t.Errorf("trackerTargets() = %v with an empty branch selection, want none — a stale Beads pick hijacked it", got)
	}
}

// TestClearActiveSelectionOnlyClearsTheActiveMap proves a confirmed
// dispatch's cleanup (workflowConfirmKey) empties whichever map is active
// and leaves the other one — Clean Up's own state included — untouched.
func TestClearActiveSelectionOnlyClearsTheActiveMap(t *testing.T) {
	m := Model{
		active: tabLocal, pane: paneBranches,
		selected:        map[string]bool{"feature-1": true},
		trackerSelected: map[string]bool{"proj-1": true},
	}
	m.clearActiveSelection()
	if len(m.selected) != 0 {
		t.Errorf("m.selected = %v after clearActiveSelection on the Branches pane, want empty", m.selected)
	}
	if !m.trackerSelected["proj-1"] {
		t.Errorf("clearActiveSelection on the Branches pane touched trackerSelected: %v", m.trackerSelected)
	}

	m.active, m.pane = tabPRs, paneBranches
	m.selected = map[string]bool{"feature-1": true}
	m.clearActiveSelection()
	if !m.selected["feature-1"] {
		t.Errorf("clearActiveSelection on the PRs tab touched m.selected — Clean Up's state, want untouched: %v", m.selected)
	}
}

// TestToggleTrackerSelectionOnBranchesPaneRoutesToBranchSelection proves
// space on the Branches pane goes through toggleBranchSelection (its
// secMain / checked-out refusals still apply) rather than writing branch
// names into trackerSelected.
func TestToggleTrackerSelectionOnBranchesPaneRoutesToBranchSelection(t *testing.T) {
	m := Model{
		active: tabLocal, pane: paneBranches, def: "main", current: "main",
		local:           localData{branches: []git.BranchInfo{{Name: "main", Current: true}, {Name: "feature-1"}}},
		selected:        map[string]bool{},
		trackerSelected: map[string]bool{},
	}
	m.paneCursor[paneBranches] = len(m.branchRows()) - 1 // the non-main row
	m.toggleTrackerSelection()
	if !m.selected["feature-1"] {
		t.Errorf("space on the Branches pane did not select feature-1 via m.selected: %v", m.selected)
	}
	if len(m.trackerSelected) != 0 {
		t.Errorf("space on the Branches pane wrote into trackerSelected: %v", m.trackerSelected)
	}
}

// TestDeleteSelectedBranchesUnaffectedByACancelledWorkflowPick proves Clean
// Up still reads the exact same m.selected after a `w` that opened the
// picker and was then cancelled — the acceptance criterion "Clean Up still
// reads the same Branches selection unchanged."
func TestDeleteSelectedBranchesUnaffectedByACancelledWorkflowPick(t *testing.T) {
	mm := workTestModel(selectionWorkflowManifest())
	mm.active, mm.pane = tabLocal, paneBranches
	mm.def, mm.current = "main", "main"
	mm.local = localData{branches: []git.BranchInfo{{Name: "main", Current: true}, {Name: "feature-1"}}}
	mm.selected = map[string]bool{"feature-1": true}
	mm.trackerSelected = map[string]bool{}

	got, _ := mm.startWorkflowPicker()
	m := got.(Model)
	if !m.swOpen {
		t.Fatalf("startWorkflowPicker did not open with a nonempty branch selection")
	}
	got, _ = m.workflowPickerKey("esc")
	m = got.(Model)
	if m.swOpen {
		t.Fatalf("picker still open after esc")
	}
	if !m.selected["feature-1"] {
		t.Fatalf("m.selected lost feature-1 after a cancelled workflow pick: %v", m.selected)
	}
	got, _ = m.deleteSelectedBranches()
	m = got.(Model)
	if !m.confirming || len(m.confirmTargets) != 1 || m.confirmTargets[0].branch != "feature-1" {
		t.Errorf("deleteSelectedBranches after a cancelled pick = confirming=%v targets=%v, want feature-1 staged", m.confirming, m.confirmTargets)
	}
}

// newBranchesPaneModel builds a real Model (via New, over a nonexistent repo
// root so loadManifest simply finds nothing — the same pattern
// design_parity_test.go's protoModel uses) with one selectable branch and
// one open PR, ready to drive through the pump directly. Frame(ctx, m, …)
// is deliberately not used here: it runs Init, whose local/remote loads over
// a t.TempDir() would overwrite these injected fixtures before a single key
// lands.
func newBranchesPaneModel() Model {
	m := New(context.Background(), "/repo", "owner/repo", "main", "main")
	m.width, m.height = 100, 30
	m.focus = focusBody
	m.local = localData{branches: []git.BranchInfo{
		{Name: "main", Current: true},
		{Name: "feature-1"},
	}}
	m.haveLocal = true
	m.remote = data.Remote{PRs: []gh.OpenPR{{Number: 42, Title: "fix the thing", Bucket: gh.BucketMine}}}
	m.haveData = true
	m.manifest = &manifest.Manifest{
		Custom:  map[string]manifest.Command{"review-branch": {Desc: "review a picked branch", Steps: []string{"review"}}},
		Actions: map[string]manifest.Action{"review": {"kind": "workflow", "over": "selection", "run": "claude $ADMIN_TARGET"}},
	}
	m.manifestOK = true
	return m
}

// TestFrameSpaceThenWOnBranchesPaneOpensPickerAndConfirm is the acceptance
// criterion "space then w on the Branches pane opens the picker and
// dispatches over the selected branches", asserted through the headless
// frame renderer.
func TestFrameSpaceThenWOnBranchesPaneOpensPickerAndConfirm(t *testing.T) {
	m := newBranchesPaneModel()
	m.active, m.pane = tabLocal, paneBranches
	// branchRows() puts "main" first (secMain, rank -1) and "feature-1"
	// after it — one down from the default cursor lands on feature-1.
	p := &pump{msgs: make(chan tea.Msg, 64), model: m}
	p.apply(keyMsg("down"))
	p.apply(keyMsg("space"))
	p.apply(keyMsg("w"))

	out := p.model.View()
	if !strings.Contains(out, "Run a workflow over 1 selected") {
		t.Fatalf("picker did not open over the selected branch:\n%s", out)
	}
	if !strings.Contains(out, "review a picked branch") {
		t.Errorf("picker body missing the candidate's desc:\n%s", out)
	}

	p.apply(keyMsg("enter"))
	out = p.model.View()
	if !strings.Contains(out, "feature-1") {
		t.Errorf("confirm overlay does not name the selected branch feature-1:\n%s", out)
	}
	if !p.model.selected["feature-1"] {
		t.Errorf("m.selected = %v with the confirm overlay still open, want untouched until dispatch is confirmed", p.model.selected)
	}
}

// TestFrameSpaceThenWOnPRsTabOpensPickerAndConfirm is the acceptance
// criterion's "same for the PRs tab", asserted through the headless frame
// renderer.
func TestFrameSpaceThenWOnPRsTabOpensPickerAndConfirm(t *testing.T) {
	m := newBranchesPaneModel()
	m.active = tabPRs
	p := &pump{msgs: make(chan tea.Msg, 64), model: m}
	trackerCursorTo(t, &p.model, "42")
	p.apply(keyMsg("space"))
	p.apply(keyMsg("w"))

	out := p.model.View()
	if !strings.Contains(out, "Run a workflow over 1 selected") {
		t.Fatalf("picker did not open over the selected PR:\n%s", out)
	}

	p.apply(keyMsg("enter"))
	out = p.model.View()
	if !strings.Contains(out, "42") {
		t.Errorf("confirm overlay does not name the selected PR #42:\n%s", out)
	}
}

// TestFrameBranchesKeybarWLabelFollowsSelection is the acceptance criterion
// "the Branches keybar renders 'workflow' with a non-empty selection and
// 'new' with an empty one", asserted through the headless frame renderer.
// Width is set well past keybar()'s drop threshold (100) so the `w` pair is
// never dropped before the assertion can see it.
func TestFrameBranchesKeybarWLabelFollowsSelection(t *testing.T) {
	m := newBranchesPaneModel()
	m.width = 200
	m.active, m.pane = tabLocal, paneBranches
	p := &pump{msgs: make(chan tea.Msg, 64), model: m}

	out := stripANSI(p.model.View())
	if !strings.Contains(out, "w new") {
		t.Errorf("empty-selection footer missing \"w new\":\n%s", out)
	}
	if strings.Contains(out, "w workflow") {
		t.Errorf("empty-selection footer already says \"w workflow\":\n%s", out)
	}

	// branchRows() puts "main" first (secMain, rank -1) and "feature-1" after
	// it — one down from the default cursor lands on feature-1, which space
	// can actually select.
	p.apply(keyMsg("down"))
	p.apply(keyMsg("space"))

	out = stripANSI(p.model.View())
	if !strings.Contains(out, "w workflow") {
		t.Errorf("non-empty-selection footer missing \"w workflow\":\n%s", out)
	}
	if strings.Contains(out, "w new") {
		t.Errorf("non-empty-selection footer still says \"w new\":\n%s", out)
	}
}
