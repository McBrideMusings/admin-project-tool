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
	return branchRowLayout{keep: keep, nameW: flexWidth(w, g+1, rightW, 8)}
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

// renderLocalPanels renders the Repo tab's dual-panel layout in exactly height
// lines, alongside which pane/row each line hits. Branches sits left of
// Commits/Stashes at the same y, so every body line's hitTarget is split at
// the column the two columns are joined on (see localSplitHit).
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

	// The rail is a fixed width, not a share of the terminal. Recent commits and
	// Stashes are read-only reference — every keybinding the footer advertises acts
	// on Branches — so the rail takes what a short sha, a readable subject and an
	// age need and nothing more. Splitting by percentage gave the rail half the
	// screen at 200 cells, where the extra cells bought a longer commit subject
	// while branch names on the left truncated.
	rightW := min(railW, (width-1)*railMaxPct/100)
	if width-1-rightW < brMinW {
		rightW = width - 1 - brMinW
	}
	if rightW < 10 {
		rightW = 10
	}
	leftW := width - 1 - rightW
	if leftW < 1 {
		leftW = 1
	}

	// Branches panel (left, full height). The cursor indexes rows, not lines — a row
	// is one or two lines and headings are lines nothing can land on — so itemLine
	// carries the mapping and every caller keeps addressing branches by position.
	bInnerW := leftW - 2
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
		width: leftW, height: height, focused: m.pane == paneBranches,
		lines: bLines, active: bActive, scroll: clampScroll(0, len(bLines), height-2, bActive),
		footer: footer,
	}

	// The rail is a 3:1 split, commits over stashes, and the ratio does not move
	// with how many rows either pane holds — a dashboard is something you glance at
	// the same place every time, and a proportion that tracks content is one you
	// have to re-find. The single exception is a stash count of zero, which is not
	// "fewer rows" but no pane to size: see below. Cleanup no longer has its own box
	// on this column — every deletable branch is marked in the Branches list already
	// and ⇧X is named on its bottom border — so this split is the whole rail rather
	// than three boxes competing.
	avail := height
	stashesH := avail / 4
	switch {
	case len(stashes) == 0:
		// A stash pane with no stashes is eight blank lines behind two borders. The
		// borders are there to enclose rows; with no rows the box is nothing but its
		// own chrome, so it collapses to one named rule and the rail's height goes to
		// the pane that has something to show. The line stays rather than the pane
		// disappearing, so an empty stash list still reads as a fact.
		stashesH = 1
	case stashesH < 3:
		// A box needs ≥3 rows (two borders + ≥1 content) to exist at all. Below that,
		// fold the space into commits rather than draw a box that can't hold anything.
		stashesH = 0
	}
	commitsH := avail - stashesH
	// Commits is the pane that always has content, so it is the one that must stay
	// drawable: on a terminal too short for both, the rail is entirely commits.
	if commitsH < 3 {
		commitsH, stashesH = avail, 0
	}
	rInnerW := rightW - 2
	cLines := make([][]seg, len(commits))
	for i, c := range commits {
		cLines[i] = m.cmPanelLine(c, rInnerW)
	}
	cCur := clampi(0, m.paneCursor[paneCommits], len(commits)-1)
	cActive := -1
	if len(commits) > 0 {
		cActive = cCur
	}
	commitsBox := boxPanel{
		title: "Recent commits", badge: strconv.Itoa(len(commits)), width: rightW, height: commitsH,
		focused: m.pane == paneCommits, lines: cLines, active: cActive,
		scroll: clampScroll(0, len(cLines), commitsH-2, cActive),
	}
	sLines := make([][]seg, len(stashes))
	for i, s := range stashes {
		sLines[i] = m.stPanelLine(s, rInnerW)
	}
	sCur := clampi(0, m.paneCursor[paneStashes], len(stashes)-1)
	sActive := -1
	if len(stashes) > 0 {
		sActive = sCur
	}
	stashesBox := boxPanel{
		title: "Stashes", badge: strconv.Itoa(len(stashes)), width: rightW, height: stashesH,
		focused: m.pane == paneStashes, lines: sLines, active: sActive,
		scroll: clampScroll(0, len(sLines), stashesH-2, sActive),
	}

	left := branchBox.render()
	right := commitsBox.render()
	switch {
	case stashesH == 1:
		right = append(right, stashesBox.renderRule())
	case stashesH >= 3:
		right = append(right, stashesBox.render()...)
	}

	// itemLine maps a branch row to the line it landed on; the hit map needs the
	// inverse — a line back to the row it renders — so it can answer what a
	// click on that line hits.
	lineToRow := make([]int, len(bLines))
	for i := range lineToRow {
		lineToRow[i] = -1
	}
	for row, li := range itemLine {
		lineToRow[li] = row
	}
	hits := make([]hitTarget, height)
	for r := 0; r < height; r++ {
		hits[r] = localBodyHit(r, leftW, height, commitsH, stashesH, branchBox, commitsBox, stashesBox, lineToRow)
	}

	return zipColumns(left, right, height), hits
}

// localBodyHit resolves one row r (0 ≤ r < bodyH) of renderLocalPanels' merged
// output into what it hits: Branches to the left of leftW, Commits/Stashes to
// the right. It reads the same boxPanel.innerRow each box's own render() drew
// from, so the hit map can never drift from what was actually drawn — a
// changed layout only ever changes what render() computes, and this reads
// that computation rather than repeating it.
func localBodyHit(r, leftW, bodyH, commitsH, stashesH int, branchBox, commitsBox, stashesBox boxPanel, lineToRow []int) hitTarget {
	h := hitTarget{
		kind: hitRow, split: true, splitX: leftW,
		pane: paneBranches, row: -1,
		pane2: paneCommits, row2: -1,
	}

	if r != 0 && r != bodyH-1 {
		if li, hint := branchBox.innerRow(r - 1); !hint && li >= 0 && li < len(lineToRow) {
			h.row = lineToRow[li]
		}
	}

	switch {
	case r < commitsH:
		h.pane2 = paneCommits
		if r != 0 && r != commitsH-1 {
			if li, hint := commitsBox.innerRow(r - 1); !hint && li >= 0 {
				h.row2 = li
			}
		}
	case stashesH >= 3:
		idx := r - commitsH
		h.pane2 = paneStashes
		if idx != 0 && idx != stashesH-1 {
			if li, hint := stashesBox.innerRow(idx - 1); !hint && li >= 0 {
				h.row2 = li
			}
		}
	default:
		// stashesH == 1 is the collapsed "no stashes" rule (no rows exist to
		// select there) and stashesH == 0 can't reach this branch, since that
		// only happens when commitsH == bodyH and r < commitsH already matched
		// above. Either way there is nothing selectable on the right here.
		h.pane2 = paneStashes
	}
	return h
}

// clampi clamps v to [lo, hi]; if hi < lo it returns lo.
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
		if m.paneCount(n) > 0 {
			return n
		}
	}
	return p
}
