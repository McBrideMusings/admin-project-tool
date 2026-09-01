package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/McBrideMusings/admin-project-tool/internal/data"
)

// hitKind identifies what a rendered line — or, for a split line, one side of
// it — represents for mouse hit-testing.
type hitKind int

const (
	hitNone   hitKind = iota // nothing here: chrome, a border, a blank fill
	hitTabBar                // the tab-bar line; see hitTarget.tabs
	hitRow                   // a pane row; see hitTarget.pane/row (and .pane2/row2)
)

// tabHit is one visible tab's horizontal cell range on the tab-bar line —
// inclusive of x0, exclusive of x1.
type tabHit struct {
	tab    tab
	x0, x1 int
}

// hitTarget names what a click on this rendered line hits. Every render
// function feeding renderBody returns a hits slice exactly as long as its
// lines, so View joins lines and Update indexes hits by mouse Y.
//
// Most lines are single-target: kind, pane and row say which row of which
// pane a click anywhere on the line lands on (pane is meaningful only on the
// Local tab; zero — paneBranches — elsewhere and unused there). row is -1
// when the line has content but nothing selectable: a border, a blank fill, a
// section heading, a "N more" hint.
//
// Two shapes need more than that single target:
//   - the tab bar line, where which tab a click hits depends on x, not just
//     y — it carries tabs instead of pane/row (kind hitTabBar);
//   - a Local-tab body line, where Branches sits left of Commits/Stashes at
//     the same y — split is set, and x < splitX resolves to pane/row while
//     x >= splitX resolves to pane2/row2.
type hitTarget struct {
	kind hitKind
	pane localPane
	row  int

	split  bool
	splitX int
	pane2  localPane
	row2   int

	tabs []tabHit
}

// noneHits is a run of hitNone targets, for lines produced by a render path
// that has nothing clickable — an overlay or modal drawn over the dashboard.
func noneHits(n int) []hitTarget {
	if n <= 0 {
		return nil
	}
	out := make([]hitTarget, n)
	return out
}

// padHits mirrors pad(lines, n) for a hits slice, so the two never drift out
// of step.
func padHits(hits []hitTarget, n int) []hitTarget {
	for len(hits) < n {
		hits = append(hits, hitTarget{kind: hitNone})
	}
	if len(hits) > n {
		hits = hits[:n]
	}
	return hits
}

// View renders a fixed-height frame: the header and tab bar are pinned at the
// top and the footer at the bottom, with the body scrolling inside the bounded
// region between them. The total output is exactly m.height lines so the
// alt-screen never scrolls and the header can never fall off the top.
func (m Model) View() string {
	lines, _ := m.frame()
	return strings.Join(lines, "\n")
}

// frame renders the whole screen and, for each line, what a click on that
// line hits. hits is exactly as long as lines. View joins lines; Update
// indexes hits by mouse Y.
func (m Model) frame() (lines []string, hits []hitTarget) {
	lines, hits = m.frameRaw()
	lines = m.applyHighlight(lines)
	return lines, hits
}

// frameRaw is frame() before the drag-to-copy highlight is spliced in — what
// Update's mouse handling reads to resolve a click, and what the copy path
// reads to extract the selected text.
func (m Model) frameRaw() (lines []string, hits []hitTarget) {
	width, height, bodyHeight := m.frameGeom()

	header := truncateLine(m.headerLine(width), width)
	tabs := truncateLine(m.tabBar(), width)
	footerLines := m.footerLines(width)

	// Top chrome: header, tab bar, one blank separator.
	top := []string{header, tabs, ""}
	topHits := []hitTarget{{kind: hitNone}, m.tabBarHit(), {kind: hitNone}}
	body, bodyHits := m.renderBodyHits(width, bodyHeight)

	lines = make([]string, 0, height)
	lines = append(lines, top...)
	lines = append(lines, body...)
	lines = append(lines, footerLines...)

	hits = make([]hitTarget, 0, height)
	hits = append(hits, topHits...)
	hits = append(hits, bodyHits...)
	for range footerLines {
		hits = append(hits, hitTarget{kind: hitNone})
	}
	return lines, hits
}

// frameGeom is the line budget View lays the frame out against: the usable width
// and height, and how many rows the body gets between the top chrome and the
// footer.
func (m Model) frameGeom() (width, height, bodyHeight int) {
	width, height = m.width, m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	const bodyTop = 3 // header, tab bar, blank separator
	bodyHeight = height - bodyTop - len(m.footerLines(width))
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	return width, height, bodyHeight
}

// footerLines is the footer, split and clipped to width.
func (m Model) footerLines(width int) []string {
	lines := strings.Split(m.footer(width), "\n")
	for i, l := range lines {
		lines[i] = truncateLine(l, width)
	}
	return lines
}

