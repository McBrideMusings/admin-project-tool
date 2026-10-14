// workview.go renders the Work tab (admin-3rgp.20): the combined
// command+worker list under one cursor, filling the body width, and a live
// band beneath it (full width, never a right-hand column) whose shape
// follows the cursor row's kind. work.go is the data half this reads.
package tui

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/logformat"
)

// bandH is the band's row count: fixed for a given body height, whatever the
// row list holds or the cursor row's state, so the band always sits in the
// same rows at the bottom of the body.
func bandH(h int) int {
	return maxi(mini(13, h/2), mini(9, h-1))
}

// fieldRowsH is the fixed height of the band's field block for every row
// kind: a live command's log+url, a live worker's log+branch+worktree+
// session+now, or an idle entry's kind+desc+last run+last log (+query).
// Five is the tallest. The block is padded or clipped to exactly this by
// slotRows, so moving the cursor between rows in different states never moves
// a row of the band.
const fieldRowsH = 5

// renderWorkBody lays out the Work tab: one layout at every geometry — the
// row list fills the body width, and the live band spans that same full
// width beneath it, pinned to the bottom: always bandH rows, whatever state
// the cursor row is in. The list gives up HEIGHT to the band and never width;
// a short list leaves blank rows between itself and the band.
// There is no side-by-side split on a wide terminal: it would give the detail
// pane roughly 30 columns on exactly the terminals where log lines are
// longest, and two layout branches could disagree.
func (m Model) renderWorkBody(width, height int) ([]string, []hitTarget) {
	rows := m.visibleRows()
	cursor := *m.cur(m.active)

	// The Status strip is pinned above the scrolling list, whatever the filter
	// leaves in it, and takes its rows from the list alone while leaving the
	// list at least three — the band keeps bandH(fullHeight) either way.
	var strip []string
	var stripHits []hitTarget
	if m.manifest != nil {
		strip, stripHits = statusStrip(m.manifest.Status, width, height-bandH(height)-3)
	}
	fullHeight := height
	height -= len(strip)

	if len(rows) == 0 {
		out := append(strip, "  "+mutedStyle.Render("(nothing here)"))
		outHits := append(stripHits, hitTarget{kind: hitNone})
		return pad(out, fullHeight), padHits(outHits, fullHeight)
	}

	// The rows come from the live registry and the filter on every frame, so a
	// frame can see fewer rows than the cursor last had; the next key press
	// re-clamps the stored cursor, this frame only has to not index past them.
	cursor = clampi(0, cursor, len(rows)-1)

	lw := maxi(width-2, 20)

	left := make([]string, len(rows))
	hits := make([]hitTarget, len(rows))
	for i, r := range rows {
		switch r.kind {
		case rowHeader:
			// A header carrying a rightSegs entry (Commands' and Workers' own
			// counts — work.go's workRows) renders bold label + dim count,
			// ("Commands  47", "Workers  3"); any other header keeps the
			// plain muted form.
			// No leading space of its own — the outer render loop below
			// (padEnd(" "+padEnd(l, lw), width)) already supplies the single
			// leading column every row gets, headers included.
			if len(r.rightSegs) > 0 {
				left[i] = lipgloss.NewStyle().Bold(true).Render(r.label) + faintStyle.Render(r.rightSegs[0].t)
			} else {
				left[i] = mutedStyle.Render(r.label)
			}
			hits[i] = hitTarget{kind: hitNone}
		case rowText:
			// Same rule as rowHeader just above: the outer loop already
			// supplies the row's one leading column, so this adds none of its
			// own, so the empty-Workers line carries a single leading space.
			if r.label != "" {
				left[i] = faintStyle.Render(r.label)
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

	// The band always takes bandH rows of the whole body at the bottom,
	// whatever the cursor row's state or the strip above; the list gets the
	// rest, padded with blank rows when its own rows don't fill them.
	bh := bandH(fullHeight)
	listH := maxi(height-bh, 1)
	bh = maxi(height-listH, 1)

	listLines := left
	hitLines := hits
	if len(left) > listH {
		start, end := window(len(left), cursor, listH)
		listLines = append([]string{}, left[start:end]...)
		hitLines = append([]hitTarget{}, hits[start:end]...)
		if start > 0 && len(listLines) > 0 {
			// The header of whichever section the window starts inside is
			// pinned to its first row, PLUS the "↑ N more" marker — never the
			// marker alone replacing it — so a scrolled list still says
			// whether the rows below are Commands or Workers ("Commands   ↑ 49
			// more").
			header := lipgloss.NewStyle().Bold(true).Render(sectionHeaderAt(rows, start))
			listLines[0] = header + faintStyle.Render("   ↑ "+strconv.Itoa(start)+" more")
			hitLines[0] = hitTarget{kind: hitNone}
		}
		if end < len(left) && len(listLines) > 0 {
			last := len(listLines) - 1
			listLines[last] = faintStyle.Render("  ↓ more")
			hitLines[last] = hitTarget{kind: hitNone}
		}
	}

	detail := m.renderWorkBand(rows[cursor], width, maxi(bh, 1))

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
		// The band is edge to edge, like the footer rule under it: its title
		// rule, dashed rule and tail lines carry their own one-column margin
		// where they want one (bandFields*, bandTail), so no outer inset is
		// added here.
		out = append(out, padEnd(d, width))
		outHits = append(outHits, hitTarget{kind: hitNone})
	}
	out = append(strip, out...)
	outHits = append(stripHits, outHits...)
	return pad(out, fullHeight), padHits(outHits, fullHeight)
}

// sectionHeaderAt is which section a scrolled window starting at row i is
// inside — the nearest rowHeader at or above it in the combined (unwindowed)
// row list. It reads the rowItem list rather than re-parsing rendered text,
// since the header row's own label is already the answer.
func sectionHeaderAt(rows []rowItem, i int) string {
	for j := i; j >= 0 && j < len(rows); j-- {
		if rows[j].kind == rowHeader {
			return rows[j].label
		}
	}
	return "Commands"
}

// The Commands section's one column grid: a two-cell cursor marker, a
// 16-cell name (or, on a target row, connector+key sharing that same 16
// cells), a 10-cell state column ("● running"), a 7-cell elapsed column
// ("4m12s"), then one free column that spends itself on activity, a run
// hint, or a description depending on the row's shape. Verified
// against commands.txt at w=96: the name column ends at screen column 19 on
// every row kind, and the free column begins at column 36 (19 + wState +
// wElapsed) on every row kind.
const (
	wMarker  = 2  // "◆ " / "◇ "
	wName    = 16 // the name column (target rows: connector + key)
	wState   = 10 // "● running"
	wElapsed = 7  // "4m12s"
)

// idleRest is the width an idle row's description gets: the free column plus
// the state and elapsed cells an idle row leaves empty.
func idleRest(width int) int {
	return maxi(width-wMarker-wName, 6)
}

// commandGlyph is the state marker shown to the left of a Commands-section
// row's state word: waiting and failed override everything else, anything
// else — running, starting — gets the plain dot. A worker row never reaches
// this function; workerGlyph owns the Workers section's marker.
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
// diamond ◈, not the dot, which commandGlyph above omits since the Commands
// section never draws a worker row. The caller passes st, read through
// livework.Default.State, the only reader of an entry's state.
func liveGlyph(e *livework.Entry, st livework.State) string {
	switch st {
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
// anything else dim — shared by renderCommandState, renderWorkerState (below)
// and the live band's title so every state-word renderer can differ in
// padding and glyph without duplicating the color mapping itself.
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
		// The Workers section now renders on the exact same grid the
		// Commands section uses (admin-3rgp.72.16): marker, name, state,
		// elapsed, activity, then a right-hand where-column.
		e := wd.worker
		name := padEnd(truncR(e.ID, wName), wName)
		st, _ := livework.Default.State(e.ID)
		state := renderWorkerState(st)
		elapsed := faintStyle.Render(padEnd(elapsedStr(time.Since(e.StartedAt)), wElapsed))
		// used counts the row's own leading column (the "1") plus the fixed
		// grid columns, even though that leading column is prepended by
		// renderWorkBody's caller rather than by this function: the row's
		// total on-screen width is the same either way, and activity/where
		// would each claim one cell too many without it, shifting the
		// where-column one column to the right.
		used := 1 + wMarker + wName + wState + wElapsed
		whereText, whereStyled := whereSeg(e)
		ww := whereWidth(whereText)
		// Drop the where-column rather than overflow when the activity
		// column would be left under eight cells.
		if ww > 0 && width-used-ww < 8 {
			ww, whereStyled = 0, ""
		}
		actW := maxi(width-used-ww, 4)
		activity, _ := livework.Default.Line(e.ID)
		act := padEnd(truncR(activity, actW), actW)
		return marker + name + state + elapsed + faintStyle.Render(act) + whereStyled
	}

	// truncR to wName-1, not wName: a name that exactly fills the column
	// leaves no blank cell before the kind column starts, and the elision
	// mark plus "shell" read as one token (admin-3rgp.72.19). Padding still
	// fills out to wName, so the column's start and width are unchanged.
	name := padEnd(truncR(wd.cmdName, wName-1), wName)
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
			activity, _ := livework.Default.Line(wd.live.ID)
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
	desc := truncR(wd.cmdDesc, idleRest(width))
	return marker + name + faintStyle.Render(desc)
}

// renderGroupRow renders a multi-target parent's own row: two blank cells
// where the marker would be — the marker column is what promises enter will
// run the row, and a group runs nothing itself — then its name, kind and
// description in the same columns every other row uses. A group collapse mode
// has closed (wd.closed) puts "▸" in that marker cell and its hidden target
// count after the name — "▸ prototype (2)" — so it never reads as empty.
func renderGroupRow(wd *workRowData, width int) string {
	desc := faintStyle.Render(truncR(wd.cmdDesc, idleRest(width)))
	bold := lipgloss.NewStyle().Bold(true)
	if !wd.closed {
		// Same reserved cell as renderWorkRow's flat-command branch, for the
		// same reason: a full-width group name must not run straight into the
		// kind column (admin-3rgp.72.19).
		return "  " + bold.Render(padEnd(truncR(wd.cmdName, wName-1), wName)) + desc
	}
	count := " (" + strconv.Itoa(len(wd.targets)) + ")"
	name := truncR(wd.cmdName, maxi(wName-1-len(count), 1))
	label := bold.Render(name) + faintStyle.Render(count) + strings.Repeat(" ", maxi(wName-lipgloss.Width(name)-len(count), 1))
	return faintStyle.Render("▸ ") + label + desc
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
	// An idle row has no state or elapsed to show, so its description starts
	// right after the name column and may use the cells those columns hold on
	// a live row.
	connector := "├ "
	if wd.targetLast {
		connector = "└ "
	}
	keyW := maxi(wName-2, 4)
	key := padEnd(truncR(wd.targetKey, keyW-1), keyW)
	name := key
	if focused {
		name = lipgloss.NewStyle().Foreground(cCyan).Render(key)
	}
	if wd.live != nil {
		st, _ := livework.Default.State(wd.live.ID)
		state := renderCommandState(st)
		elapsed := faintStyle.Render(padEnd(elapsedStr(time.Since(wd.live.StartedAt)), wElapsed))
		activity, _ := livework.Default.Line(wd.live.ID)
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
	hint := truncR(wd.targetDesc, rest+wState+wElapsed)
	return faintStyle.Render(connector) + name + faintStyle.Render(hint)
}

// renderWorkBand is the Work tab's full-width live band (admin-3rgp.72.6): a
// title rule naming whatever the combined cursor sits on plus a count of
// everything else running (bandTitle), a fixed fieldRowsH-row field block
// chosen by the row's own state (bandIdle / bandFieldsCommand /
// bandFieldsWorker), then the row's own live output tail filling the rest.
// The row-list half of the Work tab's layout lives in Model.renderWorkBody
// above; this is the band half.
//
// Contract: given the body width w and the band's own height h, returns
// exactly h lines, whatever row r is. The field block is a fixed fieldRowsH
// slot for every row kind (live command, live worker, idle entry), so
// moving the cursor between rows in different states never moves a row of
// the band beneath it; a block taller than the slot is clipped to it.
func (m Model) renderWorkBand(r rowItem, w, h int) []string {
	wd := r.work
	if wd == nil && m.active == tabWorkers && len(liveWorkers()) == 0 {
		return workersEmptyBand(w, h)
	}
	if wd == nil {
		return blankRows(w, h)
	}

	live := bandEntry(r)
	name := rowName(r, live)

	out := make([]string, 0, h)
	out = append(out, bandTitle(w, live, name))

	if live != nil {
		out = append(out, slotRows(bandFields(live, wd, w), w)...)
		avail := maxi(h-1-fieldRowsH, 1)
		var tail []string
		switch {
		case live.Screen != nil:
			tail = screenTailLines(live.Screen, maxi(w-1, 1), avail)
		case live.Via != "":
			tail = snapshotTailLines(live.ID, maxi(w-1, 1))
		}
		out = append(out, bandTail(tail, avail, w)...)
	} else {
		out = append(out, bandIdle(wd, w)...)
		out = append(out, blankRows(w, maxi(h-1-fieldRowsH-1, 0))...)
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

// bandFields is the field block for live entry e on row data wd: a worker's
// five rows, or a command's log and url rows. The docked band and the
// attached view (terminalview.go's termFields) both draw it, so the two
// always match.
func bandFields(e *livework.Entry, wd *workRowData, w int) []string {
	if e.Worker != nil {
		return bandFieldsWorker(e, w)
	}
	return bandFieldsCommand(wd, w)
}

// bandEntry is the live entry the band previews for row r: a worker row's
// own session, or a command/target row's running entry. nil when r has none.
func bandEntry(r rowItem) *livework.Entry {
	if r.work == nil {
		return nil
	}
	if r.kind == rowWorker {
		return r.work.worker
	}
	return r.work.live
}

// othersRunning is the live band title's right side: how much is live that
// is NOT the row being previewed, read from the exact same
// livework.Default.List() the row list itself reads (work.go's byID/
// liveWorkers), so the row list and this count can never disagree about
// what is running. An empty registry returns "nothing running".
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

// rowName is the one name the Work tab prints for a row: a worker's own
// name, a target's "<command> <target>", or a command's name. The band
// title reads it directly; the attach breadcrumb and the quit confirm reach
// it through entryName. live is the row's own entry (bandEntry), nil if idle.
func rowName(r rowItem, live *livework.Entry) string {
	if r.kind == rowWorker && live != nil {
		return live.Name
	}
	if r.work.targetKey == "" {
		return r.work.cmdName
	}
	return r.work.cmdName + " " + r.work.targetKey
}

// entryRow is the Commands or Workers row whose live entry has id, with
// collapse off so a hidden target row still counts.
func (m Model) entryRow(id string) (rowItem, *livework.Entry, bool) {
	all := m
	all.workCollapsed = false
	for _, r := range all.workRows() {
		if e := bandEntry(r); e != nil && e.ID == id && r.work != nil {
			return r, e, true
		}
	}
	// A worker's row carries nothing but its entry, so it is read off the
	// registry rather than through the Workers tab, which may not be mounted.
	for _, e := range liveWorkers() {
		if e.ID == id {
			return workerRow(e, e.Name), e, true
		}
	}
	return rowItem{}, nil, false
}

// entryName is rowName for a live entry, found through its row rather than
// parsed out of its registry id ("<command>:<target>", kinds.TargetLiveKey).
// An entry with no row keeps its own Name.
func (m Model) entryName(e *livework.Entry) string {
	if r, live, ok := m.entryRow(e.ID); ok {
		return rowName(r, live)
	}
	return e.Name
}

// bandTitle is the rule between the row list and the live band: the
// previewed row's own identity on the left, once, and the count of
// everything else running on the right. live nil means the
// previewed row is idle; name is the row's own identity (a command's name,
// a target's "<cmd> <target>", or a worker's own name).
func bandTitle(w int, live *livework.Entry, name string) string {
	var titlePlain, titleStyled, selID string
	if live != nil {
		selID = live.ID
		st, _ := livework.Default.State(live.ID)
		glyph := liveGlyph(live, st)
		state := string(st)
		elapsed := elapsedStr(time.Since(live.StartedAt))
		titlePlain = glyph + " " + name + "  " + state + "  " + elapsed
		titleStyled = lipgloss.NewStyle().Foreground(stateColor(st)).Render(glyph) + " " +
			lipgloss.NewStyle().Bold(true).Render(name) +
			faintStyle.Render("  "+state+"  "+elapsed)
	} else {
		titlePlain = name + "  not running"
		titleStyled = lipgloss.NewStyle().Bold(true).Render(name) + faintStyle.Render("  not running")
	}

	return bandRule(w, titlePlain, titleStyled, othersRunning(selID))
}

// workersEmptyBand is the empty Workers tab's band: the same rule, field slot
// and last line as an idle command's band, so the tab keeps its one layout
// before the first worker exists. Nothing is previewed, so the right side counts what
// is running rather than what else is.
func workersEmptyBand(w, h int) []string {
	right := strings.TrimPrefix(othersRunning(""), "also ")
	if right != "nothing running" {
		right += " running"
	}
	out := []string{bandRule(w, "no worker", faintStyle.Render("no worker"), right)}
	out = append(out, blankRows(w, maxi(h-2, 0))...)
	out = append(out, padEnd(" "+faintStyle.Render("a worker's log, branch, worktree and session appear here"), w))
	return out[:maxi(h, 0)]
}

// bandRule draws the band's title rule: titleStyled (whose visible text is
// titlePlain) on the left, right on the right, dashes between.
func bandRule(w int, titlePlain, titleStyled, right string) string {
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

// padRows pads every row of a field block to w without changing how many
// rows there are: the attached view (terminalview.go's termFields) sizes its
// field block to its content, and the docked band slots the same rows into
// fieldRowsH through slotRows.
func padRows(out []string, w int) []string {
	for i, l := range out {
		out[i] = padEnd(l, w)
	}
	return out
}

// slotRows pads (or clips) a field block to exactly fieldRowsH rows, each
// already padded to w, so a block that would otherwise grow or shrink with
// what it has to say never moves a row of the band beneath it.
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

// bandIdle is the band's field block while the cursor sits on a command that
// has no live entry: kind, description, last run and last log, plus the query
// a query-sourced workflow will dispatch over. A never-run command reads
// "never" and "—". "last run" is the resolved log file's mtime. A target
// row's kind is the literal "target" (commands.txt), not its parent's.
func bandIdle(wd *workRowData, w int) []string {
	lw := maxi(w-11, 8)
	kind := wd.cmdKind
	if kind == "multi-target" {
		kind = "multi-tgt"
	}
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
	rows := []string{
		" " + faintStyle.Render(padEnd("kind", 9)) + truncR(kind, lw),
		" " + faintStyle.Render(padEnd("desc", 9)) + truncR(wd.cmdDesc, lw),
		" " + faintStyle.Render(padEnd("last run", 9)) + faintStyle.Render(lastRun),
		" " + faintStyle.Render(padEnd("last log", 9)) + logCell,
	}
	if wd.workflow != nil {
		rows = append(rows, " "+faintStyle.Render(padEnd("query", 9))+truncR(wd.workflow.Query, lw))
	}
	return slotRows(rows, w)
}

// bandFieldsCommand is the band's field block for a live (running) command:
// its log path, then one row per configured url — sized to its own content:
// a command with no url gets no url row at all, and one with several gets a
// row for every one of them rather than being clipped at a fixed slot.
// The url row reuses urlRow's own reachability indicator, since the app
// probes each url.
func bandFieldsCommand(wd *workRowData, w int) []string {
	lw := maxi(w-11, 8)
	out := []string{" " + faintStyle.Render(padEnd("log", 9)) + faintStyle.Render(fitPath(wd.logPath, lw))}
	for _, u := range wd.urls {
		out = append(out, " "+faintStyle.Render(padEnd("url", 9))+urlRow(u, true, lw))
	}
	return padRows(out, w)
}

// bandFieldsWorker is the band's field block for a live worker: log,
// branch, worktree, session, now — always all five, since a worker (unlike
// a command) has no idle state to preview instead. "now" is the worker's own
// self-reported Activity.
func bandFieldsWorker(e *livework.Entry, w int) []string {
	lw := maxi(w-11, 8)
	var branch, worktree string
	if e.Worker != nil {
		branch, worktree = e.Worker.Branch, e.Worker.Worktree
	}
	activity, _ := livework.Default.Line(e.ID)
	logPath, _ := livework.Default.LogPath(e.ID)
	return padRows([]string{
		" " + faintStyle.Render(padEnd("log", 9)) + faintStyle.Render(fitPath(logPath, lw)),
		" " + faintStyle.Render(padEnd("branch", 9)) + lipgloss.NewStyle().Foreground(cCyan).Render(truncR(branch, lw)),
		" " + faintStyle.Render(padEnd("worktree", 9)) + faintStyle.Render(fitPath(worktree, lw)),
		" " + faintStyle.Render(padEnd("session", 9)) + sessionField(e, lw),
		" " + faintStyle.Render(padEnd("now", 9)) + faintStyle.Render(truncR(activity, lw)),
	}, w)
}

// sessionField is the worker band's "session" value: hosted-here vs a
// detached launcher pane — decided from Via,
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
// and reads as corruption. A log path is always shown absolute, never
// relative to the process's cwd.
func fitPath(p string, w int) string {
	if vlen(p) <= w {
		return p
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	base := segs[len(segs)-1]
	if w >= 12 {
		for k := len(segs) - 1; k >= 1; k-- {
			if cand := "/" + segs[0] + "/…/" + strings.Join(segs[len(segs)-k:], "/"); vlen(cand) <= w {
				return cand
			}
		}
	}
	// Root + basename doesn't fit even elided down to one middle segment.
	// The basename is never elided, so it — not the front of the path —
	// is what survives from here down.
	if cand := "…/" + base; vlen(cand) <= w {
		return cand
	}
	if vlen(base) <= w {
		return base
	}
	// Budget too small even for the bare basename: something has to give,
	// so the basename itself is truncated rather than the path being cut
	// from the front (which would drop the basename entirely).
	return truncR(base, maxi(w, 1))
}

// screenTailLines is the band's text for a hosted entry: the last n rows of
// its terminal (livework.Entry.Screen, the vt emulator the hosted pty writes
// into) that carry words, top to bottom, as plain text. It reads the screen
// from the bottom up and continues into the main screen's scrollback, so a
// band taller than the pty still fills with the lines that scrolled off; a
// child on the alternate screen (a full-screen agent) shows its screen alone.
// The emulator has already applied every escape, tab, carriage return and
// cursor move the child wrote — a docker buildx progress block redrawn with
// cursor-up leaves only its latest rows — so each cell is printable text and
// no control byte reaches bubbletea's frame. Rows that are empty or only
// rules and box lines are dropped, and each is truncated to w cells.
func screenTailLines(screen *vt.SafeEmulator, w, n int) []string {
	cols := screen.Width()
	row := func(cell func(x int) *uv.Cell) string {
		var b strings.Builder
		for x := 0; x < cols; x++ {
			c := cell(x)
			switch {
			case c != nil && c.Width == 0:
				// A wide rune's continuation cell.
			case c == nil || c.Content == "":
				b.WriteByte(' ')
			default:
				b.WriteString(c.Content)
			}
		}
		return strings.TrimRight(b.String(), " \t")
	}
	var lines []string
	keep := func(l string) bool {
		if logformat.HasWords(l) {
			lines = append(lines, truncR(l, maxi(w, 1)))
		}
		return len(lines) < n
	}
	more := true
	for y := screen.Height() - 1; y >= 0 && more; y-- {
		more = keep(row(func(x int) *uv.Cell { return screen.CellAt(x, y) }))
	}
	if !screen.IsAltScreen() {
		for y := screen.ScrollbackLen() - 1; y >= 0 && more; y-- {
			more = keep(row(func(x int) *uv.Cell { return screen.ScrollbackCellAt(x, y) }))
		}
	}
	slices.Reverse(lines)
	return lines
}

// snapshotTailLines is the band's text for a launcher-dispatched entry: the
// last pane snapshot [terminal].read produced, or — when none has arrived,
// which is every entry of a launcher declaring no read command — the
// attach hint.
func snapshotTailLines(id string, w int) []string {
	snap, _ := livework.Default.Snapshot(id)
	if lines := plainTailLines(snap, w); len(lines) > 0 {
		return lines
	}
	return []string{truncR(noLocalOutput, maxi(w, 1))}
}

// plainTailLines turns a launcher pane snapshot's text into the band's
// rows: ANSI escapes stripped, CRLF normalized, each line flattened by
// logformat.PlainLine, rows without a letter or digit dropped, and each
// truncated to w cells. The band writes these rows straight into bubbletea's frame, so
// any control byte left in one reaches the real terminal and moves its
// cursor.
func plainTailLines(raw string, w int) []string {
	raw = logformat.AnsiEscapeRe.ReplaceAllString(raw, "")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	var lines []string
	for _, l := range strings.Split(raw, "\n") {
		if l = logformat.PlainLine(l); logformat.HasWords(l) {
			lines = append(lines, truncR(l, maxi(w, 1)))
		}
	}
	return lines
}

// noLocalOutput is the band's text for a launcher-dispatched entry with no
// pane snapshot.
const noLocalOutput = "no local output - attach to see the real session"

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

// workerGlyph is the Workers-section state marker: the same waiting/failed
// overrides as commandGlyph, but the default is the diamond ◈ — a worker row
// is always a genuine agent session, never the plain-dot default commandGlyph
// falls back to for a running/starting command. renderWorkRow only reaches
// this for r.kind == rowWorker, so the diamond applies unconditionally.
func workerGlyph(s livework.State) string {
	switch s {
	case livework.StateWaiting:
		return "◐"
	case livework.StateFailed:
		return "✗"
	default:
		return "◈"
	}
}

// renderWorkerState renders a livework.State as the Workers grid's
// fixed-width, color-coded "<glyph> <word>" padded to wState — the same
// shape renderCommandState gives the Commands section, but with workerGlyph's
// diamond default instead of commandGlyph's dot.
func renderWorkerState(s livework.State) string {
	word := workerGlyph(s) + " " + string(s)
	return lipgloss.NewStyle().Foreground(stateColor(s)).Render(padEnd(word, wState))
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
