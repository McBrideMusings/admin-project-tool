// workview.go renders the Work tab (admin-3rgp.20): the combined
// command+worker list under one cursor, filling the body width, and a live
// band beneath it (full width, never a right-hand column) whose shape
// follows the cursor row's kind. work.go is the data half this reads.
package tui

import (
	"os"
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

// fieldRowsH is the fixed height of the live band's field block, for every
// row kind: a live command's log+url, a live worker's log+branch+worktree+
// session+now, or an idle entry's kind+desc+last-run+last-log. Five is the
// tallest of those. This is the epic's own single most important invariant
// (docs/spikes/admin-tui-desktop/variants.go:1258-1261): the field block is
// a fixed slot, padded or clipped to exactly this height by slotRows, so
// moving the cursor between rows in different states never moves a row of
// the band.
const fieldRowsH = 5

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

	detail := m.renderWorkBand(rows[cursor], lw, maxi(bh, 1))

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

// liveGlyph is the live band title's own state marker: the same waiting/
// failed overrides as commandGlyph, but a worker's default glyph is the
// diamond ◈, not the dot — ported from the design prototype's liveRow
// version of this rule (docs/spikes/admin-tui-desktop/variants.go:1319-
// 1331), which commandGlyph above deliberately dropped since the Commands
// section never draws a worker row.
func liveGlyph(e *livework.Entry) string {
	switch e.State {
	case livework.StateWaiting:
		return "◐"
	case livework.StateFailed:
		return "✗"
	default:
		if e.Worker != nil {
			return "◈"
		}
		return "●"
	}
}

// stateColor is the color a livework.State renders in everywhere it
// appears on the Work tab — running green, waiting yellow, failed red,
// anything else dim — shared by renderCommandState here and renderLiveState
// (below, Workers section + the live band's title) so the two state-word
// renderers can differ in padding and glyph without duplicating the color
// mapping itself.
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
// fixed-width, color-coded "<glyph> <word>" padded to wState.
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

// renderWorkBand is the Work tab's full-width live band (admin-3rgp.72.6): a
// title rule naming whatever the combined cursor sits on plus a count of
// everything else running (bandTitle), a fixed fieldRowsH-row field block
// chosen by the row's own state (bandIdle / bandFieldsCommand /
// bandFieldsWorker), then the row's own live output tail filling the rest.
// Ported from the design prototype's renderWorkBody band half (docs/spikes/
// admin-tui-desktop/variants.go:1533-1588) — the row-list half of that
// function stays in Model.renderWorkBody above, unchanged; this replaces
// the old right-hand detail pane (renderWorkDetail/commandDetail/
// workerDetail/detailHeader/appendLiveTail/outputRule) entirely.
//
// Contract: given the body width w and the band's own fixed height h,
// returns exactly h lines, whatever row r is — the field block's fixed
// height is what keeps the band from reflowing as the cursor moves between
// rows in different states (fieldRowsH's own doc comment).
func (m Model) renderWorkBand(r rowItem, w, h int) []string {
	wd := r.work
	if wd == nil {
		return blankRows(w, h)
	}

	var live *livework.Entry
	name := wd.cmdName
	if wd.targetKey != "" {
		name += " " + wd.targetKey
	}
	if r.kind == rowWorker {
		live = wd.worker
		if live != nil {
			name = live.Name
		}
	} else {
		live = wd.live
	}

	out := make([]string, 0, h)
	out = append(out, bandTitle(w, live, name))

	switch {
	case live != nil:
		var fields []string
		if live.Worker != nil {
			fields = bandFieldsWorker(live, w)
		} else {
			fields = bandFieldsCommand(wd, w)
		}
		out = append(out, fields...)
		avail := maxi(h-1-fieldRowsH, 1)
		var tail []string
		if live.Output != nil {
			tail = ringTailLines(live.Output, maxi(w-1, 1))
		}
		out = append(out, bandTail(tail, avail, w)...)
	default:
		out = append(out, bandIdle(wd, w)...)
		out = append(out, blankRows(w, maxi(h-2-fieldRowsH, 0))...)
		out = append(out, padEnd(" "+faintStyle.Render("output appears here while it runs"), w))
	}

	for i, l := range out {
		out[i] = padEnd(l, w)
	}
	for len(out) < h {
		out = append(out, padEnd("", w))
	}
	return out[:h]
}

// othersRunning is the live band title's right side: how much is live that
// is NOT the row being previewed, read from the exact same
// livework.Default.List() the row list itself reads (work.go's byName/
// liveWorkers), so the row list and this count can never disagree about
// what is running. Ported from the design prototype (docs/spikes/
// admin-tui-desktop/variants.go:1400-1437).
func othersRunning(selID string) string {
	entries := livework.Default.List()
	if len(entries) == 0 {
		return "nothing running"
	}
	cmds, workers := 0, 0
	for _, e := range entries {
		if selID != "" && e.ID == selID {
			continue
		}
		if e.Worker != nil {
			workers++
		} else {
			cmds++
		}
	}
	if cmds == 0 && workers == 0 {
		return "nothing else running"
	}
	parts := ""
	if cmds > 0 {
		parts = strconv.Itoa(cmds) + " command" + plural(cmds)
	}
	if workers > 0 {
		if parts != "" {
			parts += " · "
		}
		parts += strconv.Itoa(workers) + " worker" + plural(workers)
	}
	return "also " + parts
}

// bandTitle is the rule between the row list and the live band: the
// previewed row's own identity on the left, once, and the count of
// everything else running on the right — ported from the design prototype
// (docs/spikes/admin-tui-desktop/variants.go:1439-1473). live nil means the
// previewed row is idle; name is the row's own identity (a command's name,
// a target's "<cmd> <target>", or a worker's own name).
func bandTitle(w int, live *livework.Entry, name string) string {
	var titlePlain, titleStyled, selID string
	if live != nil {
		selID = live.ID
		glyph := liveGlyph(live)
		state := string(live.State)
		elapsed := elapsedStr(time.Since(live.StartedAt))
		titlePlain = glyph + " " + name + "  " + state + "  " + elapsed
		titleStyled = lipgloss.NewStyle().Foreground(stateColor(live.State)).Render(glyph) + " " +
			lipgloss.NewStyle().Bold(true).Render(name) +
			faintStyle.Render("  "+state+"  "+elapsed)
	} else {
		titlePlain = name + "  not running"
		titleStyled = lipgloss.NewStyle().Bold(true).Render(name) + faintStyle.Render("  not running")
	}

	right := othersRunning(selID)
	// Budget: "── " + title + " " + fill + suffix + " ──" = w, so the dashes
	// are whatever is left after everything with a fixed width. The
	// identity wins the space: on a narrow terminal the count drops rather
	// than squeezing the name it is a footnote to.
	const fixed = 7 // "── ", the space after the title, and " ──"
	if w-fixed-vlen(right)-1 < vlen(titlePlain) && w-fixed-vlen(right)-1 < 28 {
		right = ""
	}
	suffix := ""
	if right != "" {
		suffix = " " + right
	}
	// truncR counts display cells, not raw bytes, and title carries color
	// escapes — cutting it unconditionally would spend the budget on
	// invisible characters and clip a name that fits. Only cut when the
	// VISIBLE width is actually over, and cut the PLAIN text (never the
	// styled composite, which risks slicing mid-escape-sequence) from the
	// tail — which preserves the leading glyph+name and drops the trailing
	// state/elapsed first, the same "identity wins" rule the count-drop
	// above already follows.
	budget := maxi(w-fixed-vlen(suffix), 8)
	if vlen(titlePlain) > budget {
		titlePlain = truncR(titlePlain, budget)
		titleStyled = titlePlain
	}
	fill := maxi(w-fixed-vlen(titlePlain)-vlen(suffix), 0)
	return faintStyle.Render("── ") + titleStyled + faintStyle.Render(" "+strings.Repeat("─", fill)+suffix+" ──")
}

// slotRows pads (or clips) a field block to exactly fieldRowsH rows, each
// already padded to w — the fixed slot every one of the band's three field
// blocks renders through, so a block that would otherwise grow or shrink
// with what it has to say never moves a row of the band beneath it.
func slotRows(out []string, w int) []string {
	for i, l := range out {
		out[i] = padEnd(l, w)
	}
	for len(out) < fieldRowsH {
		out = append(out, padEnd("", w))
	}
	return out[:fieldRowsH]
}

// blankRows returns n rows, each padded to w — used to fill the band below
// a field block with nothing, rather than leaving short rows for the outer
// caller to pad.
func blankRows(w, n int) []string {
	out := make([]string, 0, maxi(n, 0))
	for len(out) < n {
		out = append(out, padEnd("", w))
	}
	return out
}

// bandIdle is the band's field block while the cursor sits on something
// that is NOT running: the entry's own kind, description and last run — a
// never-run command reads "never" and "—" for both. Ported from the design
// prototype's bandIdle (docs/spikes/admin-tui-desktop/variants.go:1499-
// 1531). A target row's kind is the literal "target", matching the
// reference frame (commands.txt:18) — its wd.cmdKind carries the PARENT's
// kind ("multi-target"), which is not what a target itself is.
//
// "last run" is derived from the resolved log file's own mtime (there is no
// separate exit-status/run-history record in this codebase — commandLogPath
// already does the best-effort glob commandRowItem falls back to for an
// idle row) rather than the design prototype's fixture-only "· exit 0",
// which nothing here tracks.
func bandIdle(wd *workRowData, w int) []string {
	lw := maxi(w-11, 8)
	kind := kindLabel(wd.cmdKind)
	if wd.targetKey != "" {
		kind = "target"
	}
	lastRun := "never"
	logCell := faintStyle.Render("—")
	if wd.logPath != "" {
		logCell = faintStyle.Render(fitPath(wd.logPath, lw))
		if fi, err := os.Stat(wd.logPath); err == nil {
			lastRun = humanizeAge(time.Since(fi.ModTime()))
		}
	}
	return slotRows([]string{
		" " + faintStyle.Render(padEnd("kind", 9)) + truncR(kind, lw),
		" " + faintStyle.Render(padEnd("desc", 9)) + truncR(wd.cmdDesc, lw),
		" " + faintStyle.Render(padEnd("last run", 9)) + faintStyle.Render(lastRun),
		" " + faintStyle.Render(padEnd("last log", 9)) + logCell,
	}, w)
}

// bandFieldsCommand is the band's field block for a live (running) command:
// its log path, then one row per configured url — a command with no url
// gets no url row, since the fixed slot already holds the space (printing
// "url —" would spend a row saying a field is absent, which the blank row
// already says). Ported from the design prototype's bandFields (docs/
// spikes/admin-tui-desktop/variants.go:1475-1487); the url row reuses
// urlRow's own reachability indicator rather than the prototype's plain
// string, since the real app (unlike the fixture) actually probes.
func bandFieldsCommand(wd *workRowData, w int) []string {
	lw := maxi(w-11, 8)
	out := []string{" " + faintStyle.Render(padEnd("log", 9)) + faintStyle.Render(fitPath(wd.logPath, lw))}
	for _, u := range wd.urls {
		out = append(out, " "+faintStyle.Render(padEnd("url", 9))+urlRow(u, true, lw))
	}
	return slotRows(out, w)
}

// bandFieldsWorker is the band's field block for a live worker: log,
// branch, worktree, session, now — always all five, since a worker (unlike
// a command) has no idle state to preview instead. Ported from the design
// prototype's bandFields worker branch (docs/spikes/admin-tui-desktop/
// variants.go:1489-1496); "now" is the worker's own self-reported Activity.
func bandFieldsWorker(e *livework.Entry, w int) []string {
	lw := maxi(w-11, 8)
	var branch, worktree string
	if e.Worker != nil {
		branch, worktree = e.Worker.Branch, e.Worker.Worktree
	}
	activity, _ := livework.Default.Activity(e.ID)
	return slotRows([]string{
		" " + faintStyle.Render(padEnd("log", 9)) + faintStyle.Render(fitPath(e.LogPath, lw)),
		" " + faintStyle.Render(padEnd("branch", 9)) + lipgloss.NewStyle().Foreground(cCyan).Render(truncR(branch, lw)),
		" " + faintStyle.Render(padEnd("worktree", 9)) + faintStyle.Render(fitPath(worktree, lw)),
		" " + faintStyle.Render(padEnd("session", 9)) + sessionField(e, lw),
		" " + faintStyle.Render(padEnd("now", 9)) + faintStyle.Render(truncR(activity, lw)),
	}, w)
}

// sessionField is the worker band's "session" value: hosted-here vs a
// detached launcher pane, ported from the design prototype's whereLabel
// (docs/spikes/admin-tui-desktop/variants.go:604-609) — decided from Via,
// never from a possibly-empty Handle, the same rule whereSeg (below) already
// follows for the Workers section's own where-column.
func sessionField(e *livework.Entry, w int) string {
	if e.Via == "" {
		return lipgloss.NewStyle().Foreground(cCyan).Render(truncR("hosted here", w)) +
			faintStyle.Render(" · dies when admin quits")
	}
	return lipgloss.NewStyle().Foreground(cMag).Render(truncR(e.Handle, w)) +
		faintStyle.Render(" · detached, outlives admin")
}

// bandTail is the live band's output tail: the row's ring, bottom-aligned
// against the floor of the band so a short tail leaves its gap above rather
// than below — the way terminal output accumulates. A nil/empty lines slice
// (a launcher-dispatched worker's bytes live in its own pane, not this
// process's — admin-spjp.6, a known capability gap out of scope here)
// renders as an empty tail: every row blank, nothing more.
func bandTail(lines []string, avail, w int) []string {
	if avail < 1 {
		avail = 1
	}
	if len(lines) > avail {
		lines = lines[len(lines)-avail:]
	}
	out := make([]string, 0, avail)
	for i := 0; i < avail-len(lines); i++ {
		out = append(out, padEnd("", w))
	}
	for _, l := range lines {
		out = append(out, padEnd(" "+l, w))
	}
	return out
}

// fitPath keeps a log or worktree path absolute. When it will not fit,
// whole segments go from the middle — "/Users/…/logs/docs.log" — so it
// still reads as a path. A mid-rune cut produces "/Users/pierce/Proj…tool/"
// and reads as corruption. Ported from the design prototype (docs/spikes/
// admin-tui-desktop/variants.go:1278-1299). Replaces shortenLogPath, which
// made a path relative to the process's cwd instead — the wrong rule per
// the design README's own "Log paths are absolute" section.
func fitPath(p string, w int) string {
	if vlen(p) <= w {
		return p
	}
	if w < 12 {
		return truncR(p, maxi(w, 1))
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for k := len(segs) - 1; k >= 1; k-- {
		if cand := "/" + segs[0] + "/…/" + strings.Join(segs[len(segs)-k:], "/"); vlen(cand) <= w {
			return cand
		}
	}
	if cand := "…/" + segs[len(segs)-1]; vlen(cand) <= w {
		return cand
	}
	return truncR(p, w)
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
// by the Workers section row (see renderCommandState above for the
// Commands section's own, glyph-carrying form).
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

// elapsedStr renders a duration as a compact "<1m / 12m / 1h5m" — the live
// band's "elapsed" value. Distinct from humanizeAge (which always appends
// "ago" for a point-in-time age) since "elapsed" reads as a span, not an
// age.
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
