// workflowdispatch.go runs a kind="workflow" dispatch off the UI goroutine:
// ↵ on one matched item of a query workflow's target group (work.go's
// dispatchWorkflowTarget) and a row-scoped workflow binding
// (rowkeys.go's rowKeyWorkflow) both hand their targets to
// dispatchWorkflowCmd.
//
// DispatchTargets itself already registers each target into
// internal/livework as soon as its pane spawns (internal/kinds/
// workflowdispatch.go), so dispatched targets show up as Worker rows via the
// existing workTickCmd repaint — this file never polls or renders their
// progress itself.
package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// workflowDispatchDoneMsg carries a dispatch's outcome back to Update, once
// every target has run to completion (or failed) — see dispatchWorkflowCmd's
// own comment for why that can be a long wait that still never blocks the UI.
type workflowDispatchDoneMsg struct {
	name    string
	results []kinds.TargetResult
}

// runDispatchWorkflow is the seam dispatchWorkflowCmd calls through —
// kinds.DispatchTargets by default, swapped out in tests so a test can prove
// exactly which targets got dispatched without creating a single real git
// worktree (DispatchTargets's phase 1 is real `git worktree add` calls, which
// have no business running off a fabricated in-memory Model).
var runDispatchWorkflow = kinds.DispatchTargets

// dispatchWorkflowCmd runs targets via runDispatchWorkflow in the
// background. The real implementation (kinds.DispatchTargets) blocks its own
// caller until every target has spawned (a window worker holds its
// concurrency-ceiling slot until kinds.ReleaseWorkerSlot frees it — see
// workflowdispatch.go), which can be a long time when targets outnumber the ceiling;
// running it as a tea.Cmd is what keeps that wait off the UI goroutine; the
// dashboard keeps repainting from workTickCmd and the live registry each
// target registers into as it starts.
func dispatchWorkflowCmd(repoRoot, name string, action manifest.Action, targets []kinds.Target) tea.Cmd {
	return func() tea.Msg {
		results := runDispatchWorkflow(repoRoot, name, action, targets, kinds.SurfaceDashboard)
		return workflowDispatchDoneMsg{name: name, results: results}
	}
}

// workflowDispatchDone reports a dispatch's final tally on the status line.
func (m *Model) workflowDispatchDone(msg workflowDispatchDoneMsg) {
	ok, failed := 0, 0
	var first error
	for _, r := range msg.results {
		if r.Err != nil {
			failed++
			if first == nil {
				first = fmt.Errorf("%s: %w", r.Target, r.Err)
			}
		} else {
			ok++
		}
	}
	if failed == 0 {
		m.setStatus(fmt.Sprintf("%s: %d target(s) finished", msg.name, ok), cGreen)
	} else {
		m.setStatus(fmt.Sprintf("%s: %d finished, %d failed — %v", msg.name, ok, failed, first), cRed)
	}
}
