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
// Shape: a snapshot taken when the confirm opens
// (quitHosted) so accepting kills exactly what was shown even if the
// registry changes under it, one key handler (quitConfirmKey) that owns the
// keyboard while the overlay is up, and one renderer (overlayQuitConfirm)
// drawing the box over the finished frame.
package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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

// quitBoxInner is the confirm box's inner width — the design frame's
// (docs/spikes/admin-tui-desktop/variants.go's overlayQuit), narrowed only
// when the terminal cannot fit it.
const quitBoxInner = 60

// overlayQuitConfirm draws the confirm box over the finished frame, so the
// list and band stay visible around it, the way the design frame
// (docs/spikes/admin-tui-desktop/hosted-quit.txt) places it: centered on the
// whole screen, never over the top three rows (header, tabs, separator) or
// the bottom two (rule, keybar). The box gets at most the rows between those,
// and when it is that tall it sits flush against them rather than centered, so
// its title and action row are always drawn. The splice is cell-based
// (ansi.Cut), so the rows under the box keep their own colors either side of it.
func (m Model) overlayQuitConfirm(lines []string, width int) []string {
	bw := clampi(1, quitBoxInner, width-2)
	box := m.quitConfirmBox(bw, len(lines)-5)
	top := clampi(3, (len(lines)-len(box))/2, len(lines)-2-len(box))
	left := maxi((width-bw-2)/2, 0)
	for i, l := range box {
		y := top + i
		if y < 3 || y >= len(lines)-2 {
			continue
		}
		base := lines[y]
		if gap := width - ansi.StringWidth(base); gap > 0 {
			base += padN(gap)
		}
		lines[y] = ansi.Cut(base, 0, left) + l + ansi.Cut(base, left+bw+2, width)
	}
	return lines
}

// quitBoxChrome is the confirm box's height around its process list: the two
// borders, the title and the blank under it, and the four rows after the list
// (blank, workers reminder, blank, actions).
const quitBoxChrome = 8

// quitConfirmBox renders the box rows at inner width bw, at most maxH rows
// tall, from the snapshot taken when the confirm opened: a title counting
// what will die, one row per hosted process (state, command, what it is
// doing), the reminder that workers survive, and the two actions. A list too
// long for maxH shows its first processes and ends on a "+N more running"
// row; the title still counts all of them, and accepting kills all of them.
func (m Model) quitConfirmBox(bw, maxH int) []string {
	n := len(m.quitHosted)
	noun := "processes"
	if n == 1 {
		noun = "process"
	}

	shown := m.quitHosted
	if room := maxi(maxH-quitBoxChrome, 1); n > room {
		shown = shown[:room-1]
	}

	// The command column fits the longest name, never narrower than the
	// design's 10 cells, so the activity column lines up across rows.
	nameW := 10
	for _, e := range shown {
		nameW = maxi(nameW, vlen(m.entryName(e))+2)
	}

	faint := lipgloss.NewStyle().Foreground(cFaint)
	row := func(s string) string {
		return faint.Render("│") + padEnd(ansi.Truncate(s, bw, ""), bw) + faint.Render("│")
	}
	box := []string{faint.Render("┌" + strings.Repeat("─", bw) + "┐")}
	box = append(box,
		row("  "+lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("Quit — %d %s still running", n, noun))),
		row(""))
	for _, e := range shown {
		st, _ := livework.Default.State(e.ID)
		stateColor := cDim
		switch st {
		case livework.StateRunning:
			stateColor = cGreen
		case livework.StateWaiting:
			stateColor = cYellow
		}
		line := "    " + lipgloss.NewStyle().Foreground(stateColor).Render(padEnd(string(st), 9)) +
			padEnd(m.entryName(e), nameW)
		if act, _ := livework.Default.Activity(e.ID); act != "" {
			// The design leaves 5 cells spare after a 32-cell activity at
			// the 10-cell command column (variants.go's overlayQuit: bw-28).
			line += mutedStyle.Render(truncR(act, maxi(bw-18-nameW, 1)))
		}
		box = append(box, row(line))
	}
	if more := n - len(shown); more > 0 {
		box = append(box, row("    "+mutedStyle.Render(fmt.Sprintf("+%d more running", more))))
	}
	box = append(box,
		row(""),
		row("    "+mutedStyle.Render("Workers keep running — they have their own panes.")),
		row(""),
		row("    "+lipgloss.NewStyle().Foreground(cRed).Render("⏎ kill them and quit")+"     "+mutedStyle.Render("esc cancel")),
		faint.Render("└"+strings.Repeat("─", bw)+"┘"))
	return box
}
