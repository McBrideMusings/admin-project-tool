package tui

import (
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// The Branches pane, assembled: one paneRow per branch, grouped by what you do
// next. See section.go for the ladder and ownership.go for what "yours" means.

// openPRFor returns the open PR whose head is this branch, or nil.
func (m Model) openPRFor(branch string) *gh.OpenPR {
	for i := range m.remote.PRs {
		if m.remote.PRs[i].HeadBranch == branch {
			return &m.remote.PRs[i]
		}
	}
	return nil
}

// rowFor resolves one branch into everything the pane draws. Every field comes
// from a measurement made elsewhere — this only assembles them, so the row and
// the delete path cannot disagree about a branch.
func (m Model) rowFor(b git.BranchInfo) paneRow {
	protected := b.Name == m.def || b.Name == m.mainTreeBranch()
	pr := m.openPRFor(b.Name)
	landed := m.landedProof(b.Name, pr != nil)
	own := m.ownership(b.Name, pr)

	// A protected branch is nobody's leftovers and nobody's next action; it is the
	// thing everything else is measured against, so it skips the ladder entirely.
	if protected {
		landed = ""
	}

	wtPath := b.WorktreePath
	if wtPath == "" {
		wtPath = m.worktreeByBranch()[b.Name]
	}
	st := m.local.wtState[wtPath]
	broken := blockers(pr, m.worktreeOp(wtPath), st.conflicts)
	ahead := b.Ahead
	if !b.HasRemote {
		// With no counterpart on origin, "ahead" has to be measured against the default
		// branch — there is nothing else for the commits to be ahead OF.
		ahead = m.local.aheadDefault[b.Name]
	}
	holds := holdsOf(st.changed, m.local.branchUnsafe[b.Name], ahead, landed)
	// Being worked on right now holds a branch ONLY where the clock is carrying real
	// information — an `empty` branch, or one with no proof at all, where "cut ten
	// minutes ago" and "abandoned in March" are the same shape in the commit graph.
	//
	// A finished branch is not held by it. Work that merged an hour ago is exactly as
	// merged as work that merged last week, and holding it because its worktree was
	// touched recently is what left fifteen provably-landed branches sitting behind a
	// warning with nothing to warn about.
	if !protected && !finishedProof(landed) && m.isActive(b.Name, wtPath) {
		holds = append(holds, "active")
	}

	r := paneRow{
		name:   b.Name,
		owner:  own,
		holds:  holds,
		broken: broken,
		ready:  readyWord(landed, holds),
		issue:  issueOfBranch(b.Name),
		pr:     pr,
	}
	if pr != nil {
		r.prNum = pr.Number
	}
	r.sec = classifySection(protected, own, landed, pr, broken, holds)
	r.state = stateWord(r, landed)
	if r.state == "" {
		// Nothing about the branch's review or landing says anything, so fall back to
		// its relationship with origin — the only thing left that distinguishes work
		// sitting on this machine from work that has been shared.
		switch {
		case ahead > 0 && !b.HasRemote:
			r.state = "local only"
		case ahead > 0:
			r.state = "unpushed"
		case protected:
			r.state = ""
		default:
			// "no commits" is a null state, blanked at render by stateCell in local.go
			// but kept semantically intact here for stateColor/section reasoning.
			r.state = "no commits"
		}
	}
	prTitle := ""
	if pr != nil {
		prTitle = pr.Title
	}
	r.purpose = m.purposeOf(b.Name, prTitle, r.issue)
	return r
}

// branchRows is the ordered pane content: the default branch first, then each
// section in sectionOrder, each sorted by when it was last worked on.
//
// Ordering by section rather than by one global sort is the point of the redesign
// — a flat recency list answers "what changed" and the pane has to answer "what do
// I do" — but recency still decides the order WITHIN a section, because among two
// branches that both want the same action, the one you touched an hour ago is the
// one you meant.
func (m Model) branchRows() []paneRow {
	flt := strings.ToLower(m.filter)
	var rows []paneRow
	for _, b := range m.local.branches {
		if flt != "" && !strings.Contains(strings.ToLower(b.Name), flt) {
			continue
		}
		// `g` narrows the pane to your work. Clean Up stays: a sweep is the one action
		// that is about the repo rather than about you, and hiding a colleague's landed
		// branch from it would make ⇧X quietly do less than the pane says it will.
		r := m.rowFor(b)
		if m.minesOnly && !r.owner.mine && r.sec != secMain && r.sec != secCleanup {
			continue
		}
		rows = append(rows, r)
	}
	rank := map[section]int{secMain: -1}
	for i, s := range sectionOrder {
		rank[s] = i
	}
	age := map[string]int{}
	for _, b := range m.local.branches {
		age[b.Name] = ageMinutes(b.LastCommit)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rank[rows[i].sec] != rank[rows[j].sec] {
			return rank[rows[i].sec] < rank[rows[j].sec]
		}
		return age[rows[i].name] < age[rows[j].name]
	})
	return rows
}

