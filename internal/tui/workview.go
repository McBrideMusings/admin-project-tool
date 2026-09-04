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

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// renderWorkBody lays out the Work tab: the row list at roughly two thirds
// of the width, a "│" divider, and the cursor row's detail pane filling the
// rest — the ticket's "a detail pane on the right third whose shape follows
// the row's kind."
func (m Model) renderWorkBody(width, height int) ([]string, []hitTarget) {
	rows := m.visibleRows()
	cursor := m.cursor[m.active]

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
			if r.label != "" {
				left[i] = "   " + faintStyle.Render(r.label)
			} else {
				left[i] = ""
			}
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
	// current mirrors renderRow's own distinction: the cyan cursor diamond
	// only means "current" while focus is actually in the body (admin-70hb,
	// AC1/AC6) — from the tab bar every row falls back to the faint,
	// unfocused marker so nothing on the Work tab reads as current either.
	current := selected && m.focus == focusBody
	marker := faintStyle.Render("◇ ")
	if current {
		marker = lipgloss.NewStyle().Foreground(cCyan).Render("◆ ")
	}
	const nameW, stateW = 16, 9
	rest := width - 2 - nameW
	if rest < 6 {
		rest = 6
	}

	if r.kind == rowCommandTarget {
		return marker + renderTargetRow(wd, current, nameW, rest)
	}

	if r.kind == rowWorker {
		e := wd.worker
		name := padEnd(truncR(e.ID, nameW), nameW)
		st, _ := livework.Default.State(e.ID)
		state := renderLiveState(st)
		whereText, whereStyled := whereSeg(e)
		actW := maxi(rest-stateW-whereWidth(whereText), 4)
		activity, _ := livework.Default.Activity(e.ID)
		act := padEnd(truncR(activity, actW), actW)
		return marker + name + state + faintStyle.Render(act) + whereStyled
	}

	name := padEnd(truncR(wd.cmdName, nameW), nameW)
	// A holder row (it has targets) never runs itself — it is a menu, not a
	// dispatch — so it must never take the live/pending rendering path even
	// when wd.live/wd.pending happen to be set (they no longer are, since a
	// genuine multi-target menu's entry is now keyed per target rather than
	// on the shared parent name, but this stays the belt-and-suspenders
	// guard the acceptance criteria name explicitly: the parent holder row
	// never shows a running indicator). It always falls straight to the
	// idle kind+desc form below.
	if len(wd.targets) == 0 {
		if wd.live != nil {
			st, _ := livework.Default.State(wd.live.ID)
			state := renderLiveState(st)
			whereText, whereStyled := whereSeg(wd.live)
			actW := maxi(rest-stateW-whereWidth(whereText), 4)
			activity, _ := livework.Default.Activity(wd.live.ID)
			act := padEnd(truncR(activity, actW), actW)
			return marker + name + state + faintStyle.Render(act) + whereStyled
		}
		if wd.pending {
			state := renderLiveState(livework.StateStarting)
			desc := truncR(wd.cmdDesc, maxi(rest-stateW, 4))
			return marker + name + state + faintStyle.Render(desc)
		}
	}
	kind := padEnd(truncR(wd.cmdKind, stateW), stateW)
	desc := truncR(wd.cmdDesc, maxi(rest-stateW, 4))
	return marker + name + faintStyle.Render(kind+desc)
}

