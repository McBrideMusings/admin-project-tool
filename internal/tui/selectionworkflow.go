// selectionworkflow.go is admin-3rgp.27 (extended by admin-4pps): letting a
// selection-sourced kind="workflow" command run over rows the user has
// picked on the Beads tab, the Issues tab, the PRs tab, or the Branches
// pane — the row-only half of the "target source" split .24 defined and
// .26 already built the OTHER half of (a query-sourced workflow running
// bare, with a resolve/confirm/dispatch overlay). ADR-0018 states the
// dashboard has ONE selection semantic; admin-4pps is what makes that true
// for all four selectable row kinds instead of just rowBead/rowIssue.
//
// The active tab/pane decides which map IS the selection and which
// already-loaded slice its targets come from (activeSelection below):
// Beads/Issues/PRs all share m.trackerSelected, keyed by tracker id; the
// Branches pane keeps its own pre-existing m.selected, keyed by branch
// name, unchanged (so Clean Up's `x` still reads exactly what it always
// read). Three phases, two of them new here:
//
//  1. `space` on a rowBead/rowIssue/rowPR row toggles it into
//     trackerSelected — toggleTrackerSelection below. `space` on a
//     Branches row routes to the pane's own toggleBranchSelection instead.
//     Either selection is its own mode: its length IS the mode, so there's
//     nothing to leave "on" with nothing picked.
//  2. `w` with a nonempty active selection opens the picker overlay
//     (startWorkflowPicker): every declared selection-sourced workflow
//     (work.go's selectionWorkflows(), .24's own machinery filtering by
//     WorkflowSource), listed for the user to choose which one runs over
//     the selection. `w` with an EMPTY selection refuses instead of
//     opening an empty picker — the acceptance criterion "the workflow key
//     says so rather than dispatching nothing silently" (on the Branches
//     pane an empty selection instead falls through to the pre-existing
//     new-named-worktree prompt, since `w` there is overloaded).
//  3. Enter on a picked candidate (pickWorkflow) builds a []kinds.Target
//     straight from the current selection — no query, no resolve step,
//     since the targets are already exactly the rows the user pointed at —
//     and hands off into the SAME confirm overlay .26 built (wfOpen et al,
//     workflowconfirm.go): agent/ceiling shown, enter/y dispatches via the
//     same dispatchWorkflowCmd → kinds.DispatchTargets path a query-sourced
//     workflow already uses. Nothing here talks to kinds.DispatchTargets or
//     internal/livework directly — reusing that overlay is what keeps this
//     from being a second dispatch path.
package tui

import (
	"fmt"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
)

// activeSelection returns whichever map IS the selection right now — the
// single place that answers that question, so every reader (trackerTargets,
// the header count, the per-row checkbox) and every writer
// (toggleTrackerSelection, clearActiveSelection) agree. The Branches pane
// keeps its own pre-existing m.selected; every other tab shares
// m.trackerSelected.
func (m Model) activeSelection() map[string]bool {
	if m.active == tabLocal && m.pane == paneBranches {
		return m.selected
	}
	return m.trackerSelected
}

// clearActiveSelection empties whichever map activeSelection names, by
// assigning it a fresh map rather than deleting through the old one — same
// effect, and matches the shape workflowConfirmKey already used for
// trackerSelected alone.
func (m *Model) clearActiveSelection() {
	if m.active == tabLocal && m.pane == paneBranches {
		m.selected = map[string]bool{}
		return
	}
	m.trackerSelected = map[string]bool{}
}

// trackerSelectMode reports whether the active tab/pane is mid multi-select
// — same "map length is the mode" shape as the Branches pane's own
// selectMode, which is in fact what this reduces to on that pane.
func (m Model) trackerSelectMode() bool {
	return len(m.activeSelection()) > 0
}