// mineCount is how many of the rendered rows are your work, for the pane badge.
func mineCount(rows []paneRow) int {
	n := 0
	for _, r := range rows {
		if r.owner.mine && r.sec != secMain {
			n++
		}
	}
	return n
}

// branchInfo is one branch's git record by name. Returns a zero BranchInfo when
// the name is unknown, which only happens if the branch vanished between the pane
// building its rows and a key being handled — the row's own fields still describe
// what the user was looking at.
func (m Model) branchInfo(name string) git.BranchInfo {
	for _, b := range m.local.branches {
		if b.Name == name {
			return b
		}
	}
	return git.BranchInfo{Name: name}
}

// branchInfos maps the pane's rows back to git records, preserving row order so a
// cursor index means the same branch in both.
func (m Model) branchInfos(rows []paneRow) []git.BranchInfo {
	out := make([]git.BranchInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, m.branchInfo(r.name))
	}
	return out
}

// branchPaneLines assembles the Branches panel's actual screen lines: a blank
// line and heading whenever the section changes, then each row's line and,
// for sections whose showsPurpose is true, its purpose subline. itemLine maps
// each row index back to the line it landed on, since a row costs one line or
// two and headings are lines nothing can be the cursor on.
//
// This is the single production caller of planColumns: rows are grouped by
// section once, a plan computed per section, and that plan threaded into the
// heading, the row and the subline for every row in that section — the only
// way all three can agree on which columns this section hoisted or dropped.
func (m Model) branchPaneLines(rows []paneRow, byName map[string]git.BranchInfo, counts map[section]int, w int) (lines [][]seg, itemLine []int) {
	secRows := map[section][]paneRow{}
	for _, r := range rows {
		secRows[r.sec] = append(secRows[r.sec], r)
	}
	plans := make(map[section]columnPlan, len(secRows))
	for sec, rs := range secRows {
		plans[sec] = planColumns(sec, rs)
	}

	itemLine = make([]int, len(rows))
	lastSec := section(-1)
	for i, r := range rows {
		plan := plans[r.sec]
		if r.sec != lastSec {
			// A section with nothing in it is not drawn at all — an empty heading is a
			// row of furniture that says only that you have nothing to do there, and the
			// pane already says that by not mentioning it.
			if lastSec != section(-1) {
				lines = append(lines, []seg{{t: ""}})
			}
			if r.sec != secMain {
				lines = append(lines, brSectionLine(r.sec, counts[r.sec], w, plan.chips))
			}
			lastSec = r.sec
		}
		itemLine[i] = len(lines)
		lines = append(lines, m.brPanelLine(r, byName[r.name], w, plan))
		// A section wide enough to draw the purpose as a column on the row itself
		// (branchRowLayout.purposeW) has no second line to draw.
		if r.sec.showsPurpose() && m.branchRowLayout(r, w, plan).purposeW == 0 {
			if sub := m.brSubLine(r, w, plan); sub != nil {
				lines = append(lines, sub)
			}
		}
	}
	return lines, itemLine
}