// renderBodyHits is renderBody plus the hit map for every line it returns.
// Every branch feeding it returns hits alongside its lines: the overlays and
// modals (errDetail, bead, confirm/delete) and the Config tab have nothing
// clickable, so noneHits is correct and fine for them; the Local tab and the
// flat row lists (renderLocalPanels, renderList) resolve real targets.
func (m Model) renderBodyHits(width, bodyHeight int) ([]string, []hitTarget) {
	contentH := bodyHeight - 1
	if contentH < 1 {
		contentH = 1
	}
	var content []string
	var hits []hitTarget
	switch {
	case m.errOpen:
		content = m.renderErrDetailBody(width, contentH)
		hits = noneHits(len(content))
	case m.beadOpen:
		content = m.renderBeadOverlay(width, contentH)
		hits = noneHits(len(content))
	case m.confirming || m.deleting:
		content = m.renderModalBody(width, contentH)
		hits = noneHits(len(content))
	case m.swOpen:
		content = m.renderWorkflowPickerBody(width, contentH)
		hits = noneHits(len(content))
	case m.wfResolving || m.wfOpen:
		content = m.renderWorkflowConfirmBody(width, contentH)
		hits = noneHits(len(content))
	case m.active == tabWork:
		content, hits = m.renderWorkBody(width, contentH)
	case m.active == tabLocal:
		content, hits = m.renderLocalPanels(width, contentH)
	case m.active == tabConfig:
		content = m.renderConfigBody(width, contentH)
		hits = noneHits(len(content))
	default:
		content, hits = m.renderList(width, contentH)
	}
	content = pad(content, contentH)
	hits = padHits(hits, contentH)
	content = append(content, m.ruleLine(width))
	hits = append(hits, hitTarget{kind: hitNone})
	return content, hits
}

func (m Model) ruleLine(width int) string {
	return renderSegs([]seg{{t: strings.Repeat("─", width), fg: cRule}}, nil)
}

// centerMessage renders a single dim message centered horizontally and vertically
// within a width×height block — the empty-state for a view with nothing to show.
func centerMessage(msg string, width, height int) []string {
	out := make([]string, height)
	mid := (height - 1) / 2
	leftPad := (width - vlen(msg)) / 2
	if leftPad < 0 {
		leftPad = 0
	}
	for i := range out {
		if i == mid {
			out[i] = padN(leftPad) + mutedStyle.Render(msg)
		} else {
			out[i] = ""
		}
	}
	return out
}

// renderList renders the flat row list (PRs/Issues/Beads) into height lines,
// alongside which row each line hits — row i is exactly rows[i], the same
// index m.cursor[m.active] and currentRow() already address.
func (m Model) renderList(width, height int) ([]string, []hitTarget) {
	rows := m.visibleRows()
	cursor := m.cursor[m.active]

	// Whole-view empty state: a single centered message (e.g. Cleanup, nothing to do).
	if len(rows) == 1 && rows[0].kind == rowCenter {
		return centerMessage(rows[0].label, width, height), noneHits(height)
	}

	lines := make([]string, 0, height)
	hits := make([]hitTarget, 0, height)
	if len(rows) == 0 {
		lines = append(lines, "  "+mutedStyle.Render("(nothing here)"))
		hits = append(hits, hitTarget{kind: hitNone})
		return pad(lines, height), padHits(hits, height)
	}

	start, end := window(len(rows), cursor, height)
	moreAbove := start > 0
	moreBelow := end < len(rows)

	if moreAbove {
		lines = append(lines, faintStyle.Render("  ↑ "+fmt.Sprintf("%d", start)+" more"))
		hits = append(hits, hitTarget{kind: hitNone})
		start++
	}
	for i := start; i < end; i++ {
		if moreBelow && len(lines) == height-1 {
			break
		}
		lines = append(lines, m.renderRow(rows[i], i == cursor, width))
		if rows[i].selectable() {
			hits = append(hits, hitTarget{kind: hitRow, row: i})
		} else {
			hits = append(hits, hitTarget{kind: hitNone})
		}
	}
	if moreBelow {
		lines = append(lines, faintStyle.Render("  ↓ "+fmt.Sprintf("%d", len(rows)-(end-1))+" more"))
		hits = append(hits, hitTarget{kind: hitNone})
	}
	return pad(lines, height), padHits(hits, height)
}

// window returns the [start,end) slice of rows to show so the cursor stays
// visible, biased toward keeping the cursor roughly centered.
func window(total, cursor, height int) (int, int) {
	if total <= height {
		return 0, total
	}
	start := cursor - height/2
	if start < 0 {
		start = 0
	}
	if start > total-height {
		start = total - height
	}
	return start, start + height
}

func pad(lines []string, n int) []string {
	for len(lines) < n {
		lines = append(lines, "")
	}
	if len(lines) > n {
		lines = lines[:n]
	}
	return lines
}