// renderTargetRow renders one target of an expanded command: a tree
// connector under the parent, then the target key in the same name column
// every other row uses, indented by the connector's two cells so the
// submenu reads as belonging to the row above it. The cursor marker is
// already rendered by the caller, so this starts at the connector.
//
// wd.live is this target's OWN entry (targetRows looks it up by
// "<cmdName>:<targetKey>", not copied from the parent — admin-0cyu.1), so
// when it is running this mirrors renderWorkRow's rowWorker/live branch:
// state, activity and where-segment take the place of the idle
// connector+key+hint form.
func renderTargetRow(wd *workRowData, focused bool, nameW, rest int) string {
	connector := "├ "
	if wd.targetLast {
		connector = "└ "
	}
	keyW := maxi(nameW-2, 4)
	key := padEnd(truncR(wd.targetKey, keyW), keyW)
	name := key
	if focused {
		name = lipgloss.NewStyle().Foreground(cCyan).Render(key)
	}
	if wd.live != nil {
		const stateW = 9
		st, _ := livework.Default.State(wd.live.ID)
		state := renderLiveState(st)
		whereText, whereStyled := whereSeg(wd.live)
		actW := maxi(rest-stateW-whereWidth(whereText), 4)
		activity, _ := livework.Default.Activity(wd.live.ID)
		act := padEnd(truncR(activity, actW), actW)
		return faintStyle.Render(connector) + name + state + faintStyle.Render(act) + whereStyled
	}
	if wd.pendingHere {
		// Mirrors renderWorkRow's own wd.pending branch: the window between
		// enter and a livework.Entry registering must show SOMETHING or a
		// target row looks unresponsive for however long StartHostedPTY
		// takes to spawn and register (admin-2y6p — a user report of "no
		// visible change" after enter on a target row with nothing else
		// running to compare against). Without this, wd.live == nil and
		// the row fell straight through to the idle hint below, so a
		// slow-to-register dispatch was indistinguishable from no dispatch
		// at all.
		//
		// pendingHere, not pending: pending is command-wide (every sibling
		// target under the same multi-target command shares
		// Model.pendingDispatch[cmdName]), so using it here made EVERY
		// target under "command-test" render "starting" the instant any ONE
		// of them (e.g. "counter") was dispatched — a second, distinct bug
		// this same report caught. pendingHere is targetRows' per-row answer
		// to "is THIS target the one Model.pendingTarget[cmdName] names".
		state := renderLiveState(livework.StateStarting)
		return faintStyle.Render(connector) + name + state
	}
	hint := truncR(wd.targetRun, maxi(rest-2, 4))
	return faintStyle.Render(connector) + name + faintStyle.Render(hint)
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
	locale := "outlives the dashboard"
	if e.Via == "" {
		locale = "dies when the dashboard quits"
	} else if e.Handle != "" {
		locale = e.Handle + "  ·  outlives the dashboard"
	}
	st, _ := livework.Default.State(e.ID)
	out := []string{
		lipgloss.NewStyle().Bold(true).Render(e.ID),
		renderLiveState(st) + faintStyle.Render("  ·  "+truncR(locale, maxi(w-2, 4))),
	}
	if activity, _ := livework.Default.Activity(e.ID); activity != "" {
		out = append(out, "",
			faintStyle.Render("now"),
			"  "+truncR(activity, maxi(w-2, 4)),
		)
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
	// No retry: a worker is an agent session in a worktree, and restarting one
	// under the same identity is not something every producer can honour or
	// every kind of work can survive. Kill it and start the work again.
	actions := []string{"a attach", "k kill"}
	out = append(out, faintStyle.Render(strings.Join(actions, "   ")))
	return out
}

func commandDetail(wd *workRowData, w int) []string {
	title := wd.cmdName
	if wd.targetKey != "" {
		title += " " + wd.targetKey
	}
	out := []string{
		lipgloss.NewStyle().Bold(true).Render(title),
		faintStyle.Render(wd.cmdKind + "  ·  " + wd.cmdArchetype),
		"",
	}
	if len(wd.targets) > 0 {
		out = append(out, faintStyle.Render("targets"))
		for _, t := range wd.targets {
			out = append(out, "  "+truncR(t, maxi(w-2, 4)))
		}
		out = append(out, "")
	}
	out = append(out, faintStyle.Render("log"))
	if wd.logPath != "" {
		out = append(out, "  "+faintStyle.Render(truncR(wd.logPath, maxi(w-2, 4))))
	} else {
		out = append(out, "  "+faintStyle.Render("none yet"))
	}
	out = append(out, "")

	// elapsed and reachability are both properties of a command actually
	// running right now — an idle command shows neither, per the ticket's
	// own "does not show a stale elapsed or a reachability state for a url
	// nothing is serving."
	if wd.live != nil {
		out = append(out, faintStyle.Render("elapsed"), "  "+elapsedStr(time.Since(wd.live.StartedAt)), "")
	}
	if len(wd.urls) > 0 {
		out = append(out, faintStyle.Render("urls"))
		for _, u := range wd.urls {
			out = append(out, "  "+urlRow(u, wd.live != nil, maxi(w-2, 4)))
		}
		out = append(out, "")
	}

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

// urlRow renders one configured url as "<dot> label  url", truncated to fit
// w. A bare-string entry resolves to Label == URL (frame.ResolveURLs's own
// rule), which this matches by dropping the label column entirely rather
// than printing the same string twice — the same one-column-vs-two rule the
// pinned footer's own ContentRows applies. The reachability dot is shown
// only while live is true (a running command): probeUp green, probeDown
// red/dim, probeUnknown faint. An idle command carries no dot at all —
// nothing is being checked, so there is nothing to report.
func urlRow(u frame.URLEntry, live bool, w int) string {
	var dot string
	if live {
		switch urlProbes.Status(u.URL) {
		case probeUp:
			dot = lipgloss.NewStyle().Foreground(cGreen).Render("●") + " "
		case probeDown:
			dot = lipgloss.NewStyle().Foreground(cRed).Render("○") + " "
		default:
			dot = faintStyle.Render("·") + " "
		}
	}
	rest := maxi(w-vlen(dot), 4)
	if u.Label == u.URL {
		return dot + faintStyle.Render(truncR(u.URL, rest))
	}
	label := truncR(u.Label, maxi(rest/3, 4))
	url := truncR(u.URL, maxi(rest-vlen(label)-2, 4))
	return dot + lipgloss.NewStyle().Bold(true).Render(label) + "  " + faintStyle.Render(url)
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
	case livework.StateStarting:
		c = cCyan
	}
	return lipgloss.NewStyle().Foreground(c).Render(padEnd(string(s), 9))
}

// whereSeg returns the right-hand where-value for a live entry: the literal
// "hosted" (cyan) for a hosted-pty entry, the launcher handle (magenta) for
// a launcher-dispatched one. Decided from Via, never from a possibly-empty
// Handle — that inference is the bug this replaces. text is the plain,
// unstyled form (for width math and tests); styled carries the color and,
// when non-empty, a leading space to separate it from the activity.
func whereSeg(e *livework.Entry) (text string, styled string) {
	switch {
	case e.Via == "":
		text = "hosted"
		styled = " " + lipgloss.NewStyle().Foreground(cCyan).Render(text)
	case e.Handle != "":
		text = e.Handle
		styled = " " + lipgloss.NewStyle().Foreground(cMag).Render(text)
	}
	return text, styled
}

// whereWidth is the plain-text display width whereSeg's styled form takes
// up once rendered: the value itself plus a leading separator space, or 0
// when there is no where-value to show.
func whereWidth(text string) int {
	if text == "" {
		return 0
	}
	return vlen(text) + 1
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
