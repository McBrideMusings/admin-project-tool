// workflowconfirm.go is admin-3rgp.26: letting a query-sourced kind="workflow"
// command (Work tab) run bare, but never without the user having seen what it
// resolved to first. Three phases, one overlay:
//
//  1. Enter on the row (work.go's activateCommand → startWorkflowConfirm)
//     kicks off resolveWorkflowCmd and sets wfResolving — the overlay is up,
//     nothing to show yet, no key does anything but wait.
//  2. workflowResolvedMsg lands (workflowResolved) and the overlay switches to
//     showing the resolved set — or, when the query errored or matched
//     nothing, saying so instead of a set.
//  3. A key decides it: enter/y on a real resolved set hands it straight to
//     kinds.DispatchTargets via dispatchWorkflowCmd (workflowConfirmKey);
//     anything else — esc, n, any key at all over an error/empty result —
//     closes the overlay having dispatched nothing.
//
// DispatchTargets itself already registers each target into
// internal/livework as soon as its agent starts (internal/kinds/
// workflowdispatch.go), so once confirmed, targets show up as Worker rows on
// this same Work tab via the existing workTickCmd repaint — this file never
// polls or renders their progress itself.
package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// workflowResolvedMsg carries a query's resolution back to Update. agent/
// agentOK/ceiling are resolved alongside the targets (not deferred to
// dispatch time) so the confirm screen shows the same agent and ceiling
// DispatchTargets will actually use — reading config.Load fresh here rather
// than trusting a possibly-stale m.cfgMerged, matching what DispatchTargets
// itself does.
type workflowResolvedMsg struct {
	name    string
	action  manifest.Action
	query   string
	targets []kinds.Target
	err     error
	agent   string
	agentOK bool
	ceiling int
}

// workflowDispatchDoneMsg carries a confirmed dispatch's outcome back to
// Update, once every target has run to completion (or failed) — see
// dispatchWorkflowCmd's own comment for why that can be a long wait that
// still never blocks the UI.
type workflowDispatchDoneMsg struct {
	name    string
	results []kinds.TargetResult
}

// runResolveQuery is the seam resolveWorkflowCmd calls through —
// kinds.ResolveQuery by default, swapped out in tests so a resolve test can
// prove the model transition without shelling out to a real `bd` binary
// against whatever happens to be at repoRoot (in a hand-built test Model,
// repoRoot is often empty, which would otherwise resolve to this very
// process's own working directory).
var runResolveQuery = kinds.ResolveQuery

// resolveWorkflowCmd resolves query against repoRoot's tracker in the
// background. Nothing here dispatches anything — runResolveQuery is a read,
// and the agent/ceiling lookup below is config.Load (repoRoot's admin.toml,
// merged with its archetypes), also a read.
func resolveWorkflowCmd(repoRoot, name string, action manifest.Action, query string) tea.Cmd {
	return func() tea.Msg {
		targets, err := runResolveQuery(context.Background(), repoRoot, query)
		msg := workflowResolvedMsg{name: name, action: action, query: query, targets: targets, err: err}
		if err == nil {
			cfg, cfgErr := config.Load(repoRoot)
			if cfgErr != nil {
				cfg = config.Config{}
			}
			msg.agent, msg.agentOK = kinds.ResolveAgent(action, cfg)
			msg.ceiling = kinds.Ceiling(cfg)
		}
		return msg
	}
}

// runDispatchWorkflow is the seam dispatchWorkflowCmd calls through —
// kinds.DispatchTargets by default, swapped out in tests so a confirm-key
// test can prove exactly which targets got dispatched without creating a
// single real git worktree (DispatchTargets's phase 1 is real `git worktree
// add` calls, which have no business running off a fabricated in-memory
// Model).
var runDispatchWorkflow = kinds.DispatchTargets