// truncateLine clips a (possibly ANSI-styled) line to width columns.
func truncateLine(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

func (m Model) headerLine(width int) string {
	branch := m.current
	if branch == "" {
		branch = "(detached)"
	}
	nwo := m.nwo
	if nwo == "" {
		nwo = "(no remote)"
	}
	// Both names in the header are OSC-8 hyperlinks when origin is GitHub: the repo
	// to its GitHub page, the current branch to its branch page — the latter only
	// when origin carries that branch, since a local-only branch has no page there.
	branchSeg := seg{t: branch, fg: cGreen}
	if m.branchOnOrigin(m.current) {
		branchSeg.link = branchURL(m.nwo, m.current)
	}
	left := []seg{
		{t: nwo, fg: cFg, link: repoURL(m.nwo)}, {t: "  " + branchGlyph(), fg: cDim}, branchSeg,
	}
	if m.haveLocal {
		if m.local.status.Clean() {
			left = append(left, seg{t: "  ✔ clean", fg: cDim})
		} else {
			s := m.local.status
			left = append(left, seg{t: fmt.Sprintf("  +%d ~%d ?%d", s.Staged, s.Unstaged, s.Untracked), fg: cYellow})
		}
	}
	var right []seg
	switch {
	case m.trackerSelectMode() && (m.active == tabBeads || m.active == tabIssues):
		// The count is the whole visibility contract for admin-3rgp.27's
		// acceptance criterion "the count is visible": the per-row checkmark
		// says WHICH rows, this says HOW MANY, and it takes priority over the
		// header's usual sync/freshness readout because it's the more
		// actionable fact while a selection is in progress.
		right = []seg{{t: fmt.Sprintf("%d selected", len(m.trackerSelected)), fg: cGreen}}
	case m.creating:
		right = []seg{{t: "⟳ " + m.createLabel, fg: cCyan}}
	case m.isRefreshing():
		right = []seg{{t: "⟳ syncing", fg: cDim}}
	case m.remote.PRsFetchError != "" || m.remote.IssuesFetchError != "" || m.remote.MergedFetchError != "":
		// Any section of the one sync failing is the sync failing. Persists until
		// the next successful one clears it — otherwise a section silently keeps
		// showing data older than the header's age label admits.
		right = []seg{{t: "⚠ sync failed", fg: cRed}}
	case m.active == tabBeads || m.active == tabIssues:
		// The active sort order, named so it's readable without opening
		// anything (admin-wihc). Freshness still shows alongside it — the
		// order is the more actionable fact, but the data's age doesn't stop
		// mattering because the list got reordered.
		right = []seg{{t: "sort: " + m.trackSort.label(), fg: cDim}}
		if m.haveData {
			right = append(right, seg{t: "  ", fg: cFaint}, m.freshnessSeg())
		}
	case m.haveData:
		right = []seg{m.freshnessSeg()}
	}
	return renderSegs(composeLR(left, right, width), nil)
}

// freshnessSeg is the header's right-side cache indicator: the age of the data
// the active tab is showing, tinted yellow once it's past TTL so stale data reads
// as stale at a glance instead of a flat "cached" that hides how old it is. One
// sync stamps all three sections together, so these normally agree — they part
// company only when a section failed its fetch and kept its older stamp, which is
// exactly when the difference is worth seeing.
func (m Model) freshnessSeg() seg {
	var at time.Time
	switch m.active {
	case tabWork:
		// The manifest and the live-work registry are both read fresh on every
		// frame — no fetch, no cache, exactly like Beads below.
		return seg{t: "live", fg: cFaint}
	case tabBeads:
		// Beads are read straight out of the local database on a short clock, so
		// there is no cache age to report — what is on screen is what the workspace
		// said seconds ago.
		return seg{t: "live", fg: cFaint}
	case tabPRs:
		at = m.remote.PRsFetchedAt
	case tabIssues:
		at = m.remote.IssuesFetchedAt
	case tabLocal:
		// The Repo tab's remote input is the merged-PR set: what strikes a
		// squash-merged branch through.
		at = m.remote.MergedFetchedAt
	}
	if at.IsZero() {
		return seg{t: "cached", fg: cFaint}
	}
	age := time.Since(at)
	fg := cFaint
	if age >= data.TTL {
		fg = cYellow
	}
	return seg{t: "cached · " + humanizeAge(age), fg: fg}
}

// humanizeAge renders a duration as a compact "just now / Nm ago / Nh ago".
func humanizeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	default:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	}
}

// tabDisplay is the tab-bar label; the Local tab renders as "Repo" per the design.
func tabDisplay(t tab) string {
	if t == tabLocal {
		return "Repo"
	}
	return tabNames[t]
}

