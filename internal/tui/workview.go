// workview.go renders the Work tab (admin-3rgp.20): the combined
// command+worker list in the left two thirds under one cursor, and a detail
// pane in the right third whose shape follows the row's kind. work.go is the
// data half this reads.
package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// renderWorkBody lays out the Work tab: the row list at roughly two thirds
// of the width, a "│" divider, and the cursor row's detail pane filling the
// rest — the ticket's "a detail pane on the right third whose shape follows
// the row's kind."
func (m Model) renderWorkBody(width, height int) ([]string, []hitTarget) {
	rows := m.visibleRows()
	cursor := m.cursor[m.active]

	if len(rows) == 1 && rows[0].kind == rowCenter {
		return centerMessage(rows[0].label, width, height), noneHits(height)
	}
	if len(rows) == 0 {
		return pad([]string{"  " + mutedStyle.Render("(nothing here)")}, height),
			padHits([]hitTarget{{kind: hitNone}}, height)
	}

	lw := width * 2 / 3
	if lw > width-14 {
		lw = width - 14
	}
	if lw < 20 {
		lw = width
	}
	rw := width - lw - 2
	if rw < 0 {
		rw = 0
	}

	left := make([]string, len(rows))
	hits := make([]hitTarget, len(rows))
	for i, r := range rows {
		switch r.kind {
		case rowHeader:
			left[i] = " " + mutedStyle.Render(r.label)
			hits[i] = hitTarget{kind: hitNone}
		case rowText:
			left[i] = ""
			hits[i] = hitTarget{kind: hitNone}
		default:
			left[i] = m.renderWorkRow(r, i == cursor, lw)
			hits[i] = hitTarget{kind: hitRow, row: i}
		}
	}

	start, end := window(len(left), cursor, height)
	leftLines := append([]string{}, left[start:end]...)
	hitLines := append([]hitTarget{}, hits[start:end]...)
	if start > 0 && len(leftLines) > 0 {
		leftLines[0] = faintStyle.Render("  ↑ " + strconv.Itoa(start) + " more")
		hitLines[0] = hitTarget{kind: hitNone}
	}
	if end < len(left) && len(leftLines) > 0 {
		last := len(leftLines) - 1
		leftLines[last] = faintStyle.Render("  ↓ more")
		hitLines[last] = hitTarget{kind: hitNone}
	}

	right := m.renderWorkDetail(rows[cursor], rw)

	out := make([]string, height)
	outHits := make([]hitTarget, height)
	divider := faintStyle.Render("│ ")
	for i := 0; i < height; i++ {
		l, hit := "", hitTarget{kind: hitNone}
		if i < len(leftLines) {
			l, hit = leftLines[i], hitLines[i]
		}
		r := ""
		if i < len(right) {
			r = right[i]
		}
		out[i] = " " + padEnd(l, lw) + divider + r
		outHits[i] = hit
	}
	return out, outHits
}

// renderWorkRow renders one command or worker row: a cursor marker, the
// name, then either the description (idle) or the live state, activity and
// pane (running) — never both at once, per the ticket's "last run is NOT in
// the list."
func (m Model) renderWorkRow(r rowItem, selected bool, width int) string {
	wd := r.work
	if wd == nil {
		return ""
	}
	marker := faintStyle.Render("◇ ")
	if selected {
		marker = lipgloss.NewStyle().Foreground(cCyan).Render("◆ ")
	}
	const nameW, stateW = 16, 9
	rest := width - 2 - nameW
	if rest < 6 {
		rest = 6
	}

	if r.kind == rowWorker {
		e := wd.worker
		name := padEnd(truncR(e.ID, nameW), nameW)
		state := renderLiveState(e.State)
		act := truncR(e.Activity, maxi(rest-stateW-1, 4))
		return marker + name + state + " " + faintStyle.Render(act)
	}

	name := padEnd(truncR(wd.cmdName, nameW), nameW)
	if wd.live != nil {
		state := renderLiveState(wd.live.State)
		pane := ""
		if wd.live.PaneID != "" {
			pane = " " + wd.live.PaneID
		}
		actW := maxi(rest-stateW-1-vlen(pane), 4)
		act := padEnd(truncR(wd.live.Activity, actW), actW)
		return marker + name + state + " " + faintStyle.Render(act) + faintStyle.Render(pane)
	}
	kind := padEnd(truncR(wd.cmdKind, stateW), stateW)
	desc := truncR(wd.cmdDesc, maxi(rest-stateW, 4))
	return marker + name + faintStyle.Render(kind+desc)
}

