// workview.go renders the Work tab (admin-3rgp.20): the combined
// command+worker list under one cursor, filling the body width, and a live
// band beneath it (full width, never a right-hand column) whose shape
// follows the cursor row's kind. work.go is the data half this reads.
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

// bandH is how many rows of the body height the live band takes, fixed per
// terminal height and never per cursor position or content — ported
// verbatim from the design prototype (docs/spikes/admin-tui-desktop/
// variants.go:1263-1269).
func bandH(h int) int {
	return maxi(mini(13, h/2), mini(9, h-1))
}

// renderWorkBody lays out the Work tab: one layout at every geometry — the
// row list fills the body width, and the live band spans that same full
// width beneath it. The list gives up HEIGHT to the band (bandH) and never
// width. Ported from the design prototype's workBody/renderPaletteScreen
// (docs/spikes/admin-tui-desktop/variants.go); the prototype's own two-
// orientation switch (a side-by-side split on a wide terminal, a stacked
// layout on a tall narrow one, picked by comparing width against height) is
// gone — the side-by-side split gave the detail pane roughly 30 columns on
// exactly the terminals where log lines are longest, and the two branches
// were two bodies of layout code that could disagree.
func (m Model) renderWorkBody(width, height int) ([]string, []hitTarget) {
	rows := m.visibleRows()
	cursor := m.cursor[m.active]

	if len(rows) == 0 {
		return pad([]string{"  " + mutedStyle.Render("(nothing here)")}, height),
			padHits([]hitTarget{{kind: hitNone}}, height)
	}

	lw := maxi(width-2, 20)

	left := make([]string, len(rows))
	hits := make([]hitTarget, len(rows))
	for i, r := range rows {
		switch r.kind {
		case rowHeader:
			// A header carrying a rightSegs entry (Commands' own resolved-
			// invocation count — work.go's workRows) renders bold label + dim
			// count, matching the reference frame's "Commands  47"
			// (docs/spikes/admin-tui-desktop/commands.txt:4); every other
			// header (Workers) keeps the plain muted form it always had.
			if len(r.rightSegs) > 0 {
				left[i] = " " + lipgloss.NewStyle().Bold(true).Render(r.label) + faintStyle.Render(r.rightSegs[0].t)
			} else {
				left[i] = " " + mutedStyle.Render(r.label)
			}
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
			if r.group {
				// A group parent carries no marker and the cursor can never
				// land on it (selectable() excludes it) — it must not be
				// clickable into either, matching the header/text rows above.
				hits[i] = hitTarget{kind: hitNone}
			} else {
				hits[i] = hitTarget{kind: hitRow, row: i}
			}
		}
	}

	bh := bandH(height)
	listH := maxi(height-bh, 1)

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

	detail := m.renderWorkDetail(rows[cursor], lw, maxi(bh, 1))

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
	for i := 0; i < bh; i++ {
		d := ""
		if i < len(detail) {
			d = detail[i]
		}
		// padEnd's outer call is the trailing one-column margin: lw
		// (=width-2) already leaves that column blank after the leading
		// space, this just makes the blank explicit rather than relying on
		// whatever painted the row before.
		out = append(out, padEnd(" "+padEnd(d, lw), width))
		outHits = append(outHits, hitTarget{kind: hitNone})
	}
	return pad(out, height), padHits(outHits, height)
}

// The Commands section's one column grid (docs/spikes/admin-tui-desktop/
// variants.go:1252-1255, ported verbatim): a two-cell cursor marker, a
// 16-cell name (or, on a target row, connector+key sharing that same 16
// cells), a 10-cell state column ("● running"), a 7-cell elapsed column
// ("4m12s"), then one free column that spends itself on activity, a run
// hint, or a kind+description pair depending on the row's shape. Verified
// against commands.txt at w=96: the name column ends at screen column 19 on
// every row kind, and the free column begins at column 36 (19 + wState +
// wElapsed) on every row kind.
const (
	wMarker  = 2  // "◆ " / "◇ "
	wName    = 16 // the name column (target rows: connector + key)
	wState   = 10 // "● running"
	wElapsed = 7  // "4m12s"
)

// kindLabel abbreviates the one kind whose name does not fit the state+
// elapsed columns verbatim (docs/spikes/admin-tui-desktop/variants.go:1168).
func kindLabel(k string) string {
	if k == "multi-target" {
		return "multi-tgt"
	}
	return k
}

// commandGlyph is the state marker shown to the left of a Commands-section
// row's state word: waiting and failed override everything else, anything
// else — running, starting — gets the plain dot (docs/spikes/
// admin-tui-desktop/variants.go:1319 minus the isWorkerRow branch, which
// only ever applied to the Workers section — out of scope here, admin-3rgp.
// 72.16).
func commandGlyph(s livework.State) string {
	switch s {
	case livework.StateWaiting:
		return "◐"
	case livework.StateFailed:
		return "✗"
	default:
		return "●"
	}
}

// stateColor is the color a livework.State renders in everywhere it
// appears on the Work tab — running green, waiting yellow, failed red,
// anything else dim — shared by renderCommandState here and renderLiveState
// (below, Workers section + detail pane) so the two state-word renderers
// can differ in padding and glyph without duplicating the color mapping
// itself.
func stateColor(s livework.State) lipgloss.TerminalColor {
	switch s {
	case livework.StateRunning:
		return cGreen
	case livework.StateWaiting:
		return cYellow
	case livework.StateFailed:
		return cRed
	case livework.StateStarting:
		return cCyan
	default:
		return cDim
	}
}