func (m Model) tabCount(t tab) int {
	switch t {
	case tabWork:
		return m.workRowCount()
	case tabPRs:
		return len(m.remote.PRs)
	case tabIssues:
		return len(m.remote.Issues)
	case tabBeads:
		ready, inProgress, blocked := groupBeads(m.beads)
		return len(ready) + len(inProgress) + len(blocked)
	}
	return 0
}

// tabBarSegs builds the tab bar's segments and, for each visible tab, its
// horizontal cell range within the line — the one piece of column math
// tabBar() and the tab-bar hit-test both need, computed once so a click can
// never disagree with the bar about where one tab's region ends and the next
// begins.
func (m Model) tabBarSegs() (segs []seg, ranges []tabHit) {
	lead := seg{t: " "}
	segs = []seg{lead}
	x := vlen(lead.t)
	for i, t := range m.visibleTabs() {
		if i > 0 {
			sep := seg{t: "   "}
			segs = append(segs, sep)
			x += vlen(sep.t)
		}
		start := x
		label := tabDisplay(t)
		badge := ""
		if n := m.tabCount(t); t != tabLocal && t != tabConfig {
			badge = fmt.Sprintf("%d", n)
		}
		if t == m.active {
			txt := " " + label
			if badge != "" {
				txt += " " + badge
			}
			txt += " "
			s := seg{t: txt, fg: cBg, bg: cBlue}
			segs = append(segs, s)
			x += vlen(s.t)
			ranges = append(ranges, tabHit{tab: t, x0: start, x1: x})
			continue
		}
		s1 := seg{t: " " + label, fg: cDim}
		segs = append(segs, s1)
		x += vlen(s1.t)
		if badge != "" {
			s2 := seg{t: " " + badge, fg: cCyan}
			segs = append(segs, s2)
			x += vlen(s2.t)
		}
		s3 := seg{t: " ", fg: cDim}
		segs = append(segs, s3)
		x += vlen(s3.t)
		ranges = append(ranges, tabHit{tab: t, x0: start, x1: x})
	}
	return segs, ranges
}

func (m Model) tabBar() string {
	segs, _ := m.tabBarSegs()
	return renderSegs(segs, nil)
}

// tabBarHit is the tab-bar line's hitTarget: which tab a click at a given x
// resolves to.
func (m Model) tabBarHit() hitTarget {
	_, ranges := m.tabBarSegs()
	return hitTarget{kind: hitTabBar, tabs: ranges}
}

// leadSeg is the cursor marker for a list row.
func leadSeg(selected bool) seg {
	if selected {
		return seg{t: "▸", fg: cBlue}
	}
	return seg{t: " "}
}

func (m Model) renderRow(r rowItem, selected bool, width int) string {
	switch r.kind {
	case rowHeader:
		left := []seg{{t: " "}, {t: r.label, fg: cBright}}
		if len(r.rightSegs) > 0 {
			return renderSegs(fitSegs(composeLR(left, r.rightSegs, width), width), nil)
		}
		return renderSegs(fitSegs(left, width), nil)
	case rowRule:
		return m.ruleLine(width)
	case rowText:
		return renderSegs(fitSegs([]seg{{t: "   "}, {t: r.label, fg: cFaint}}, width), nil)
	}
	left := []seg{leadSeg(selected)}
	// The tracker multi-select checkbox (admin-3rgp.27): a fixed two-column
	// glyph on every rowBead/rowIssue row once mode is on, so a row's title
	// starts at the same column whether that row is picked or not — the same
	// "the checkbox is always drawn, blank when it doesn't apply" contract
	// gutterSegs already keeps for the Branches pane's own checkbox.
	if (r.kind == rowBead || r.kind == rowIssue) && m.trackerSelectMode() {
		mark := seg{t: "  "}
		if m.trackerSelected[trackerRowID(r)] {
			mark = seg{t: "✓ ", fg: cGreen}
		}
		left = append(left, mark)
	}
	left = append(left, r.leftSegs...)
	line := composeLR(left, r.rightSegs, width)
	if selected {
		return renderSegs(fitSegs(line, width), cSel)
	}
	return renderSegs(fitSegs(line, width), nil)
}

