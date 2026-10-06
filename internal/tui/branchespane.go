package tui

import (
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// The Branches pane, assembled: one paneRow per branch, grouped by what you do
// next. See section.go for the ladder and ownership.go for what "yours" means.

// openPRFor returns the open PR whose head is this branch, or nil. Three ways in,
// first hit wins:
//
//   - the PR's head has this branch's name;
//   - the PR's head is this branch's upstream — pushed as `HEAD:other-name` with
//     tracking, so GitHub knows the work only by the remote name;
//   - the PR's head commit is this branch's tip — pushed under another name with
//     no tracking at all, or tracking origin/main because it was cut from it.
//
// The tip match skips a PR whose head name is another LOCAL branch: that branch is
// the PR's, and a second branch sitting on the same commit is not its own claim on
// it. The upstream match does not: `gh` and the status line resolve a PR through
// the upstream, so a branch tracking a PR's head shows that PR here too.
//
// With none of those, a branch whose tip is inside an open PR's head — the base
// of a stack, a mid-tier feature with the branch it was built for on top — takes
// the nearest such PR (fewest commits above its tip). Only then: a base that has
// a PR of its own was matched above and keeps it, so two branches meant to be
// reviewed separately always show their separate PRs.
func (m Model) openPRFor(branch string) *gh.OpenPR {
	for i := range m.remote.PRs {
		if m.remote.PRs[i].HeadBranch == branch {
			return &m.remote.PRs[i]
		}
	}
	local := map[string]bool{}
	for _, b := range m.local.branches {
		local[b.Name] = true
	}
	upstream := m.local.signals[branch].upstream
	tip := m.local.tips[branch]
	var byTip *gh.OpenPR
	for i := range m.remote.PRs {
		pr := &m.remote.PRs[i]
		if upstream != "" && upstream != m.def && pr.HeadBranch == upstream {
			return pr
		}
		if local[pr.HeadBranch] {
			continue
		}
		if byTip == nil && tip != "" && pr.HeadOID == tip {
			byTip = pr
		}
	}
	if byTip != nil {
		return byTip
	}
	var nearest *gh.OpenPR
	best := 0
	for i := range m.remote.PRs {
		pr := &m.remote.PRs[i]
		above, ok := m.local.containedIn[branch][pr.HeadBranch]
		if !ok || (nearest != nil && above >= best) {
			continue
		}
		nearest, best = pr, above
	}
	return nearest
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
	ahead := m.local.unpushed[b.Name]
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
	r.ahead, r.behind, r.changed = ahead, m.local.signals[b.Name].behind, st.changed
	switch {
	case protected:
		// The default branch is behind nothing but its own origin copy: ↑/↓ there are
		// what a push or a pull would move.
		r.ahead, r.behind = b.Ahead, b.Behind
	case r.sec == secCleanup:
		// A landed branch falls further behind every day and nobody will ever rebase
		// it; the count would be the only non-empty cell on a row that is finished.
		r.behind = 0
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
// This is the single production caller of planColumns: one plan over every row,
// threaded into each row and subline, so every section draws its columns at the
// same cells.
func (m Model) branchPaneLines(rows []paneRow, byName map[string]git.BranchInfo, counts map[section]int, w int) (lines [][]seg, itemLine []int) {
	plan := planColumns(rows)
	itemLine = make([]int, len(rows))
	// The PR marks' legend rides the first heading over a row that carries one.
	var prs []*gh.OpenPR
	legendSec := section(-1)
	for _, r := range rows {
		if r.pr != nil {
			if legendSec == section(-1) && r.sec != secMain {
				legendSec = r.sec
			}
			prs = append(prs, r.pr)
		}
	}
	lastSec := section(-1)
	for i, r := range rows {
		if r.sec != lastSec {
			// A section with nothing in it is not drawn at all — an empty heading is a
			// row of furniture that says only that you have nothing to do there, and the
			// pane already says that by not mentioning it.
			if lastSec != section(-1) {
				lines = append(lines, []seg{{t: ""}})
			}
			if r.sec != secMain {
				var legend []seg
				if r.sec == legendSec {
					legend = markLegend(prs, w)
				}
				lines = append(lines, brSectionLine(r.sec, counts[r.sec], w, legend))
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
