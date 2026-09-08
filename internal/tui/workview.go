// workview.go renders the Work tab (admin-3rgp.20): the combined
// command+worker list in the left two thirds under one cursor, and a detail
// pane in the right third whose shape follows the row's kind. work.go is the
// data half this reads.
package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/logformat"
)

// renderWorkBody lays out the Work tab. On a wide-enough terminal it's the
// row list at roughly two thirds of the width, a "│" divider, and the cursor
// row's detail pane filling the rest — the ticket's "a detail pane on the
// right third whose shape follows the row's kind." On a tall, narrow one
// (bottomOrient) it stacks instead: the row list on top (capped to at most
// half the body height, scrolling within that half), a full-width rule, and
// the detail pane filling every remaining row — ported from the design
// prototype's workBody/renderPaletteScreen (docs/spikes/admin-tui-desktop/
// variants.go), which solved the "detail pane truncated to ~30 columns while
// most of a tall screen sits empty" problem this mirrors exactly.
func (m Model) renderWorkBody(width, height int) ([]string, []hitTarget) {
	rows := m.visibleRows()
	cursor := m.cursor[m.active]

	if len(rows) == 0 {
		return pad([]string{"  " + mutedStyle.Render("(nothing here)")}, height),
			padHits([]hitTarget{{kind: hitNone}}, height)
	}

	// bottomOrient: the exact geometry test the prototype settled on (that
	// file's own comment has the reasoning for the 2.3 constant) — kept
	// identical here so the two never disagree.
	bottomOrient := float64(width)/2.3 < float64(height)

	var lw, rw int
	if bottomOrient {
		lw = maxi(width-2, 20)
		rw = lw
	} else {
		lw = width * 2 / 3
		if lw > width-14 {
			lw = width - 14
		}
		if lw < 20 {
			lw = width
		}
		rw = width - lw - 2
		if rw < 0 {
			rw = 0
		}
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

	if bottomOrient {
		return m.renderWorkBodyStacked(rows, left, hits, cursor, width, height, lw)
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

	right := m.renderWorkDetail(rows[cursor], rw, height)

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

// renderWorkBodyStacked is bottomOrient's layout: the row list on top
// (never more than half the body height — it scrolls inside its half once
// its content would need more), a full-width rule directly beneath the
// list's own content with no padding above it, then the detail pane filling
// every remaining row, uncapped. Mirrors workBody's own bottomOrient branch
// in the prototype byte for byte in geometry: detailH is computed first as
// "whatever's left after the full, unclipped list plus one rule row," then
// bumped up to at least half the body height — which is what caps the list
// to at most the other half, rather than a separate clamp on listH itself.
func (m Model) renderWorkBodyStacked(rows []rowItem, left []string, hits []hitTarget, cursor, width, height, lw int) ([]string, []hitTarget) {
	detailH := height - len(left) - 1
	if detailH < height/2 {
		detailH = height / 2
	}
	if detailH < 0 {
		detailH = 0
	}
	listH := height - detailH - 1
	if listH < 0 {
		listH = 0
	}

	// The preview band spans the same width as the list above it (lw is
	// already width-2: one leading space and one trailing column left
	// blank on both halves of the stacked layout). A 100-column cap here
	// was ported from the design prototype's own frame width and left the
	// detail band's rules and output short of the real body width on any
	// terminal wider than ~102 columns (admin fix-detail-band-full-width).
	previewW := lw

	listLines := left
	hitLines := hits
	if len(left) > listH {
		start, end := window(len(left), cursor, listH)
		listLines = append([]string{}, left[start:end]...)
		hitLines = append([]hitTarget{}, hits[start:end]...)
		if start > 0 && len(listLines) > 0 {
			listLines[0] = faintStyle.Render("  ↑ " + strconv.Itoa(start) + " more")
			hitLines[0] = hitTarget{kind: hitNone}
		}
		if end < len(left) && len(listLines) > 0 {
			last := len(listLines) - 1
			listLines[last] = faintStyle.Render("  ↓ more")
			hitLines[last] = hitTarget{kind: hitNone}
		}
	}

	detail := m.renderWorkDetail(rows[cursor], previewW, maxi(detailH, 1))

	out := make([]string, 0, height)
	outHits := make([]hitTarget, 0, height)
	for i := 0; i < listH; i++ {
		l, hit := "", hitTarget{kind: hitNone}
		if i < len(listLines) {
			l, hit = listLines[i], hitLines[i]
		}
		out = append(out, padEnd(" "+padEnd(l, lw), width))
		outHits = append(outHits, hit)
	}
	if detailH > 0 {
		out = append(out, faintStyle.Render(strings.Repeat("─", width)))
		outHits = append(outHits, hitTarget{kind: hitNone})
		for i := 0; i < detailH; i++ {
			d := ""
			if i < len(detail) {
				d = detail[i]
			}
			// padEnd's outer call is the trailing one-column margin: lw
			// (=width-2) already leaves that column blank after the
			// leading space, this just makes the blank explicit rather
			// than relying on whatever painted the row before.
			out = append(out, padEnd(" "+padEnd(d, previewW), width))
			outHits = append(outHits, hitTarget{kind: hitNone})
		}
	}
	return pad(out, height), padHits(outHits, height)
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
func (m Model) renderWorkDetail(r rowItem, w, h int) []string {
	wd := r.work
	if wd == nil {
		return nil
	}
	if r.kind == rowWorker {
		return workerDetail(wd.worker, w, h)
	}
	return commandDetail(wd, w, h)
}

func workerDetail(e *livework.Entry, w, h int) []string {
	st, _ := livework.Default.State(e.ID)
	out := detailHeader(e.ID, true, st, e.StartedAt, e.LogPath, w)
	out = append(out, "")

	locale := "outlives the dashboard"
	if e.Via == "" {
		locale = "dies when the dashboard quits"
	} else if e.Handle != "" {
		locale = e.Handle + "  ·  outlives the dashboard"
	}
	out = append(out, faintStyle.Render("where"), "  "+truncR(locale, maxi(w-2, 4)))

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
	out = append(out, "")
	// No retry: a worker is an agent session in a worktree, and restarting one
	// under the same identity is not something every producer can honour or
	// every kind of work can survive. Kill it and start the work again.
	actions := []string{"a attach", "k kill"}
	out = append(out, faintStyle.Render(strings.Join(actions, "   ")))
	return appendLiveTail(out, e, w, h)
}

func commandDetail(wd *workRowData, w, h int) []string {
	title := wd.cmdName
	if wd.targetKey != "" {
		title += " " + wd.targetKey
	}
	var state livework.State
	var started time.Time
	if wd.live != nil {
		state, _ = livework.Default.State(wd.live.ID)
		started = wd.live.StartedAt
	}
	out := detailHeader(title, wd.live != nil, state, started, wd.logPath, w)
	out = append(out, "")

	if len(wd.targets) > 0 {
		out = append(out, faintStyle.Render("targets"))
		for _, t := range wd.targets {
			out = append(out, "  "+truncR(t, maxi(w-2, 4)))
		}
		out = append(out, "")
	}

	// Reachability is a property of a command actually running right now —
	// an idle command shows its configured urls with no dot, per the
	// ticket's own "does not show a reachability state for a url nothing is
	// serving."
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
	}

	return appendLiveTail(out, wd.live, w, h)
}

// detailHeader renders the Work tab detail pane's two-row header, shared by
// commandDetail and workerDetail: row 1 is the row's name flush left and,
// while running, the pane's own colored state plus elapsed flush right (the
// same renderLiveState/elapsedStr the row list and the old field-list pane
// already used); row 2 is a dim "log" label plus the log path, shown only
// when one is known. A row that is neither running nor carries a log path
// renders just the name.
func detailHeader(name string, running bool, state livework.State, started time.Time, logPath string, w int) []string {
	var row1 string
	if running {
		right := renderLiveState(state) + faintStyle.Render(" · "+elapsedStr(time.Since(started)))
		left := lipgloss.NewStyle().Bold(true).Render(truncR(name, maxi(w-vlen(right)-2, 1)))
		gap := maxi(w-vlen(left)-vlen(right), 1)
		row1 = left + strings.Repeat(" ", gap) + right
	} else {
		row1 = lipgloss.NewStyle().Bold(true).Render(truncR(name, maxi(w-1, 1)))
	}
	out := []string{" " + row1}
	if logPath != "" {
		out = append(out, " "+faintStyle.Render("log")+"  "+faintStyle.Render(shortenLogPath(logPath, maxi(w-6, 4))))
	}
	return out
}

// shortenLogPath fits path into w display cells, preferring the path made
// relative to the process's own working directory — the project root, since
// the interpreter cd's there at boot (see the Guards section of this
// project's own CLAUDE.md) — over truncating the absolute path from the
// front: the root prefix is most of an absolute log path's length and
// carries the least information of any part of it. Falls back to a plain
// truncation when the relative form still doesn't fit, or when the path
// can't be made relative at all (outside the cwd, or it's unavailable).
func shortenLogPath(path string, w int) string {
	if vlen(path) <= w {
		return path
	}
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, path); err == nil && !strings.HasPrefix(rel, "..") {
			return truncR(rel, w)
		}
	}
	return truncR(path, w)
}

// appendLiveTail is the shared second half of both detail panes
// (admin-3rgp.65 follow-up): when e is a live hosted entry with a non-nil
// Output ring, it keeps the field block exactly as built above and fills
// every remaining row of the column with the ring's tail — framed by an
// opening rule carrying the word "output" and a plain closing rule, both
// spanning the full pane width with no side borders — bottom-aligned
// against the closing rule so a short tail leaves its gap above rather than
// below, the way terminal output accumulates. A nil entry (idle row) or a
// nil ring (dispatched through a [terminal] launcher rather than hosted
// in-process, so nothing is being drained into a Ring at all) leaves out
// untouched: no rules, no header, no placeholder.
func appendLiveTail(out []string, e *livework.Entry, w, h int) []string {
	if e == nil || e.Output == nil {
		return out
	}
	// Reserve one row for the opening rule and one for the closing rule at
	// the very bottom of the pane, plus one more for a blank separator
	// before the opening rule — unless the block built above already ends
	// in one (most of the pane's own sections close with a blank line
	// already; reusing it instead of stacking a second one is what keeps a
	// bare header with nothing else above it from reading as a double gap).
	// Only bother with a tail if at least one row of actual content would
	// also fit between the two rules.
	reserve := 2
	needsSeparator := len(out) == 0 || out[len(out)-1] != ""
	if needsSeparator {
		reserve++
	}
	avail := h - len(out) - reserve
	if avail < 1 {
		return out
	}
	lines := ringTailLines(e.Output, w)
	if len(lines) > avail {
		lines = lines[len(lines)-avail:]
	}
	if needsSeparator {
		out = append(out, "")
	}
	out = append(out, outputRule("output", w))
	for i := 0; i < avail-len(lines); i++ {
		out = append(out, "")
	}
	out = append(out, lines...)
	return append(out, outputRule("", w))
}

// outputRule draws one of the output block's two bounding rules, spanning
// exactly w display cells with no side borders. label carries the block's
// name on the opening rule ("─── output ──…──"); the closing rule passes
// "" and renders as plain dashes.
func outputRule(label string, w int) string {
	if label == "" {
		return faintStyle.Render(strings.Repeat("─", maxi(w, 0)))
	}
	prefix := "─── " + label + " "
	dashes := maxi(w-vlen(prefix), 0)
	return faintStyle.Render(prefix + strings.Repeat("─", dashes))
}

// ringTailLines reads a live entry's whole Ring, strips ANSI escapes (this
// is a raw peek at the process's own output — internal/logformat already
// owns re-timestamping and re-colorizing for every other surface, and this
// pane does neither), splits on lines, and truncates each to w display
// cells. A trailing blank line left by the child's own final newline is
// dropped so it doesn't masquerade as a real blank row of output.
//
// The Ring holds raw bytes straight off the hosted pty's master fd
// (internal/kinds/hostedpty.go's io.Copy), and a pty in the ordinary cooked
// mode every plain CLI process runs in (nothing here sets the slave to raw
// mode) applies ONLCR: every outgoing "\n" the child writes arrives here as
// "\r\n". Splitting on "\n" alone left that "\r" stuck to the end of every
// line, and bubbletea's own line-diffing renderer turns that into a visibly
// blank row: it truncates/pads each rendered line, and pads short lines with
// an erase-to-end-of-line escape appended AFTER the text — but a trailing
// "\r" moves the real terminal's cursor back to column 0 first, so that
// erase wipes out the very content this line just painted, one full row
// after every real line of tail output. Normalizing CRLF to LF before
// splitting (rather than only trimming the buffer's own trailing newline)
// is what keeps that "\r" off the end of every line, not just the last one.
func ringTailLines(r *livework.Ring, w int) []string {
	raw := logformat.AnsiEscapeRe.ReplaceAllString(string(r.Bytes()), "")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.TrimRight(raw, "\n")
	if raw == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	for i, l := range lines {
		lines[i] = truncR(strings.TrimRight(l, "\r"), maxi(w, 1))
	}
	return lines
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
