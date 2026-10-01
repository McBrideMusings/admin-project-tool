package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// localPane identifies the focused panel in the Repo (local) tab.
type localPane int

const (
	paneBranches localPane = iota
	paneCommits
	paneStashes
	paneCleanup
	numPanes
)

// localPanes returns the Commits and Stashes panel item lists after the filter.
// Branches has its own path — branchRows() — because it groups by section rather
// than filtering a flat list; this is what remains once that split happened.
func (m Model) localPanes() (commits []git.Commit, stashes []git.Stash) {
	flt := strings.ToLower(m.filter)
	match := func(s string) bool { return flt == "" || strings.Contains(strings.ToLower(s), flt) }

	for _, c := range m.local.commits {
		if match(c.SHA + " " + c.Subject) {
			commits = append(commits, c)
		}
	}
	for _, s := range m.local.stashes {
		if match(s.Subject) {
			stashes = append(stashes, s)
		}
	}
	return
}

// paneCount returns the selectable row count of a pane — for cursor clamping and
// for deciding whether focus may land there at all. The Branches count is rows,
// not lines: headings and purpose sublines are drawn but cannot be landed on, so
// the cursor addresses exactly the branches the pane lists. paneCleanup counts 0
// because it no longer draws rows of its own.
func (m Model) paneCount(p localPane) int {
	switch p {
	case paneBranches:
		return len(m.branchRows())
	case paneCommits:
		commits, _ := m.localPanes()
		return len(commits)
	case paneStashes:
		_, stashes := m.localPanes()
		return len(stashes)
	default:
		return 0
	}
}

// --- per-row segment builders (sized to inner width W) ---

// rowCell fits a value into exactly n cells — the only way a value becomes a
// column in this pane.
//
// Truncating rather than growing is the whole rule. A cell sized to its content
// shifts every column to its right by however long that row's value happened to
// be, so the table stops being scannable, and the row at the end of the line
// overruns the box into whatever is drawn beside it. Nothing here builds a cell by
// concatenation.
func rowCell(s string, n int, fg lipgloss.TerminalColor) seg {
	return seg{t: padEnd(truncR(s, n), n), fg: fg}
}

// rowCellR is rowCell right-aligned, for the values read as quantities.
func rowCellR(s string, n int, fg lipgloss.TerminalColor) seg {
	t := truncR(s, n)
	return seg{t: padN(n-vlen(t)) + t, fg: fg}
}

// readyColor: green proves deleting is free, yellow says something would be lost,
// blue says it is live work and the question does not arise.
func readyColor(ready string) lipgloss.TerminalColor {
	switch ready {
	case "ready":
		return cGreen
	case "held":
		return cYellow
	}
	return cBlue
}

// stateColor tracks what the word means, not which section it sits in: red is
// something broken, green is a standing approval, dim is finished.
func (m Model) stateColor(r paneRow) lipgloss.TerminalColor {
	switch {
	case len(r.broken) > 0:
		return cRed
	case r.sec == secCleanup:
		return cDim
	case r.state == "approved":
		return cGreen
	case r.state == "draft":
		return cMag
	}
	return cCyan
}

// nullState: the state words that mean "this field has no value" — printed as a
// blank padded cell.
func nullState(s string) bool {
	return s == "empty" || s == "no commits"
}

// stateCell renders the state, blanking the cell when the state is a null word
// but keeping the cell's padding so column offsets do not shift.
func stateCell(r paneRow) string {
	if nullState(r.state) {
		return ""
	}
	return r.state
}

// branchRowLayout is which optional right-hand columns fit at width w, and how
// wide that leaves the name — the one piece of column math brPanelLine and
// brSubLine both need. Computed once so the two can never disagree about where
// the name column ends and the right-hand columns begin.
type branchRowLayout struct {
	keep  map[string]bool
	nameW int
	// purposeW is the inline purpose column's width, or 0 when the purpose is
	// drawn on the row's second line instead.
	purposeW int
}