// renderCommandState renders a livework.State as the Commands-section grid's
// fixed-width, color-coded "<glyph> <word>" padded to wState. Deliberately
// separate from renderLiveState (below), which the Workers section and the
// detail pane still use unchanged: both are out of scope for this ticket
// (admin-3rgp.72.16 owns porting Workers onto this same grid).
func renderCommandState(s livework.State) string {
	word := commandGlyph(s) + " " + string(s)
	return lipgloss.NewStyle().Foreground(stateColor(s)).Render(padEnd(word, wState))
}

// renderWorkRow renders one command, target or worker row against the grid
// above. A group row (a multi-target parent with more than one target) is
// routed to renderGroupRow instead: it carries no marker and no live state
// of its own — it holds targets and runs nothing itself.
func (m Model) renderWorkRow(r rowItem, selected bool, width int) string {
	wd := r.work
	if wd == nil {
		return ""
	}
	if r.kind == rowCommand && r.group {
		return renderGroupRow(wd, width)
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
	rest := maxi(width-wMarker-wName-wState-wElapsed, 6)

	if r.kind == rowCommandTarget {
		return marker + renderTargetRow(wd, current, rest)
	}

	if r.kind == rowWorker {
		// The Workers section keeps its pre-existing grid untouched — porting
		// it onto the Commands grid above is admin-3rgp.72.16's job, not this
		// ticket's (see the ticket's own "Out of scope: the Workers section").
		const workerNameW, workerStateW = 16, 9
		workerRest := width - 2 - workerNameW
		if workerRest < 6 {
			workerRest = 6
		}
		e := wd.worker
		name := padEnd(truncR(e.ID, workerNameW), workerNameW)
		st, _ := livework.Default.State(e.ID)
		state := renderLiveState(st)
		whereText, whereStyled := whereSeg(e)
		actW := maxi(workerRest-workerStateW-whereWidth(whereText), 4)
		activity, _ := livework.Default.Activity(e.ID)
		act := padEnd(truncR(activity, actW), actW)
		return marker + name + state + faintStyle.Render(act) + whereStyled
	}

	name := padEnd(truncR(wd.cmdName, wName), wName)
	// A holder row (it has targets) never runs itself — it is a menu, not a
	// dispatch — so it must never take the live/pending rendering path even
	// when wd.live/wd.pending happen to be set (they no longer are, since a
	// genuine multi-target menu's entry is now keyed per target rather than
	// on the shared parent name, but this stays the belt-and-suspenders
	// guard the acceptance criteria name explicitly: the parent holder row
	// never shows a running indicator). It always falls straight to the
	// idle kind+desc form below — though in practice r.group already routed
	// any such row to renderGroupRow above, so len(wd.targets) is always 0
	// by the time this runs.
	if len(wd.targets) == 0 {
		if wd.live != nil {
			st, _ := livework.Default.State(wd.live.ID)
			state := renderCommandState(st)
			elapsed := faintStyle.Render(padEnd(elapsedStr(time.Since(wd.live.StartedAt)), wElapsed))
			activity, _ := livework.Default.Activity(wd.live.ID)
			act := padEnd(truncR(activity, rest), rest)
			return marker + name + state + elapsed + faintStyle.Render(act)
		}
		if wd.pending {
			state := renderCommandState(livework.StateStarting)
			elapsed := faintStyle.Render(padEnd("", wElapsed))
			desc := truncR(wd.cmdDesc, rest)
			return marker + name + state + elapsed + faintStyle.Render(desc)
		}
	}
	kind := padEnd(truncR(kindLabel(wd.cmdKind), wState+wElapsed), wState+wElapsed)
	desc := truncR(wd.cmdDesc, rest)
	return marker + name + faintStyle.Render(kind+desc)
}

// renderGroupRow renders a multi-target parent's own row: two blank cells
// where the marker would be — the marker column is what promises enter will
// run the row, and a group runs nothing itself — then its name, kind and
// description in the same columns every other row uses.
func renderGroupRow(wd *workRowData, width int) string {
	rest := maxi(width-wMarker-wName-wState-wElapsed, 6)
	name := lipgloss.NewStyle().Bold(true).Render(padEnd(truncR(wd.cmdName, wName), wName))
	kind := padEnd(truncR(kindLabel(wd.cmdKind), wState+wElapsed), wState+wElapsed)
	desc := truncR(wd.cmdDesc, rest)
	return "  " + name + faintStyle.Render(kind+desc)
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
// state, elapsed and activity take the place of the idle
// connector+key+hint form.
func renderTargetRow(wd *workRowData, focused bool, rest int) string {
	connector := "├ "
	if wd.targetLast {
		connector = "└ "
	}
	keyW := maxi(wName-2, 4)
	key := padEnd(truncR(wd.targetKey, keyW), keyW)
	name := key
	if focused {
		name = lipgloss.NewStyle().Foreground(cCyan).Render(key)
	}
	if wd.live != nil {
		st, _ := livework.Default.State(wd.live.ID)
		state := renderCommandState(st)
		elapsed := faintStyle.Render(padEnd(elapsedStr(time.Since(wd.live.StartedAt)), wElapsed))
		activity, _ := livework.Default.Activity(wd.live.ID)
		act := padEnd(truncR(activity, rest), rest)
		return faintStyle.Render(connector) + name + state + elapsed + faintStyle.Render(act)
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
		state := renderCommandState(livework.StateStarting)
		return faintStyle.Render(connector) + name + state
	}
	hint := truncR(wd.targetRun, rest)
	return faintStyle.Render(connector) + name + faintStyle.Render(padEnd("", wState+wElapsed)+hint)
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
// word — running green, waiting yellow, failed red, anything else dim. Used
// by the Workers section and the detail pane, both out of scope for this
// ticket (see renderCommandState above for the Commands section's own,
// glyph-carrying form).
func renderLiveState(s livework.State) string {
	return lipgloss.NewStyle().Foreground(stateColor(s)).Render(padEnd(string(s), 9))
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