func (m Model) footer(width int) string {
	if m.beadOpen {
		return beadOverlayFooter()
	}
	if m.errOpen {
		return renderSegs([]seg{
			{t: " "}, {t: "any key", fg: cCyan}, {t: " close", fg: cDim},
		}, nil)
	}
	if m.deleting {
		hint := "deleting…"
		if m.deleteDone {
			hint = "press any key to dismiss"
		}
		return renderSegs([]seg{{t: " "}, {t: hint, fg: cDim}}, nil)
	}
	if m.naming {
		hint, hintFg := "(name, #number or URL · enter to create · esc to cancel)", cFaint
		if m.resolving {
			hint = "(resolving… · esc to cancel)"
		}
		if m.nameErr != "" {
			hint, hintFg = "("+m.nameErr+" · esc to cancel)", cRed
		}
		line := renderSegs([]seg{
			{t: " "}, {t: "new worktree: ", fg: cDim}, {t: m.nameBuf, fg: cFg},
			{t: "▌", fg: cBlue},
			{t: "   " + hint, fg: hintFg},
		}, nil)
		for _, cand := range m.nameCandLines(width) {
			line += "\n" + cand
		}
		return line
	}
	if m.confirming {
		return renderSegs([]seg{
			{t: " "}, {t: "y", fg: cCyan}, {t: " confirm", fg: cDim},
			{t: "   "}, {t: "any key", fg: cCyan}, {t: " cancel", fg: cDim},
		}, nil)
	}
	if m.filtering || m.filter != "" {
		return m.filterFooter()
	}
	keys := m.keybar(width)
	if m.status != "" {
		fg := m.statusFg
		if fg == nil {
			fg = cDim
		}
		if m.statusHint != "" {
			right := []seg{{t: m.statusHint + " ", fg: cCyan}}
			msg := truncateLine(m.status, width-segsWidth(right)-2)
			line := composeLR([]seg{{t: " "}, {t: msg, fg: fg}}, right, width)
			return renderSegs(line, nil) + "\n" + keys
		}
		return renderSegs([]seg{{t: " "}, {t: m.status, fg: fg}}, nil) + "\n" + keys
	}
	return keys
}

// nameCandLines renders the new-worktree prompt's bead search as extra footer
// rows: id / priority / type / title, in the same fixed-width columns the Beads
// pane draws, so the search list reads as a table like every other list in this
// repo (see CLAUDE.md, "Row layout: every list pane is a table"). Reusing
// beadLayoutFor rather than hand-rolling padding means these columns can never
// disagree with the Beads pane's own.
//
// Empty in every case that predates this feature — no beads loaded, or nothing
// typed yet — so a repo with no beads workspace draws a footer byte-identical to
// before this prompt could search anything.
func (m Model) nameCandLines(width int) []string {
	if len(m.nameCands) == 0 {
		return nil
	}
	l := beadLayoutFor([]string{"id", "priority", "type", "title"}, width, m.beadCtx(false), m.nameCands)
	lines := make([]string, len(m.nameCands))
	for i, b := range m.nameCands {
		left, right := l.row(beadNode{b: b}, width)
		selected := i == m.nameSel
		line := append([]seg{leadSeg(selected)}, left...)
		fitted := fitSegs(composeLR(line, right, width), width)
		if selected {
			lines[i] = renderSegs(fitted, cSel)
		} else {
			lines[i] = renderSegs(fitted, nil)
		}
	}
	return lines
}

// keymap pairs for the active tab/pane.
func (m Model) keymap() [][2]string {
	// `←→ tab` (switch top-level tab) is appended last on every view so it always
	// sits at the end of the help line, after the view-specific keys.
	tabKey := [2]string{"←→", "tab"}
	if m.active == tabLocal {
		switch m.pane {
		case paneCommits:
			return [][2]string{{"↑↓", "move"}, {"↵", "copy sha"}, {"o", "open ↗"}, {"⇥", "pane"}, {"/", "filter"}, {"r", "refresh"}, tabKey}
		case paneStashes:
			return [][2]string{{"↑↓", "move"}, {"↵", "details"}, {"⇥", "pane"}, {"/", "filter"}, {"r", "refresh"}, tabKey}
		default:
			return [][2]string{{"↑↓", "move"}, {"↵", "checkout"}, {"⌥↵/W", "worktree"}, {"w", "new"}, {"x", "delete"}, {"p", "pull"}, {"⇥", "pane"}, {"/", "filter"}, {"r", "refresh"}, tabKey}
		}
	}
	switch m.active {
	case tabWork:
		return [][2]string{{"↑↓", "move"}, {"↵", "open, run or attach"}, {"→←", "submenu"}, {"a", "attach"}, {"k", "kill"}, {"/", "filter"}, {"q", "quit"}, tabKey}
	case tabPRs:
		return [][2]string{{"↑↓", "move"}, {"↵", "checkout"}, {"o", "open"}, {"i", "issue"}, {"/", "filter"}, {"r", "refresh row"}, {"⇧R", "refresh all"}, {"q", "quit"}, tabKey}
	case tabIssues:
		return [][2]string{{"↑↓", "move"}, {"↵", "start work"}, {"o", "open"}, {"s", "sort"}, {"w", "workflow"}, {"/", "filter"}, {"r", "refresh row"}, {"⇧R", "refresh all"}, {"q", "quit"}, tabKey}
	case tabBeads:
		// No `o`: a bead lives in a local database and has no page to open.
		return [][2]string{{"↑↓", "move"}, {"↵", "start work"}, {"d", "detail"}, {"g", "tree"}, {"s", "sort"}, {"w", "workflow"}, {"/", "filter"}, {"r", "refresh"}, {"q", "quit"}, tabKey}
	case tabConfig:
		// Read-only readout of resolved values (admin-3rgp.21) — nothing to
		// move a cursor over or edit.
		return [][2]string{{"q", "quit"}, tabKey}
	}
	return nil
}