// branchRowLayout mirrors brPanelLine's own right-column budgeting exactly:
// columns are kept while they fit, least dispensable first, so a narrow pane
// loses whole columns instead of squeezing every value into an ellipsis. A
// dropped column is a decision the row can explain; a line reading "dar…"
// "conf…" "456 …" is one nobody can read. Their left-to-right ORDER never
// changes, whichever survive — a column that moves when the terminal is resized
// is no longer a column.
//
// plan.drop skips a column before the fit test ever runs: a column this row's
// SECTION hoisted onto its own rule (or dropped as constantly empty) consumes
// no width budget at all, and the surplus flows to nameW the same way a column
// that simply didn't fit would. That skip must `continue`, never `break` — a
// dropped column must never stop the scan from reaching a later one that
// would otherwise fit.
func (m Model) branchRowLayout(r paneRow, w int, plan columnPlan) branchRowLayout {
	keep := map[string]bool{}
	g := gutterWidth(m.selectMode())
	// gutter(g) on the left; 1 for the separator composeLR always keeps between
	// the name and the columns; 8 for the least a name can say.
	used := g + 1 + 8 + brAgeW + 1
	rightW := brAgeW
	for _, col := range []struct {
		name string
		w    int
	}{
		{"ready", brReadyW}, {"state", brStateW}, {"owner", brOwnerW}, {"hold", brHoldW},
	} {
		if plan.drop[col.name] {
			continue
		}
		// +1 for the gap that follows every column. Without it a value that exactly
		// fills its width butts against the next one ("remote deletedactive") and the
		// two read as a single word.
		if used+col.w+1 > w {
			break
		}
		used += col.w + 1
		rightW += col.w + 1
		keep[col.name] = true
	}
	// gutter(g) plus 1 for the separator composeLR always inserts between the name
	// and the right-hand columns — flexWidth's `fixed` has to include that forced
	// gap or the row comes out exactly one cell wider than w.
	nameW := flexWidth(w, g+1, rightW, 8)
	// With room for a readable name AND a readable purpose, the purpose is a column
	// on this row and its second line goes away. The split is per layout, and the
	// layout is per section plan, so every row of a section decides the same way.
	// Below that threshold the name keeps the whole flexible width and the purpose
	// stays on its own line, as before.
	if r.sec.showsPurpose() && nameW >= brNameMinW+1+brPurposeMinW {
		name := min(brNameMaxW, nameW-1-brPurposeMinW)
		return branchRowLayout{keep: keep, nameW: name, purposeW: nameW - name - 1}
	}
	return branchRowLayout{keep: keep, nameW: nameW}
}

// stateColumnEnd is the cell offset, from the left edge of the row, where the
// state column's right edge falls — or, if state itself was dropped for width,
// wherever the row's content stops before it. The purpose subline is capped to
// this so it never runs on past where "draft" / "conflicts" / "no commits" sits
// on the row above: the two lines describe the same branch, and a subline that
// sprawls under the hold/ready/age columns reads as unrelated to the state word
// it is actually elaborating on. Note: null state words ("empty" / "no commits")
// are rendered as blank padded cells at the render layer, but are semantically
// intact in r.state for the purpose of stateColor and classifySection.
func (m Model) stateColumnEnd(r paneRow, w int, plan columnPlan) int {
	layout := m.branchRowLayout(r, w, plan)
	// gutter(left fixed) + name + 1(forced separator) is where the right-hand
	// columns begin; owner sits before state when both are kept.
	pos := gutterWidth(m.selectMode()) + layout.nameW + 1
	if layout.keep["owner"] {
		pos += brOwnerW + 1
	}
	if layout.keep["state"] {
		pos += brStateW
	}
	return pos
}

// brGlyph is the row's single leading state glyph: shape carries worktree/
// current status, color carries ownership — so a repo where every branch is
// worktree-backed (every glyph rendering ◇) still distinguishes mine from
// someone else's from unknown by color alone.
func brGlyph(r paneRow, b git.BranchInfo) seg {
	if r.sec == secMain {
		return seg{t: "◆", fg: cGreen}
	}
	if b.WorktreePath != "" {
		color := cCyan
		switch {
		case r.owner.mine:
			color = cGreen
		case r.owner.unknown:
			color = cMag
		}
		return seg{t: "◇", fg: color}
	}
	switch {
	case r.owner.mine:
		return seg{t: "●", fg: cGreen}
	case r.owner.unknown:
		return seg{t: "?", fg: cMag}
	}
	return seg{t: "·", fg: cFaint}
}

