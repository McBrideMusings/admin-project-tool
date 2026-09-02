// quitconfirm.go is admin-3rgp.63: the dashboard now hosts processes on its
// own pseudo-terminals (admin-3rgp.61), so quitting can no longer be silent —
// a hosted dev server dies with a reflex keypress unless the user sees it
// coming first.
//
// The rule is narrow on purpose: only a PTY-addressed, non-worker entry in
// StateRunning or StateWaiting counts as "hosted" here. A pane-addressed
// entry (herdr pane) and an agent worker both outlive the dashboard by
// design — they were dispatched precisely so a plain shell's lifetime-aware
// router could open a pane for them, not this process's. Quitting must never
// warn about, let alone kill, either of those.
//
// Shape mirrors workflowconfirm.go: a snapshot taken when the confirm opens
// (quitHosted) so accepting kills exactly what was shown even if the
// registry changes under it, one key handler (quitConfirmKey) that owns the
// keyboard while the overlay is up, and one body renderer reusing boxPanel.
package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// hostedRunningEntries returns every live-work entry this process itself is
// hosting on a pty and that is still doing something — the set quit must
// warn about and, on confirm, kill. Via == "" is the explicit "do not infer
// hosted-ness from an empty address field" rule from admin-3rgp.61: a
// launcher-dispatched entry has Via set to the launcher's name, so testing
// Via directly (rather than some other field) is what keeps this correct
// however many launcher kinds are ever added. Worker == nil drops agent
// workers, which are launcher-dispatched anyway (belt-and-braces).
// Registry.List returns entries in insertion order, so the result — and
// the confirm listing built from it — is deterministic.
func hostedRunningEntries() []*livework.Entry {
	var out []*livework.Entry
	for _, e := range livework.Default.List() {
		if e.Via != "" || e.Worker != nil {
			continue
		}
		st, _ := livework.Default.State(e.ID)
		if st == livework.StateRunning || st == livework.StateWaiting {
			out = append(out, e)
		}
	}
	return out
}

// requestQuit is the "q"/"ctrl+c" handler's actual body. Nothing hosted:
// quit exactly as before, no confirm. Something hosted: open the overlay
// over a snapshot of what's running and wait — nothing is killed yet.
func (m Model) requestQuit() (tea.Model, tea.Cmd) {
	hosted := hostedRunningEntries()
	if len(hosted) == 0 {
		m.closeWatcher()
		return m, tea.Quit
	}
	m.quitOpen = true
	m.quitHosted = hosted
	m.setStatus(fmt.Sprintf("%d hosted process(es) still running", len(hosted)), cYellow)
	return m, nil
}

// quitConfirmKey handles every key while the quit-confirm overlay is up.
// enter/y/Y kills every snapshotted entry via the registry's own Kill (which
// routes to the entry's Ops.Kill — admin-3rgp.62 owns that operation's
// semantics, this file only calls it) and then quits exactly like the
// no-hosted-process path. Any other key cancels: the overlay closes, nothing
// is killed, and every entry is still StateRunning/StateWaiting because
// nothing touched it.
func (m Model) quitConfirmKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter", "y", "Y":
		hosted := m.quitHosted
		m.quitOpen = false
		m.quitHosted = nil
		failed := 0
		for _, e := range hosted {
			if err := livework.Default.Kill(e.ID); err != nil {
				failed++
			}
		}
		if failed > 0 {
			m.setStatus(fmt.Sprintf("%d of %d kill(s) failed", failed, len(hosted)), cRed)
		}
		m.closeWatcher()
		return m, tea.Quit
	default:
		m.quitOpen = false
		m.quitHosted = nil
		m.setStatus("cancelled", cDim)
		return m, nil
	}
}

// renderQuitConfirmBody draws the overlay as a centered box, reusing the
// same boxPanel the workflow-confirm and delete-confirm overlays already
// draw with (panel.go) — no new chrome, just this overlay's own content.
func (m Model) renderQuitConfirmBody(width, height int) []string {
	title, badge, lines := m.quitConfirmContent()

	boxW := clampi(30, width-8, 76)
	if boxW > width {
		boxW = width
	}
	boxH := clampi(3, len(lines)+2, height)

	footer := []seg{{t: "enter kill & quit", fg: cRed}, {t: "   esc cancel", fg: cFaint}}

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

// quitConfirmContent builds the overlay's title, badge, and body lines from
// the snapshot taken when the overlay opened. The state color mirrors
// renderLiveState's own switch (workview.go) — that helper isn't reused
// directly because it returns an already-ANSI-styled string, and nesting
// that inside a seg's own fg style would double-apply color rather than let
// boxPanel's segment renderer own it.
func (m Model) quitConfirmContent() (title, badge string, lines [][]seg) {
	for _, e := range m.quitHosted {
		st, _ := livework.Default.State(e.ID)
		stateColor := cDim
		switch st {
		case livework.StateRunning:
			stateColor = cGreen
		case livework.StateWaiting:
			stateColor = cYellow
		}
		lines = append(lines, []seg{
			{t: " • ", fg: cFaint},
			{t: e.Name, fg: cGreen},
			{t: "  "},
			{t: string(st), fg: stateColor},
		})
	}
	badge = fmt.Sprintf("%d hosted", len(m.quitHosted))
	title = fmt.Sprintf("Quit — %d hosted process(es) still running?", len(m.quitHosted))
	return title, badge, lines
}