// keybar renders the footer keybindings, dropping whole pairs when they don't all
// fit rather than letting the line be chopped mid-word.
//
// The outer render truncates the footer to the terminal width, so an overlong
// keymap silently loses whatever sits at its end — which is always `←→ tab`, the
// one binding every view shares, and every tab but the Branches pane's own puts
// `q quit` directly ahead of it. That made adding a key to any pane a way to
// delete the tab hint from the footer — and, once a keymap grew past what fit
// at a real terminal width (admin-3rgp.27 adding `space`/`w` to the Beads and
// Issues tabs is exactly this), a way to delete `quit` itself, silently,
// before any other pair. Pairs are dropped from the end of the middle
// backwards instead, keeping the LAST TWO throughout — the first pair
// (movement), and the last two together (however this view's list ends;
// `q quit` immediately before `←→ tab` on every other tab) always survive.
func (m Model) keybar(width int) string {
	pairs := m.keymap()
	for len(pairs) > 3 && keybarWidth(pairs) > width {
		pairs = append(pairs[:len(pairs)-3:len(pairs)-3], pairs[len(pairs)-2:]...)
	}
	segs := []seg{{t: " "}}
	for i, p := range pairs {
		if i > 0 {
			segs = append(segs, seg{t: " · ", fg: cFaint})
		}
		segs = append(segs, seg{t: p[0], fg: cCyan}, seg{t: " "}, seg{t: p[1], fg: cDim})
	}
	return renderSegs(segs, nil)
}

// keybarWidth is the display width keybar would render pairs at: a leading space,
// each "key label" pair, and a " · " between every two.
func keybarWidth(pairs [][2]string) int {
	w := 1
	for i, p := range pairs {
		if i > 0 {
			w += 3
		}
		w += vlen(p[0]) + 1 + vlen(p[1])
	}
	return w
}

func (m Model) filterFooter() string {
	n := m.matchCount()
	segs := []seg{{t: " "}, {t: "/", fg: cCyan}, {t: m.filter, fg: cFg}}
	if m.filtering {
		segs = append(segs, seg{t: "▌", fg: cBlue})
	}
	plural := "es"
	if n == 1 {
		plural = ""
	}
	segs = append(segs, seg{t: "   "}, seg{t: fmt.Sprintf("%d match%s", n, plural), fg: cDim},
		seg{t: "   "}, seg{t: "esc", fg: cCyan}, seg{t: " clear", fg: cDim})
	if m.filtering {
		segs = append(segs, seg{t: "  ·  ", fg: cFaint}, seg{t: "enter", fg: cCyan}, seg{t: " apply", fg: cDim})
	}
	return renderSegs(segs, nil)
}

// matchCount is the number of items the current filter matches, for the footer.
func (m Model) matchCount() int {
	if m.active == tabLocal {
		c, s := m.localPanes()
		return len(m.branchRows()) + len(c) + len(s)
	}
	n := 0
	for _, r := range m.visibleRows() {
		if r.selectable() {
			n++
		}
	}
	return n
}