// gutterSegs is the row's left gutter, exactly gutterWidth(selectMode) cells
// wide: the multi-select checkbox (select mode only) followed by the single
// state glyph, each with its trailing gap. Both branchRowLayout and
// stateColumnEnd size the name column off gutterWidth, so whatever this emits
// can never disagree with where they expect the name to start.
func (m Model) gutterSegs(r paneRow, b git.BranchInfo) []seg {
	var segs []seg
	if m.selectMode() {
		// The checkbox is always drawn, blank on the row nothing can select (main),
		// so the glyph after it never shifts depending on which rows are selected.
		check := seg{t: "   "}
		if r.sec != secMain {
			box, boxColor := "[ ]", cFaint
			if m.selected[r.name] {
				box, boxColor = "[x]", cGreen
			}
			check = seg{t: box, fg: boxColor}
		}
		segs = append(segs, check, seg{t: " "})
	}
	return append(segs, brGlyph(r, b), seg{t: " "})
}

// brPanelLine renders one branch row: the gutter, the name, then up to four
// fixed columns. The gutter's glyph is the ownership/worktree state — see
// brGlyph — so the axis the sections group on is readable straight down the
// left edge without reading a heading.
func (m Model) brPanelLine(r paneRow, b git.BranchInfo, w int, plan columnPlan) []seg {
	ownerColor := cFaint
	switch {
	case r.owner.unknown:
		ownerColor = cMag
	case r.owner.mine:
		ownerColor = cGreen
	}

	owner, ready := r.owner.who, r.ready
	if r.sec == secMain {
		// The default branch has no owner and is never a delete candidate; blanking
		// those two cells says so more plainly than filling them with a verdict that
		// does not apply.
		owner, ready = "", ""
	}
	layout := m.branchRowLayout(r, w, plan)
	keep, nameW := layout.keep, layout.nameW
	var right []seg
	addCol := func(s seg) { right = append(right, s, seg{t: " "}) }
	if keep["owner"] {
		addCol(rowCell(owner, brOwnerW, ownerColor))
	}
	if keep["state"] {
		addCol(rowCell(stateCell(r), brStateW, m.stateColor(r)))
	}
	if keep["hold"] {
		addCol(rowCell(strings.Join(r.holds, " · "), brHoldW, cYellow))
	}
	if keep["ready"] {
		addCol(rowCellR(ready, brReadyW, readyColor(r.ready)))
	}
	right = append(right, rowCellR(compactAge(b.LastCommit), brAgeW, cDim))

	nameColor := cBlue
	switch {
	case r.sec == secMain:
		nameColor = cBright
	case r.sec == secTheirs:
		// Live and not yours reads as cSlate, not gray — gray is reserved for a row
		// that is actually finished (below, only once it is also struck through).
		// Scoped to Reviewing specifically, not "any non-mine row": an unattributed
		// branch that classifySection routed to Continue Working because it holds
		// real uncommitted work is being treated as yours to finish, and coloring its
		// name the same as a colleague's live branch would contradict the section
		// it's sitting in.
		nameColor = cSlate
	}
	nameSeg := seg{t: truncR(r.name, nameW), fg: nameColor}
	namePad := 0
	if layout.purposeW > 0 {
		// The padding that aligns the purpose column is its own unlinked segment, so
		// the name's hyperlink covers the name and not the blank cells after it.
		namePad = nameW - vlen(nameSeg.t)
	}
	// Struck through means finished, and only a proof earns it — a row in Clean Up
	// with nothing held. A held row keeps its normal weight: it is deletable in the
	// same breath as it is holding work you have not saved, and dimming it is how
	// that second half stops being noticed.
	if r.sec == secCleanup && r.ready == "ready" {
		nameSeg.fg, nameSeg.strike = cDim, true
	}
	// The name is an OSC-8 hyperlink to the branch's page on GitHub, so a click
	// (cmd-click in most terminals) opens it. Only a branch origin actually carries
	// gets one: a branch that never left this machine has no page there, and a link
	// to it would hand out a 404. branchURL is empty when origin isn't GitHub.
	if b.HasRemote {
		nameSeg.link = branchURL(m.nwo, b.Name)
	}
	left := append(m.gutterSegs(r, b), nameSeg)
	if layout.purposeW > 0 {
		left = append(left, seg{t: padN(namePad + 1)}, rowCell(r.purpose.text, layout.purposeW, purposeColor(r.purpose.src)))
	}
	return composeLR(left, right, w)
}

