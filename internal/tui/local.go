package tui

import (
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
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

// stateColor is the state word's color: red for a worktree stopped on
// something, dim for a landing proof.
func (m Model) stateColor(r paneRow) lipgloss.TerminalColor {
	if len(r.broken) > 0 && r.sec != secCleanup {
		return cRed
	}
	return cDim
}

// prText is the row's PR cell as plain text — "#287 ○" (the number and prMark),
// then any state word ("#287 ≠ rebasing"), or a landing proof alone ("merged"),
// or "" for a branch with neither. planColumns measures rows by this exact
// string, so prSegs must draw the same characters.
func prText(r paneRow) string {
	var parts []string
	if r.prNum > 0 {
		parts = append(parts, "#"+strconv.Itoa(r.prNum))
	}
	if r.pr != nil {
		parts = append(parts, prMark(r.pr).t)
	}
	if r.state != "" && r.state != "empty" {
		parts = append(parts, r.state)
	}
	return strings.Join(parts, " ")
}

// colWidths is the PR and git column widths a section's plan settled on, or the
// caps for an unplanned row.
func colWidths(plan columnPlan) (prW, gitW int) {
	prW, gitW = plan.prW, plan.gitW
	if prW == 0 {
		prW = brPRW
	}
	if gitW == 0 {
		gitW = brGitW
	}
	return prW, gitW
}

// prSegs draws prText in exactly n cells: the number cyan (a link to the
// PR), the mark in its own color, the word in stateColor.
func (m Model) prSegs(r paneRow, n int) []seg {
	var segs []seg
	add := func(sg seg) {
		if len(segs) > 0 {
			segs = append(segs, seg{t: " "})
		}
		segs = append(segs, sg)
	}
	if r.prNum > 0 {
		add(seg{t: "#" + strconv.Itoa(r.prNum), fg: cCyan, link: gh.PRURL(m.nwo, r.prNum)})
	}
	if r.pr != nil {
		add(prMark(r.pr))
	}
	if r.state != "" && r.state != "empty" {
		add(seg{t: r.state, fg: m.stateColor(r)})
	}
	return fitCell(segs, n)
}

// gitText is the row's git cell in `git status` notation — "↑6 ↓47 ~13": ↑ commits
// not on origin, ↓ commits behind the default branch, ~ files changed in the
// worktree. A zero count is left out, so a branch fully pushed, current and clean
// reads blank. See paneRow.ahead.
func gitText(r paneRow) string {
	return strings.Join(gitParts(r), " ")
}

func gitParts(r paneRow) []string {
	var parts []string
	if r.ahead > 0 {
		parts = append(parts, "↑"+strconv.Itoa(r.ahead))
	}
	if r.behind > 0 {
		parts = append(parts, "↓"+strconv.Itoa(r.behind))
	}
	if r.changed > 0 {
		parts = append(parts, "~"+strconv.Itoa(r.changed))
	}
	return parts
}

// gitSegs draws gitText in exactly n cells, in the colors styles.go assigns
// these marks everywhere: ↑ green, ↓ red, ~ yellow.
func gitSegs(r paneRow, n int) []seg {
	var segs []seg
	for i, p := range gitParts(r) {
		if i > 0 {
			segs = append(segs, seg{t: " "})
		}
		fg := cGreen
		switch {
		case strings.HasPrefix(p, "↓"):
			fg = cRed
		case strings.HasPrefix(p, "~"):
			fg = cYellow
		}
		segs = append(segs, seg{t: p, fg: fg})
	}
	return fitCell(segs, n)
}

// fitCell truncates a run of segments to n cells and pads it to exactly n — rowCell
// for a cell drawn in more than one color.
func fitCell(segs []seg, n int) []seg {
	segs = fitSegs(segs, n)
	if w := segsWidth(segs); w < n {
		segs = append(segs, seg{t: padN(n - w)})
	}
	return segs
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
// loses whole columns instead of squeezing every value into an ellipsis. Their
// left-to-right ORDER never changes, whichever survive — a column that moves when
// the terminal is resized is no longer a column.
//
// plan.drop skips a column before the fit test ever runs: a column this row's
// SECTION hoisted onto its own rule (or dropped as constantly empty) consumes
// no width budget at all, and the surplus flows to the name. That skip must
// `continue`, never `break` — a dropped column must never stop the scan from
// reaching a later one that would otherwise fit.
//
// The name outranks the purpose. The purpose is a column on the row only when
// the section's longest name fits whole AND brPurposeMinW cells are left after
// it; otherwise the purpose moves to the row's second line and the name takes
// the whole flexible width.
func (m Model) branchRowLayout(r paneRow, w int, plan columnPlan) branchRowLayout {
	keep := map[string]bool{}
	g := gutterWidth(m.selectMode())
	// gutter(g) on the left; 1 for the separator composeLR always keeps between
	// the name and the columns; 8 for the least a name can say.
	used := g + 1 + 8 + brAgeW + 1
	rightW := brAgeW
	prW, gitW := colWidths(plan)
	for _, col := range []struct {
		name string
		w    int
	}{
		{"pr", prW}, {"git", gitW}, {"owner", brOwnerW},
	} {
		if plan.drop[col.name] {
			continue
		}
		// +1 for the gap that follows every column. Without it a value that exactly
		// fills its width butts against the next one and the two read as one word.
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
	flexW := flexWidth(w, g+1, rightW, 8)
	name := plan.nameW
	if name == 0 {
		name = vlen(r.name)
	}
	if r.sec.showsPurpose() && flexW-name-1 >= brPurposeMinW {
		return branchRowLayout{keep: keep, nameW: name, purposeW: flexW - name - 1}
	}
	return branchRowLayout{keep: keep, nameW: flexW}
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

// brPanelLine renders one branch row: the gutter, the name, the purpose when it
// fits inline, then owner (Reviewing only), the PR cell, the git cell and age.
// The gutter's glyph is the ownership/worktree state — see brGlyph — so the axis
// the sections group on is readable straight down the left edge.
func (m Model) brPanelLine(r paneRow, b git.BranchInfo, w int, plan columnPlan) []seg {
	layout := m.branchRowLayout(r, w, plan)
	keep, nameW := layout.keep, layout.nameW
	prW, gitW := colWidths(plan)
	var right []seg
	addCol := func(s ...seg) { right = append(append(right, s...), seg{t: " "}) }
	if keep["owner"] {
		addCol(rowCell(ownerCell(r), brOwnerW, cSlate))
	}
	if keep["pr"] {
		addCol(m.prSegs(r, prW)...)
	}
	if keep["git"] {
		addCol(gitSegs(r, gitW)...)
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
	// Capped to where the PR cell ends on the row above, not the full pane width —
	// the subline says what that branch and its PR are for, and letting it run on
	// under the git and age cells reads as part of them.
	maxText := mini(w-used, m.prColumnEnd(r, w, plan)-used)
	segs = append(segs, rowCell(r.purpose.text, maxi(0, maxText), cDim))
	return segs
}

// prColumnEnd is the cell offset, from the row's left edge, where the PR cell's
// right edge falls — or where the name column ends when the PR cell was dropped
// for width or hoisted. brSubLine caps the purpose subline to it.
func (m Model) prColumnEnd(r paneRow, w int, plan columnPlan) int {
	layout := m.branchRowLayout(r, w, plan)
	pos := gutterWidth(m.selectMode()) + layout.nameW
	if layout.purposeW > 0 {
		pos += 1 + layout.purposeW
	}
	pos++ // the separator composeLR always keeps after the name
	if layout.keep["owner"] {
		pos += brOwnerW + 1
	}
	if layout.keep["pr"] {
		prW, _ := colWidths(plan)
		pos += prW
	}
	return pos
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

// brSectionLine is a section heading: its title, a rule, and the count. legend,
// when given, sits on the rule after the title.
func brSectionLine(sec section, n, w int, legend []seg) []seg {
	title := " " + sectionTitle[sec] + "  "
	count := strconv.Itoa(n)
	out := []seg{{t: title, fg: cBright}}
	fill := w - vlen(title) - vlen(count) - 2
	if lw := segsWidth(legend); legend != nil && fill-lw-4 >= 4 {
		out = append(out, seg{t: "── ", fg: cFaint})
		out = append(out, legend...)
		out = append(out, seg{t: " ", fg: cFaint})
		fill -= lw + 4
	}
	return append(out,
		seg{t: strings.Repeat("─", maxi(0, fill)), fg: cFaint},
		seg{t: "  ", fg: cFaint},
		seg{t: count, fg: cGreen},
	)
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