// renderErrDetailBody draws the last failed action's full git output as a centered
// box. The status bar can only ever show the headline, so this is where the lines
// behind it live: the files a checkout would overwrite, the worktree already
// holding a branch, git's own advice about what to run instead.
func (m Model) renderErrDetailBody(width, height int) []string {
	boxW := clampi(28, width-8, 88)
	if boxW > width {
		boxW = width
	}
	innerW := boxW - 3 // two border cells and the one-space left inset
	if innerW < 1 {
		innerW = 1
	}

	var lines [][]seg
	for raw := range strings.SplitSeq(m.errDetail, "\n") {
		// Git indents the files it is complaining about with a hard tab, whose
		// display width the terminal decides and lipgloss.Width does not — leave it
		// in and the box pads that row to the wrong length, breaking the right
		// border. Expand it here so every line is measured in the cells it occupies.
		raw = strings.ReplaceAll(raw, "\t", "    ")
		raw = strings.TrimRight(raw, " ")
		if raw == "" {
			lines = append(lines, []seg{{t: ""}})
			continue
		}
		// Advice keeps git's own dimmer register so the refusal itself still reads
		// first, exactly as errHeadline picked it out for the status bar.
		fg := cFg
		if strings.HasPrefix(strings.TrimSpace(raw), "hint:") {
			fg = cFaint
		}
		for _, seg1 := range wrapCells(raw, innerW) {
			lines = append(lines, []seg{{t: " " + seg1, fg: fg}})
		}
	}

	title := m.errTitle
	if title == "" {
		title = "error"
	}
	boxH := clampi(3, len(lines)+2, height)
	box := boxPanel{
		title: title, badge: " any key ✕", width: boxW, height: boxH, focused: true,
		lines: lines, active: -1, scroll: 0,
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

// wrapCells breaks s into chunks of at most n display cells. Git prints absolute
// paths and full sentences, so a long line is folded rather than truncated — the
// tail of "would be overwritten by checkout: <path>" is the part worth reading.
func wrapCells(s string, n int) []string {
	if n < 1 {
		return []string{s}
	}
	var out []string
	for vlen(s) > n {
		head := truncCells(s, n)
		if head == "" {
			// A single rune wider than the whole box: take it anyway rather than
			// looping forever on a chunk that can never fit.
			_, size := utf8.DecodeRuneInString(s)
			head = s[:size]
		}
		out = append(out, head)
		s = s[len(head):]
	}
	return append(out, s)
}

// renderModalBody draws the delete modal as a centered box over an empty body —
// the confirm phase (y/n on the batch) and the running/done phase (live per-item
// progress) share one box. It returns exactly height lines.
func (m Model) renderModalBody(width, height int) []string {
	title, badge, lines, active := m.modalContent()

	boxW := clampi(28, width-8, 72)
	if boxW > width {
		boxW = width
	}
	boxH := clampi(3, len(lines)+2, height)
	scroll := clampScroll(0, len(lines), boxH-2, active)
	box := boxPanel{
		title: title, badge: badge, width: boxW, height: boxH, focused: true,
		lines: lines, active: active, scroll: scroll,
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

// modalContent builds the delete modal's title, badge, body lines, and the active
// (highlighted) line index for the current phase.
func (m Model) modalContent() (title, badge string, lines [][]seg, active int) {
	if m.confirming {
		// Each held item is badged with its own reason rather than a blanket
		// "unsaved": this is the last screen before the delete runs, so it has to
		// say which of them is about to lose files that exist nowhere else.
		dirty, blocked := 0, 0
		var guess []string
		for _, t := range m.confirmTargets {
			segs := []seg{{t: " • ", fg: cFaint}, {t: cleanupKindLabel(t), fg: cFg}}
			if t.quarantined {
				// The conflict count rides with the reason rather than replacing it:
				// "merging" says what state the worktree is stuck in, "1 conflict" says
				// how much of it is unresolved, and the delete's outcome turns on both.
				reason := cleanupReason(t)
				if n := conflictNote(t.conflicts); n != "" {
					reason += " · " + n
					blocked++
				}
				segs = append(segs, seg{t: "  [" + reason + "]", fg: cYellow})
			} else if t.conf == confLikely {
				// An unheld guess badges as one. Without this the last screen before the
				// delete renders a branch nothing proved dead exactly like a merged one,
				// which is the single place that distinction matters most — every other
				// surface marks it, and this is the one that runs the deletion.
				guess = append(guess, t.branch)
				segs = append(segs, seg{t: "  [" + strings.Join(t.because, " · ") + "]", fg: cMag})
			}
			if t.dirty {
				dirty++
			}
			lines = append(lines, segs)
		}
		if dirty > 0 {
			lines = append(lines, []seg{{t: fmt.Sprintf(" %d holding uncommitted files — nothing restores those", dirty), fg: cYellow}})
		}
		if blocked > 0 {
			lines = append(lines, []seg{{t: fmt.Sprintf(" %d holding unresolved conflicts — no stash possible, those stop and ask", blocked), fg: cYellow}})
		}
		if len(guess) > 0 {
			lines = append(lines, []seg{{t: fmt.Sprintf(" %d not proven dead — deleted on evidence, not proof", len(guess)), fg: cMag}})
		}
		return fmt.Sprintf("Delete %d item(s)?", len(m.confirmTargets)), "", lines, -1
	}

	// Running / done phase.
	done, failed := 0, 0
	for _, r := range m.deleteResults {
		var g seg
		switch r.status {
		case stRunning:
			g = seg{t: "⟳", fg: cCyan}
		case stDone:
			g = seg{t: "✓", fg: cGreen}
			done++
		case stFailed:
			g = seg{t: "✗", fg: cRed}
			failed++
		default:
			g = seg{t: "○", fg: cFaint}
		}
		segs := []seg{{t: " "}, g, {t: " "}, {t: cleanupKindLabel(r.target), fg: cFg}}
		if r.status == stFailed && r.err != nil {
			segs = append(segs, seg{t: "  " + r.err.Error(), fg: cRed})
		}
		if r.status == stDone && r.stashRef != "" {
			segs = append(segs, seg{t: "  stashed → " + r.stashRef, fg: cMag})
		}
		lines = append(lines, segs)
	}
	if m.deleteDone {
		badge = fmt.Sprintf("%d ✓", done)
		if failed > 0 {
			badge += fmt.Sprintf("  %d ✗", failed)
		}
		// A refusal is only half an answer without the way past it, and this is the
		// one screen where these keys apply — so they are spelled out here rather
		// than left to the footer's general hints.
		var keys [][]seg
		if b := m.blockedCount(); b > 0 {
			keys = append(keys, []seg{
				{t: " f", fg: cYellow},
				{t: fmt.Sprintf("  delete %d anyway — the conflicted files go with them", b), fg: cFg},
			})
		}
		if _, detail := m.deleteErrDetail(); detail != "" {
			keys = append(keys, []seg{
				{t: " e", fg: cCyan},
				{t: "  what git actually said", fg: cFg},
			})
		}
		if len(keys) > 0 {
			lines = append(lines, []seg{{t: ""}})
			lines = append(lines, keys...)
			lines = append(lines, []seg{
				{t: " any other key", fg: cFaint}, {t: "  close", fg: cDim},
			})
		}
		return "Done", badge, lines, -1
	}
	return "Cleaning up", fmt.Sprintf("%d/%d", done+failed, len(m.deleteResults)), lines, m.deleteIdx
}

// selRangeOnLine is the [a, b) cell range of row that selection sel covers —
// the whole line for a row strictly between its start and end, and just the
// anchor/cursor side of it on the row that IS the start or end. Shared by the
// highlight splice and the copy extraction so the two can never disagree
// about which cells a drag actually covers.
func selRangeOnLine(sel selection, row, width int) (a, b int, ok bool) {
	start, end := sel.ordered()
	if row < start.row || row > end.row {
		return 0, 0, false
	}
	a, b = 0, width
	switch {
	case start.row == end.row:
		a, b = start.col, end.col+1
	case row == start.row:
		a, b = start.col, width
	case row == end.row:
		a, b = 0, end.col+1
	}
	if a < 0 {
		a = 0
	}
	if b > width {
		b = width
	}
	if a >= b {
		return 0, 0, false
	}
	return a, b, true
}

// applyHighlight paints the selected cell range of each line the current drag
// covers in a single flat style, leaving every other line untouched. Splicing
// by cell (ansi.Cut) rather than by byte or rune is required because a frame
// line is full of ANSI escapes and box-drawing glyphs; Cut on either side of
// the splice preserves both.
//
// The selected slice is stripped of whatever styling it already carried
// (ansi.Strip) before the flat style is applied, rather than wrapped in a
// reverse-video SGR pair around the existing codes. A row already carries its
// own per-segment colors and, on the cursor row, its own background — a bare
// reverse toggles each of those individually wherever they change mid-range,
// so a drag paints as a patchwork of slivers instead of one solid highlight
// that is legible regardless of what the row underneath was already drawing.
func (m Model) applyHighlight(lines []string) []string {
	if !m.sel.active() {
		return lines
	}
	start, end := m.sel.ordered()
	out := lines
	copied := false
	for row := start.row; row <= end.row; row++ {
		if row < 0 || row >= len(lines) {
			continue
		}
		line := lines[row]
		w := ansi.StringWidth(line)
		a, b, ok := selRangeOnLine(m.sel, row, w)
		if !ok {
			continue
		}
		if !copied {
			out = append([]string(nil), lines...)
			copied = true
		}
		plain := ansi.Strip(ansi.Cut(line, a, b))
		mid := renderSegs([]seg{{t: plain, fg: cBg, bg: cDrag}}, nil)
		out[row] = ansi.Cut(line, 0, a) + mid + ansi.Cut(line, b, w)
	}
	return out
}

// extractSelection builds the plain-text copy payload for sel: each covered
// line's selected cell range, stripped of all styling, joined by newline with
// trailing whitespace trimmed per line. Empty for a bare click.
func (m Model) extractSelection(sel selection) string {
	if !sel.active() {
		return ""
	}
	lines, _ := m.frameRaw()
	start, end := sel.ordered()
	var out []string
	for row := start.row; row <= end.row; row++ {
		if row < 0 || row >= len(lines) {
			continue
		}
		line := lines[row]
		w := ansi.StringWidth(line)
		a, b, ok := selRangeOnLine(sel, row, w)
		if !ok {
			continue
		}
		out = append(out, strings.TrimRight(ansi.Strip(ansi.Cut(line, a, b)), " "))
	}
	return strings.Join(out, "\n")
}