// brSubLine is the row's second line: what the branch is for, tagged with which
// rung of the purpose ladder answered. Returns nil when there is nothing to say,
// so a row costs one line rather than two. Which sections draw this line at all
// is section.showsPurpose's call, not this function's — branchPaneLines gates
// the call to brSubLine on it, so Clean Up rows never reach here.
func (m Model) brSubLine(r paneRow, w int, plan columnPlan) []seg {
	if r.purpose.text == "" {
		return nil
	}
	segs := []seg{
		{t: "   "}, {t: "↳ ", fg: cFaint},
		rowCell(r.purpose.src, 7, purposeColor(r.purpose.src)),
	}
	used := 3 + 2 + 7
	if r.issue > 0 {
		label := "#" + strconv.Itoa(r.issue) + " "
		link := seg{t: label, fg: cCyan}
		if m.nwo != "" {
			link.link = issueURL(m.nwo, r.issue)
		}
		segs = append(segs, link)
		used += vlen(label)
	}
	// Capped to where the state column ends on the row above, not the full pane
	// width — the subline elaborates on that state word, and letting it run on
	// under the hold/ready/age columns instead reads as unrelated to it.
	maxText := mini(w-used, m.stateColumnEnd(r, w, plan)-used)
	segs = append(segs, rowCell(r.purpose.text, maxi(0, maxText), cDim))
	return segs
}

// purposeColor separates a statement of intent from a description of a change: a
// branch note is somebody saying what this is for, a commit subject is the last
// thing that happened, and they must not read as equally authoritative.
func purposeColor(src string) lipgloss.TerminalColor {
	switch src {
	case "note":
		return cGreen
	case "pr":
		return cCyan
	case "issue":
		return cMag
	}
	return cFaint
}

// brSectionLine is a section heading: its title, a rule, the measurement that
// files branches here, and the count. The rule is on screen rather than in the
// docs because a section whose membership you cannot account for is one you end up
// re-deriving by reading its contents.
//
// chips are the values planColumns hoisted off this section's own rows because
// every row agreed on them — a heading reading "Clean Up  ── merged · ready ── 6"
// says once what every row would otherwise have repeated. The chip run is
// dropped entirely, never squeezed, when it wouldn't leave room for at least a
// short dash fill: a heading that doesn't fit is worse than one that says
// nothing extra.
func brSectionLine(sec section, n, w int, chips []string) []seg {
	title := " " + sectionTitle[sec] + "  "
	count := strconv.Itoa(n)
	const minFill = 3
	var chip seg
	chipLen := 0
	if len(chips) > 0 {
		chip = seg{t: "── " + strings.Join(chips, " · ") + " ", fg: cFaint}
		chipLen = vlen(chip.t)
	}
	fill := w - vlen(title) - chipLen - vlen(count) - 2
	if chipLen > 0 && fill < minFill {
		chip, chipLen = seg{}, 0
		fill = w - vlen(title) - vlen(count) - 2
	}
	segs := []seg{{t: title, fg: cBright}}
	if chipLen > 0 {
		segs = append(segs, chip)
	}
	segs = append(segs,
		seg{t: strings.Repeat("─", maxi(0, fill)), fg: cFaint},
		seg{t: "  ", fg: cFaint},
		seg{t: count, fg: cGreen},
	)
	return segs
}

// branchOnOrigin reports whether origin currently carries a branch of this name,
// read from the loaded branch list (unfiltered — the header's branch is linkable
// whether or not the filter box is hiding its row). False before the local data
// lands, which only means the header's branch name isn't a link for that first frame.
func (m Model) branchOnOrigin(name string) bool {
	for _, b := range m.local.branches {
		if b.Name == name {
			return b.HasRemote
		}
	}
	return false
}

// cmPanelLine renders one commit row as three fixed columns — sha, subject, age
// — so all three sit at the same offset on every line. The subject is padded to
// its full budget rather than left to its own length: sizing the row from each
// commit's own age and subject makes every value's position depend on the one
// next to it, which is what turns the list back into a run of strings.
func (m Model) cmPanelLine(c git.Commit, w int) []seg {
	// " " + sha + "  " + subject + " " + age.
	msgW := w - 1 - cmShaW - 2 - 1 - cmAgeW
	if msgW < 4 {
		msgW = 4
	}
	// The sha is an OSC-8 hyperlink to the commit on GitHub, the same page the `o`
	// key opens on this row. commitURL is empty when origin isn't GitHub, which
	// leaves the sha as plain text.
	left := []seg{
		{t: " "}, {t: padEnd(truncR(c.SHA, cmShaW), cmShaW), fg: cCyan, link: commitURL(m.nwo, c.SHA)},
		{t: "  "}, {t: padEnd(truncR(c.Subject, msgW), msgW), fg: cFg},
	}
	right := padStart(seg{t: compactAge(c.When), fg: cDim}, cmAgeW)
	return composeLR(left, right, w)
}