// renderWorkDetail is the polymorphic half: a command shows
// kind/archetype/last-run/log/steps-with-archetype, a worker shows
// state/current-activity/branch/worktree/elapsed/actions. They share almost
// no fields, so this dispatches on the row's own kind rather than trying to
// print one field set for both.
func (m Model) renderWorkDetail(r rowItem, w int) []string {
	wd := r.work
	if wd == nil {
		return nil
	}
	if r.kind == rowWorker {
		return workerDetail(wd.worker, w)
	}
	return commandDetail(wd, w)
}

func workerDetail(e *livework.Entry, w int) []string {
	out := []string{
		lipgloss.NewStyle().Bold(true).Render(e.ID),
		renderLiveState(e.State) + faintStyle.Render("  ·  "+e.PaneID),
		"",
		faintStyle.Render("now"),
		"  " + truncR(e.Activity, maxi(w-2, 4)),
	}
	if e.Worker != nil {
		out = append(out, "",
			faintStyle.Render("branch"), "  "+truncR(e.Worker.Branch, maxi(w-2, 4)),
			faintStyle.Render("worktree"), "  "+faintStyle.Render(truncR(e.Worker.Worktree, maxi(w-2, 4))),
		)
	}
	out = append(out, "",
		faintStyle.Render("elapsed"), "  "+elapsedStr(time.Since(e.StartedAt)),
		"",
	)
	actions := []string{"a attach"}
	if e.State == livework.StateFailed {
		actions = append(actions, "r retry")
	}
	actions = append(actions, "k kill")
	out = append(out, faintStyle.Render(strings.Join(actions, "   ")))
	return out
}

func commandDetail(wd *workRowData, w int) []string {
	out := []string{
		lipgloss.NewStyle().Bold(true).Render(wd.cmdName),
		faintStyle.Render(wd.cmdKind + "  ·  " + wd.cmdArchetype),
		"",
		faintStyle.Render("last run"),
	}
	if wd.lastRun.IsZero() {
		out = append(out, "  never")
	} else {
		out = append(out, "  "+humanizeAge(time.Since(wd.lastRun)))
	}
	out = append(out, "", faintStyle.Render("log"))
	if wd.logPath != "" {
		out = append(out, "  "+faintStyle.Render(truncR(wd.logPath, maxi(w-2, 4))))
	} else {
		out = append(out, "  "+faintStyle.Render("none yet"))
	}
	out = append(out, "")

	if wd.workflow != nil {
		out = append(out, faintStyle.Render("targets  query"))
		out = append(out, "  "+faintStyle.Render(truncR(wd.workflow.Query, maxi(w-2, 4))))
		return out
	}

	out = append(out, faintStyle.Render("steps"))
	for _, s := range wd.steps {
		out = append(out, "  "+faintStyle.Render(strconv.Itoa(s.idx)+" ")+s.kind)
		out = append(out, "    "+truncR(s.name, maxi(w-4, 4)))
		out = append(out, "    "+faintStyle.Render("from ")+lipgloss.NewStyle().Foreground(cMag).Render(s.archetype))
	}
	return out
}

// renderLiveState renders a livework.State as a fixed-width, color-coded
// word — running green, waiting yellow, failed red, anything else dim.
func renderLiveState(s livework.State) string {
	var c lipgloss.TerminalColor = cDim
	switch s {
	case livework.StateRunning:
		c = cGreen
	case livework.StateWaiting:
		c = cYellow
	case livework.StateFailed:
		c = cRed
	}
	return lipgloss.NewStyle().Foreground(c).Render(padEnd(string(s), 9))
}

// elapsedStr renders a duration as a compact "<1m / 12m / 1h5m" — the
// worker detail pane's "elapsed" field. Distinct from humanizeAge (which
// always appends "ago" for a point-in-time age) since "elapsed" reads as a
// span, not an age.
func elapsedStr(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	default:
		h := int(d.Hours())
		mm := int(d.Minutes()) % 60
		return strconv.Itoa(h) + "h" + strconv.Itoa(mm) + "m"
	}
}
