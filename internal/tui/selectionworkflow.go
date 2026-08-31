// selectionworkflow.go is admin-3rgp.27: letting a selection-sourced
// kind="workflow" command run over rows the user has picked on the Beads or
// Issues tab — the row-only half of the "target source" split .24 defined
// and .26 already built the OTHER half of (a query-sourced workflow running
// bare, with a resolve/confirm/dispatch overlay). Three phases, two of them
// new here:
//
//  1. `space` on a rowBead/rowIssue row toggles it into trackerSelected —
//     toggleTrackerSelection below. The selection is its own mode exactly
//     like the Branches pane's `selected` (model.go): its length IS the
//     mode, so there's nothing to leave "on" with nothing picked.
//  2. `w` with a nonempty trackerSelected opens the picker overlay
//     (startWorkflowPicker): every declared selection-sourced workflow
//     (work.go's selectionWorkflows(), .24's own machinery filtering by
//     WorkflowSource), listed for the user to choose which one runs over
//     the selection. `w` with an EMPTY trackerSelected refuses instead of
//     opening an empty picker — the acceptance criterion "the workflow key
//     says so rather than dispatching nothing silently."
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

// trackerSelectMode reports whether the Beads/Issues tab is mid multi-select
// — same "map length is the mode" shape as the Branches pane's selectMode.
func (m Model) trackerSelectMode() bool {
	return len(m.trackerSelected) > 0
}

// trackerRowID is the id a rowBead/rowIssue row selects and dispatches
// under: a bead's own id, or a GitHub issue's number as a bare string (the
// same form ResolveQuery already gives a resolved bead's Target.Name — see
// kinds.ImplementPrompt). Empty for any other row kind, which
// toggleTrackerSelection treats as nothing to toggle.
func trackerRowID(r rowItem) string {
	switch r.kind {
	case rowBead:
		return r.beadID
	case rowIssue:
		return strconv.Itoa(r.issueNum)
	default:
		return ""
	}
}

// toggleTrackerSelection flips the row under the cursor in and out of
// trackerSelected. A row with no id (anything but rowBead/rowIssue — a
// section header, a rule) leaves the selection exactly as it found it,
// mirroring toggleBranchSelection's own refuse-quietly contract for a row
// that can't be selected.
func (m *Model) toggleTrackerSelection() {
	r, ok := m.currentRow()
	if !ok {
		return
	}
	id := trackerRowID(r)
	if id == "" {
		return
	}
	if m.trackerSelected[id] {
		delete(m.trackerSelected, id)
	} else {
		m.trackerSelected[id] = true
	}
}

// trackerTargets builds the []kinds.Target a confirmed dispatch runs over,
// straight from trackerSelected — reading the tab's own already-loaded data
// (m.beads or m.remote.Issues) rather than the currently rendered rows, so
// scrolling the list before pressing `w` can never drop a selected row that
// scrolled out of view. Order follows that underlying data's own order,
// which is what keeps this deterministic without a second, selection-order
// structure to maintain alongside the map.
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
	}
	return targets
}

// startWorkflowPicker handles `w` on the Beads/Issues tab: refuses outright
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
		m.wfErr = fmt.Sprintf("no agent configured — set 'agent' on %q or [workflow].agent in config", cr.name)
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
		title: fmt.Sprintf("Run a workflow over %d selected", len(m.trackerSelected)),
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