// stPanelLine renders one stash row as a fixed ref column and a subject column,
// so a two-digit stash index can't shift that row's subject out of line with
// every other row's.
func (m Model) stPanelLine(s git.Stash, w int) []seg {
	// " " + ref + "  " + subject.
	msgW := w - 1 - stRefW - 2
	if msgW < 4 {
		msgW = 4
	}
	left := []seg{
		{t: " "}, {t: padEnd(truncR(s.Ref, stRefW), stRefW), fg: cMag},
		{t: "  "}, {t: padEnd(truncR(s.Subject, msgW), msgW), fg: cDim},
	}
	return composeLR(left, nil, w)
}

// renderLocalPanels renders the Repo tab in exactly height lines, alongside
// which pane/row each line hits: Branches across the full width, with a
// Commits/Stashes strip beneath it.
func (m Model) renderLocalPanels(width, height int) ([]string, []hitTarget) {
	if !m.haveLocal {
		lines := []string{"  " + mutedStyle.Render("loading…")}
		return lines, noneHits(len(lines))
	}
	if m.local.err != nil {
		lines := []string{"  " + badStyle.Render("error: "+m.local.err.Error())}
		return lines, noneHits(len(lines))
	}
	commits, stashes := m.localPanes()
	rows := m.branchRows()
	// cleanCount is exactly what ⇧X will take: pruneTargets plus every Clean Up row
	// reading `ready`. It has to be built the same way cleanAll builds its batch —
	// counting the wider Clean Up section (which also holds rows marked `held`) is
	// what let the footer promise more than a sweep actually delivered.
	cleanCount := len(m.pruneTargets())
	for _, r := range rows {
		if r.sec == secCleanup && r.ready == "ready" {
			cleanCount++
		}
	}

	// Branches owns the full width: every column of the row, and the purpose, fit
	// on one line. Commits and Stashes are read-only reference under it, one at a
	// time behind a toggle, in a strip a third of the body tall at most.
	stripH := historyStripH(height)
	branchH := height - stripH

	// The cursor indexes rows, not lines — a row is one or two lines and headings
	// are lines nothing can land on — so itemLine carries the mapping and every
	// caller keeps addressing branches by position.
	bInnerW := width - 2
	byName := map[string]git.BranchInfo{}
	for _, b := range m.local.branches {
		byName[b.Name] = b
	}
	counts := map[section]int{}
	for _, r := range rows {
		counts[r.sec]++
	}
	bLines, itemLine := m.branchPaneLines(rows, byName, counts, bInnerW)
	bCur := clampi(0, m.paneCursor[paneBranches], len(rows)-1)
	bActive := -1
	if len(rows) > 0 {
		bActive = itemLine[bCur]
	}
	badge := "⇅ next action   ● yours " + strconv.Itoa(mineCount(rows)) + "   " + strconv.Itoa(len(rows))
	if m.minesOnly {
		badge = "⇅ next action   ● yours only   " + strconv.Itoa(len(rows))
	}
	var footer []seg
	if cleanCount > 0 {
		footer = []seg{{t: strconv.Itoa(cleanCount), fg: cGreen}, {t: " clean up ", fg: cDim}, {t: "⇧X", fg: cCyan}}
	}
	branchBox := boxPanel{
		title: "Branches", badge: badge,
		width: width, height: branchH, focused: m.focus == focusBody && m.pane == paneBranches,
		lines: bLines, active: bActive, scroll: clampScroll(0, len(bLines), branchH-2, bActive),
		footer: footer,
	}
	out := branchBox.render()

	// lineToRow is the inverse of itemLine — a line back to the row it renders — so
	// the hit map can answer what a click on that line hits.
	lineToRow := make([]int, len(bLines))
	for i := range lineToRow {
		lineToRow[i] = -1
	}
	for row, li := range itemLine {
		lineToRow[li] = row
	}
	hits := make([]hitTarget, 0, height)
	for r := 0; r < branchH; r++ {
		h := hitTarget{kind: hitRow, pane: paneBranches, row: -1}
		if r != 0 && r != branchH-1 {
			if li, hint := branchBox.innerRow(r - 1); !hint && li >= 0 && li < len(lineToRow) {
				h.row = lineToRow[li]
			}
		}
		hits = append(hits, h)
	}

	if stripH > 0 {
		strip := m.historyStrip(width, stripH, commits, stashes)
		out = append(out, strip.render()...)
		for r := 0; r < stripH; r++ {
			h := hitTarget{kind: hitRow, pane: m.historyPane(), row: -1}
			// An empty list draws one placeholder line, which is not a row.
			if r != 0 && r != stripH-1 && m.paneCount(m.historyPane()) > 0 {
				if li, hint := strip.innerRow(r - 1); !hint && li >= 0 {
					h.row = li
				}
			}
			hits = append(hits, h)
		}
	}
	return out, hits
}