// dispatchWorkflowCmd runs targets via runDispatchWorkflow in the
// background. The real implementation (kinds.DispatchTargets) blocks its own
// caller until every target has settled (each dispatchOne's runAgent wait is
// what frees the target's concurrency-ceiling slot — see
// workflowdispatch.go), which can be a long time for a real agent run;
// running it as a tea.Cmd is what keeps that wait off the UI goroutine; the
// dashboard keeps repainting from workTickCmd and the live registry each
// target registers into as it starts.
func dispatchWorkflowCmd(repoRoot, name string, action manifest.Action, targets []kinds.Target) tea.Cmd {
	return func() tea.Msg {
		results := runDispatchWorkflow(repoRoot, name, action, targets)
		return workflowDispatchDoneMsg{name: name, results: results}
	}
}

// startWorkflowConfirm handles enter on a query-sourced workflow row: opens
// the overlay in its resolving phase and kicks off the resolution. wd.workflow
// and wd.workflowAction are only ever set together (see work.go's
// commandRowItem).
//
// declaredCommands already excludes a selection-sourced workflow from the
// Work tab entirely (WorkflowTarget.AppearsInCommandList), so wd.workflow
// here should always be query-sourced — but this is the one place a bare
// enter could turn a selection-sourced workflow into a query dispatch with
// an empty query if that filter were ever bypassed, so the source is
// checked again rather than trusted, and a non-query source is refused
// outright (no resolution, no overlay, nothing dispatched) instead of
// resolving an empty string.
func (m Model) startWorkflowConfirm(wd *workRowData) (tea.Model, tea.Cmd) {
	if wd.workflow.Source != kinds.WorkflowSourceQuery {
		m.setStatus(wd.cmdName+" is selection-sourced — it cannot run bare", cYellow)
		return m, nil
	}
	m.wfResolving = true
	m.wfOpen = false
	m.wfFromSelection = false
	m.wfName = wd.cmdName
	m.wfAction = wd.workflowAction
	m.wfQuery = wd.workflow.Query
	m.wfTargets = nil
	m.wfErr = ""
	m.wfAgent = ""
	m.wfCeiling = 0
	m.setStatus("resolving "+wd.workflow.Query+"…", cDim)
	return m, resolveWorkflowCmd(m.repoRoot, wd.cmdName, wd.workflowAction, wd.workflow.Query)
}

// workflowResolved applies a resolution result to the model. A msg for a
// workflow the user has since backed out of (wfResolving already false, or a
// different row's msg racing this one) is dropped rather than reopening an
// overlay nobody asked for anymore.
func (m *Model) workflowResolved(msg workflowResolvedMsg) {
	if !m.wfResolving || msg.name != m.wfName {
		return
	}
	m.wfResolving = false
	m.wfOpen = true
	m.wfTargets = msg.targets
	m.wfAgent, m.wfCeiling = msg.agent, msg.ceiling
	switch {
	case msg.err != nil:
		m.wfErr = msg.err.Error()
	case len(msg.targets) == 0:
		m.wfErr = fmt.Sprintf("query %q matched nothing", msg.query)
	case !msg.agentOK:
		// Same "fail loud, don't guess" contract DispatchTargets itself
		// enforces (ResolveAgent's ok==false path) — surfaced here instead of
		// only at dispatch time so an unconfigured agent doesn't waste the
		// user's confirmation on a set that would immediately fail dispatch.
		m.wfErr = fmt.Sprintf(
			"no agent configured — set 'agent' on %q or [workflow].agent in admin.toml", msg.name)
	default:
		m.wfErr = ""
	}
}

// workflowDispatchDone reports a confirmed dispatch's final tally on the
// status line.
func (m *Model) workflowDispatchDone(msg workflowDispatchDoneMsg) {
	ok, failed := 0, 0
	for _, r := range msg.results {
		if r.Err != nil {
			failed++
		} else {
			ok++
		}
	}
	if failed == 0 {
		m.setStatus(fmt.Sprintf("%s: %d target(s) finished", msg.name, ok), cGreen)
	} else {
		m.setStatus(fmt.Sprintf("%s: %d finished, %d failed", msg.name, ok, failed), cRed)
	}
}