// trackerRowID is the id a selectable tracker row toggles and dispatches
// under: a bead's own id, or a GitHub issue or PR number as a bare string
// (the same form ResolveQuery already gives a resolved bead's Target.Name —
// see kinds.ImplementPrompt) — the same identity fields rowID already uses
// for keymap.Bead/keymap.Issue/keymap.PR, so a selection id and an
// ADMIN_ROW_ID for the same row can never disagree. Empty for any other row
// kind, which toggleTrackerSelection treats as nothing to toggle. A branch
// row never reaches here: toggleTrackerSelection routes the Branches pane to
// toggleBranchSelection before trackerRowID is ever called, and no other tab
// renders a rowBranch row — its own identity lookup is rowID (rowkeys.go).
func trackerRowID(r rowItem) string {
	switch r.kind {
	case rowBead:
		return r.beadID
	case rowIssue:
		return strconv.Itoa(r.issueNum)
	case rowPR:
		return strconv.Itoa(r.prNum)
	default:
		return ""
	}
}

// toggleTrackerSelection flips the row under the cursor in and out of the
// active selection. On the Branches pane it routes straight to
// toggleBranchSelection so that path keeps its secMain / checked-out
// refusals and its status messages verbatim — this only adds the routing,
// it never re-implements them. Elsewhere, a row with no id (anything but
// rowBead/rowIssue/rowPR — a section header, a rule) leaves the selection
// exactly as it found it, mirroring toggleBranchSelection's own
// refuse-quietly contract for a row that can't be selected.
func (m *Model) toggleTrackerSelection() {
	if m.active == tabLocal && m.pane == paneBranches {
		m.toggleBranchSelection()
		return
	}
	r, ok := m.currentRow()
	if !ok {
		return
	}
	id := trackerRowID(r)
	if id == "" {
		return
	}
	sel := m.activeSelection()
	if sel[id] {
		delete(sel, id)
	} else {
		sel[id] = true
	}
}

// trackerTargets builds the []kinds.Target a confirmed dispatch runs over,
// straight from the active selection — reading the active tab/pane's own
// already-loaded data (m.beads, m.remote.Issues, m.remote.PRs, or
// m.local.branches) rather than the currently rendered rows, so scrolling
// the list — or a Branches `/` filter or `g` minesOnly — before pressing
// `w` can never drop a selected row that's hidden or scrolled out of view.
// Order follows that underlying data's own order, which is what keeps this
// deterministic without a second, selection-order structure to maintain
// alongside the map.
func (m Model) trackerTargets() []kinds.Target {
	var targets []kinds.Target
	switch m.active {
	case tabBeads:
		for _, b := range m.beads {
			if m.trackerSelected[b.ID] {
				targets = append(targets, kinds.Target{Name: b.ID, Prompt: kinds.ImplementPrompt(b.ID)})
			}
		}
	case tabIssues:
		for _, it := range m.remote.Issues {
			id := strconv.Itoa(it.Number)
			if m.trackerSelected[id] {
				targets = append(targets, kinds.Target{Name: id, Prompt: kinds.ImplementPrompt(id)})
			}
		}
	case tabPRs:
		for _, pr := range m.remote.PRs {
			id := strconv.Itoa(pr.Number)
			if m.trackerSelected[id] {
				targets = append(targets, kinds.Target{Name: id, Prompt: kinds.ImplementPrompt(id)})
			}
		}
	case tabLocal:
		if m.pane != paneBranches {
			return nil
		}
		for _, b := range m.local.branches {
			if m.selected[b.Name] {
				targets = append(targets, kinds.Target{Name: b.Name, Prompt: kinds.ImplementPrompt(b.Name)})
			}
		}
	}
	return targets
}

// startWorkflowPicker handles `w` with a nonempty active selection: refuses outright
// (no overlay, nothing dispatched) with an empty selection or with no
// selection-sourced workflow declared at all — the acceptance criterion
// "with no rows selected, the workflow key says so rather than dispatching
// nothing silently" — otherwise opens the picker over every candidate.
func (m Model) startWorkflowPicker() (tea.Model, tea.Cmd) {
	if !m.trackerSelectMode() {
		m.setStatus("no rows selected — space to select, then w to run a workflow over them", cYellow)
		return m, nil
	}
	candidates := m.selectionWorkflows()
	if len(candidates) == 0 {
		m.setStatus("no selection-sourced workflow is declared in admin.toml", cYellow)
		return m, nil
	}
	m.swOpen = true
	m.swOptions = candidates
	m.swCursor = 0
	return m, nil
}