// historyPane is the pane the bottom strip is showing: Commits unless the toggle
// has switched it to Stashes.
func (m Model) historyPane() localPane {
	if m.historyStashes {
		return paneStashes
	}
	return paneCommits
}

// paneShown reports whether a pane is on screen. Branches always is; of Commits
// and Stashes only the one the strip toggle points at is, so focus never lands on
// the hidden one.
func (m Model) paneShown(p localPane) bool {
	switch p {
	case paneBranches:
		return true
	case paneCommits, paneStashes:
		return p == m.historyPane()
	}
	return false
}

// stripMinBody is the shortest body that fits the strip under a drawable
// Branches box: 3 rows each, since a 1-row box still draws both borders.
const stripMinBody = 6

// historyStripH is the strip's height, borders included, for a body of the given
// height: a third of it, at least 3 (a box needs two borders and a row) and at
// most historyStripMaxH. A body under stripMinBody rows draws Branches alone.
// paneShown does not know the body height, so the strip has to exist at every
// height a focusable pane can be on.
func historyStripH(body int) int {
	if body < stripMinBody {
		return 0
	}
	return clampi(3, body/3, historyStripMaxH)
}

// historyStrip is the box under Branches. Its title is the toggle itself: both
// names and counts, the one on screen in brackets, so which list this is and what
// `c` will switch to are readable without remembering the last keypress.
func (m Model) historyStrip(width, height int, commits []git.Commit, stashes []git.Stash) boxPanel {
	inner := width - 2
	pane := m.historyPane()
	var lines [][]seg
	var n int
	if pane == paneStashes {
		n = len(stashes)
		for _, s := range stashes {
			lines = append(lines, m.stPanelLine(s, inner))
		}
		if n == 0 {
			lines = [][]seg{{{t: " no stashes", fg: cFaint}}}
		}
	} else {
		n = len(commits)
		for _, c := range commits {
			lines = append(lines, m.cmPanelLine(c, inner))
		}
		if n == 0 {
			lines = [][]seg{{{t: " no commits", fg: cFaint}}}
		}
	}
	active := -1
	if n > 0 {
		active = clampi(0, m.paneCursor[pane], n-1)
	}
	label := func(name string, count int, on bool) string {
		if on {
			return "[" + name + " " + strconv.Itoa(count) + "]"
		}
		return name + " " + strconv.Itoa(count)
	}
	title := label("Commits", len(commits), pane == paneCommits) + "  " + label("Stashes", len(stashes), pane == paneStashes)
	return boxPanel{
		title: title, badge: "c switch", width: width, height: height,
		focused: m.focus == focusBody && m.pane == pane,
		lines:   lines, active: active, scroll: clampScroll(0, len(lines), height-2, active),
	}
}

// maxi is the larger of two ints, for width budgets that must never go negative.
func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// mini is the smaller of two ints.
func mini(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clampi(lo, v, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// nextVisiblePane steps the focused pane by dir, skipping any pane with no
// selectable rows. Focus is only worth spending a keypress on where a row key can
// act: a Stashes pane on a repo with no stashes collapses to a single rule, and
// paneCleanup draws nothing of its own at all — every deletable branch is marked
// in place in the Branches list, x on a branch row deletes it, and ⇧X sweeps every
// ready row from any pane. Both fall out of the same paneCount == 0 test.
//
// The walk is bounded by numPanes, so a state where every other pane is empty
// returns the pane already focused rather than spinning. Branches always lists at
// least the default branch, so that case cannot leave the focus nowhere.
func (m Model) nextVisiblePane(p localPane, dir int) localPane {
	n := p
	for i := 0; i < int(numPanes); i++ {
		n = localPane((int(n) + dir + int(numPanes)) % int(numPanes))
		if m.paneCount(n) > 0 && m.paneShown(n) {
			return n
		}
	}
	return p
}