// workflowConfirmKey handles every key while the overlay is up. During
// wfResolving every key is swallowed — there is nothing yet to confirm or
// cancel. Once resolved: enter/y on a real, dispatchable set (no wfErr) hands
// it to dispatchWorkflowCmd; every other key — including enter/y over an
// error or empty result, where there is nothing valid to confirm — closes the
// overlay having dispatched nothing, matching the acceptance criterion that
// cancelling dispatches nothing.
func (m Model) workflowConfirmKey(key string) (tea.Model, tea.Cmd) {
	if m.wfResolving {
		return m, nil
	}
	canDispatch := m.wfErr == "" && len(m.wfTargets) > 0
	if canDispatch && (key == "enter" || key == "y" || key == "Y") {
		name, action, targets := m.wfName, m.wfAction, m.wfTargets
		m.wfOpen = false
		if m.wfFromSelection {
			// admin-3rgp.27: only a selection-sourced dispatch owns
			// trackerSelected — a query-sourced one (wfFromSelection false)
			// never touched it and must leave it alone. Cleared only once
			// dispatch is actually confirmed, not when the picker merely
			// opened this overlay, so backing out with esc/n leaves the
			// selection intact for a second try at a different workflow.
			m.trackerSelected = map[string]bool{}
			m.wfFromSelection = false
		}
		m.setStatus(fmt.Sprintf("dispatching %s over %d target(s)…", name, len(targets)), cCyan)
		return m, dispatchWorkflowCmd(m.repoRoot, name, action, targets)
	}
	m.wfOpen = false
	m.wfFromSelection = false
	m.setStatus("cancelled", cDim)
	return m, nil
}

// renderWorkflowConfirmBody draws the overlay as a centered box, reusing the
// same boxPanel the delete-confirm and bead overlays already draw with
// (panel.go) — no new chrome, just this overlay's own content.
func (m Model) renderWorkflowConfirmBody(width, height int) []string {
	title, badge, lines := m.workflowConfirmContent()

	boxW := clampi(30, width-8, 76)
	if boxW > width {
		boxW = width
	}
	boxH := clampi(3, len(lines)+2, height)

	footer := []seg{{t: "esc cancel", fg: cFaint}}
	if m.wfErr == "" && !m.wfResolving && len(m.wfTargets) > 0 {
		footer = []seg{{t: "enter dispatch", fg: cCyan}, {t: "   esc cancel", fg: cFaint}}
	}

	box := boxPanel{
		title: title, badge: badge, width: boxW, height: boxH, focused: true,
		lines: lines, active: -1, footer: footer,
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

// workflowConfirmContent builds the overlay's title, badge, and body lines
// for whichever phase is current — resolving, an error/empty result, or a
// real resolved set ready to confirm.
func (m Model) workflowConfirmContent() (title, badge string, lines [][]seg) {
	if m.wfResolving {
		return "Resolving " + m.wfName, "", [][]seg{
			{{t: " " + m.wfQuery, fg: cFaint}},
		}
	}
	if m.wfErr != "" {
		return "Dispatch " + m.wfName + "?", "", [][]seg{
			{{t: " " + m.wfErr, fg: cYellow}},
		}
	}

	// The label names what wfQuery actually holds: a real query string for a
	// query-sourced workflow, or admin-3rgp.27's own "N selected row(s)" for
	// a picker-originated one — "query" would misdescribe the latter.
	srcLabel := " query   "
	if m.wfFromSelection {
		srcLabel = " over    "
	}
	lines = append(lines, []seg{{t: srcLabel, fg: cFaint}, {t: m.wfQuery, fg: cFg}})
	agent := m.wfAgent
	if agent == "" {
		agent = "(none)"
	}
	lines = append(lines, []seg{{t: " agent   ", fg: cFaint}, {t: agent, fg: cFg}})
	lines = append(lines, []seg{{t: " ceiling ", fg: cFaint}, {t: fmt.Sprintf("%d at once", m.wfCeiling), fg: cFg}})
	lines = append(lines, []seg{{t: ""}})
	for _, tgt := range m.wfTargets {
		lines = append(lines, []seg{{t: " • ", fg: cFaint}, {t: tgt.Name, fg: cGreen}})
	}
	badge = fmt.Sprintf("%d target(s)", len(m.wfTargets))
	return "Dispatch " + m.wfName + "?", badge, lines
}