// workflowPickerKey handles every key while the picker overlay is up:
// ↑/↓ (or j/k) move, enter picks, everything else (esc included) cancels
// having dispatched nothing.
func (m Model) workflowPickerKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.swCursor > 0 {
			m.swCursor--
		}
		return m, nil
	case "down", "j":
		if m.swCursor < len(m.swOptions)-1 {
			m.swCursor++
		}
		return m, nil
	case "enter":
		cr := m.swOptions[m.swCursor]
		m.swOpen = false
		m.swOptions = nil
		return m.pickWorkflow(cr)
	default:
		m.swOpen = false
		m.swOptions = nil
		m.setStatus("cancelled", cDim)
		return m, nil
	}
}

// pickWorkflow hands the picker's choice straight to the confirm overlay
// .26 built (workflowconfirm.go): the targets are already known (the
// current tracker selection, built once here rather than re-derived on
// every repaint), so this skips wfResolving entirely and opens wfOpen
// showing them right away — the same agent/ceiling/error rules
// workflowResolved applies to a query's result apply here too, just
// computed synchronously since there is no query to wait on.
func (m Model) pickWorkflow(cr workCmdRow) (tea.Model, tea.Cmd) {
	action, ok := m.soleAction(cr.cmd)
	if !ok {
		// Defensive: selectionWorkflows only ever offers a single-step
		// kind="workflow" command, so this can't happen outside a bug
		// changing that filter without updating this assumption.
		m.setStatus(cr.name+": not a workflow", cRed)
		return m, nil
	}
	targets := m.trackerTargets()

	m.wfResolving = false
	m.wfOpen = true
	m.wfFromSelection = true
	m.wfName = cr.name
	m.wfAction = action
	m.wfQuery = fmt.Sprintf("%d selected row(s)", len(targets))
	m.wfTargets = targets

	cfg, err := config.Load(m.repoRoot)
	if err != nil {
		cfg = config.Config{}
	}
	agent, agentOK := kinds.ResolveAgent(action, cfg)
	m.wfAgent = agent
	m.wfCeiling = kinds.Ceiling(cfg)
	switch {
	case len(targets) == 0:
		// Can't happen through startWorkflowPicker (it refuses an empty
		// selection before the picker ever opens), but trackerTargets
		// reads live state a moment later than that check did — the
		// selected rows could in principle have vanished from m.beads/
		// m.remote.Issues in between. Same "nothing to confirm" message
		// workflowResolved gives an empty query match.
		m.wfErr = "selection matched nothing"
	case !agentOK:
		m.wfErr = fmt.Sprintf("no agent configured — set 'agent' on %q or [workflow].agent in admin.toml", cr.name)
	default:
		m.wfErr = ""
	}
	return m, nil
}

// renderWorkflowPickerBody draws the picker as a centered box, the same
// boxPanel every other overlay in this package draws with — see
// workflowconfirm.go's renderWorkflowConfirmBody for the identical
// centering math this mirrors.
func (m Model) renderWorkflowPickerBody(width, height int) []string {
	lines := make([][]seg, len(m.swOptions))
	for i, cr := range m.swOptions {
		lines[i] = []seg{{t: " " + cr.name, fg: cFg}, {t: "  " + cr.cmd.Desc, fg: cFaint}}
	}

	boxW := clampi(30, width-8, 76)
	if boxW > width {
		boxW = width
	}
	boxH := clampi(3, len(lines)+2, height)

	box := boxPanel{
		title: fmt.Sprintf("Run a workflow over %d selected", len(m.activeSelection())),
		width: boxW, height: boxH, focused: true,
		lines: lines, active: m.swCursor,
		footer: []seg{{t: "enter pick", fg: cCyan}, {t: "   esc cancel", fg: cFaint}},
	}
	rendered := box.render()

	lp := padN(clampi(0, (width-boxW)/2, width))
	top := clampi(0, (height-boxH)/2, height)
	out := make([]string, height)
	for i := top; i < height && i-top < len(rendered); i++ {
		out[i] = lp + rendered[i-top]
	}
	return out
}
