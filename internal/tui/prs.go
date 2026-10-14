package tui

import (
	"strconv"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// prRows builds the PRs view: four exclusive buckets — "Mine", "Needs me",
// "Waiting on others", then "Everyone else" — every open PR shown once, with its
// status laid out in fixed columns (owner / flags / verdict) that line up down
// the whole table.
//
// "Needs me" and "Waiting on others" split what used to be one reviewer section
// by whose move it is: a PR the viewer has already had their say on drops out of
// "Needs me" the moment they say it, and only climbs back when a new commit or a
// direct reply puts the ball in their court again.
func (t *prsTab) prRows(sh *shell) []rowItem {
	cols := sh.cols()
	prs := t.sync.prs()
	pc := t.prColumns(cols)
	wtByBranch := worktreesByBranch(t.local.worktrees(), sh.repoRoot)
	var rows []rowItem

	section := func(title string, bucket gh.PRBucket, empty string, first bool) {
		var inBucket []gh.OpenPR
		for _, pr := range prs {
			if pr.Bucket == bucket {
				inBucket = append(inBucket, pr)
			}
		}
		if !first {
			rows = append(rows, blankRow())
		}
		hdr := countSeg(len(inBucket))
		if first {
			all := make([]*gh.OpenPR, len(prs))
			for i := range prs {
				all[i] = &prs[i]
			}
			gaps := gapLegend(prs, cols)
			// What the heading needs beside the legends: its label, the count and
			// the gaps around both — the same reserve gapLegend keeps.
			if marks := markLegend(all, cols-24-segsWidth(gaps)-3); marks != nil {
				gaps = append(append(marks, seg{t: "   "}), gaps...)
			}
			hdr = append(gaps, hdr...)
		}
		rows = append(rows, headerRow(title, hdr...), ruleRow())
		if len(inBucket) == 0 {
			rows = append(rows, rowItem{kind: rowText, label: emptyOrLoading(t.sync.loaded(), empty)})
		}
		for _, pr := range inBucket {
			left, right := t.openPRRow(pr, pc, cols)
			rows = append(rows, rowItem{
				kind: rowPR, leftSegs: left, rightSegs: right,
				prNum: pr.Number, pr: &pr, branch: pr.HeadBranch,
				wtPath: wtByBranch[pr.HeadBranch], url: gh.PRURL(t.nwo, pr.Number),
			})
		}
	}

	section("Mine", gh.BucketMine, "no open PRs", true)
	section("Needs me", gh.BucketAssigned, "nothing needs you", false)
	section("Waiting on others", gh.BucketReviewed, "nothing waiting on others", false)
	section("Everyone else", gh.BucketOther, "no other open PRs", false)
	return rows
}

// prCols is the shared column geometry for the PRs table, computed once across
// every open PR so the owner and verdict columns line up down the whole list.
// Every width is measured from the data, so the flag column costs nothing when no
// open PR carries a flag, and only as much as the worst flag present otherwise.
type prCols struct {
	numW     int // width of the "#<number>" column (widest number in the list)
	ownerW   int // 0 when every open PR is the viewer's own (no owner column)
	verdictW int
	// flagW is the triage column — the ⚑ gaps a PR is missing. 0 when no open PR
	// is missing anything.
	flagW   int
	branchW int // width of the branch-name column (measured, capped)
}

// maxBranchW caps the branch column so a long branch name can't crush the title.
const maxBranchW = 40

// minBranchW is the narrowest branch column worth keeping. A terminal with less
// room than this to spare drops the branch column outright rather than showing
// four characters of a branch name — and dropping it is what keeps the row
// inside the terminal instead of spilling past the right edge and wrapping.
const minBranchW = 12

// maxTitleFloor is the most width titleFloor will hold back for the title. Past
// it the title is comfortably readable and further cells are better spent on the
// branch column than on padding.
const maxTitleFloor = 60

// titleFloor is the title width the branch column is not allowed to eat into.
//
// The branch column takes every cell above this floor, so the floor is the whole
// negotiation. A flat 20 was calibrated when the status block was 100 cells wide
// and there was no surplus to hand out; the moment the flag columns merged and
// freed 51 cells, the branch column took 39 of them and put the title back where
// it started. Half the terminal, capped, is what stops that: the branch name on
// this tab is a slug of the title beside it, so when only one of the two fits,
// the one carrying information the other doesn't is the one that survives.
func titleFloor(cols int) int {
	if half := cols / 2; half < maxTitleFloor {
		return half
	}
	return maxTitleFloor
}

// rightWidth is the total width of the status block prStatusCells builds: every
// non-zero column plus a two-cell gap between neighbours. Both must agree, so
// this mirrors that function's cell() rule exactly.
func (pc prCols) rightWidth() int {
	w, n := 0, 0
	for _, cw := range []int{pc.ownerW, pc.flagW, pc.verdictW} {
		if cw > 0 {
			w += cw
			n++
		}
	}
	if n > 1 {
		w += (n - 1) * 2
	}
	return w
}

// prColumns measures every column from the data, so each is as tight as the
// widest value that will land in it and a flag nobody has takes no width at all.
// No status column is ever dropped for width, and the PR's state needs none: its
// mark rides beside the number.
//
// The branch column is the one that gives way under width pressure — it shrinks
// to fit whatever the number, title floor, and status block leave over, and
// disappears below minBranchW. cols is the terminal width the rows render into.
func (t *prsTab) prColumns(cols int) prCols {
	pc := prCols{}
	for _, pr := range t.sync.prs() {
		if pr.Bucket != gh.BucketMine {
			if w := vlen("@" + pr.Author); w > pc.ownerW {
				pc.ownerW = w
			}
		}
		if w := vlen(flagSeg(pr).t); w > pc.flagW {
			pc.flagW = w
		}
		if w := vlen(verdictSeg(pr).t); w > pc.verdictW {
			pc.verdictW = w
		}
		if w := vlen("#" + strconv.Itoa(pr.Number)); w > pc.numW {
			pc.numW = w
		}
		if w := vlen(pr.HeadBranch); w > pc.branchW {
			pc.branchW = w
		}
	}
	if pc.branchW > maxBranchW {
		pc.branchW = maxBranchW
	}
	// The branch column costs its own width plus two gaps and the marker (see
	// openPRRow's tail). The marker is measured rather than assumed to be two
	// cells, because REPO_BRANCH_GLYPH can be any width or empty, and a budget
	// that disagreed with what the row draws is exactly how the title gets
	// crushed. numberedRowLeft budgets numW+6 for the glyph/number cluster on
	// the left. Whatever the terminal has left after those, the status block,
	// and the title floor is all the branch column may take.
	tail := 4 + vlen(branchGlyph())
	if spare := cols - (pc.numW + 6) - pc.rightWidth() - titleFloor(cols) - tail; spare < pc.branchW {
		pc.branchW = spare
	}
	if pc.branchW < minBranchW {
		pc.branchW = 0
	}
	return pc
}

// openPRRow builds left + right segments for one open PR. The PR's mark rides
// the left with the number and title; the right is the fixed-width status grid
// (see prStatusCells). pc is the shared column geometry from prColumns.
func (t *prsTab) openPRRow(pr gh.OpenPR, pc prCols, cols int) (left, right []seg) {
	right = prStatusCells(pr, pc)
	// The number is padded to the widest one in the list, so the title — and
	// every column after it — starts at the same cell whether the row carries a
	// two-digit PR number or a five-digit one.
	numS := "#" + strconv.Itoa(pr.Number)
	numSeg := seg{t: padEnd(numS, pc.numW), fg: cCyan, link: gh.PRURL(t.nwo, pr.Number)}
	var tail []seg
	if pc.branchW > 0 {
		// Fixed-width branch column: gap + glyph + branch padded to branchW + gap,
		// so every branch name starts and ends at the same screen column. A PR with
		// no head branch still reserves the column — dropping the cell would slide
		// that row's remaining fields left, out of line with every other row.
		b := truncR(pr.HeadBranch, pc.branchW)
		glyph := branchGlyph()
		if b == "" {
			glyph = padN(vlen(branchGlyph()))
		}
		tail = []seg{
			{t: "  "},
			{t: glyph + b, fg: cFaint},
			{t: padN(pc.branchW - vlen(b))},
			{t: "  "},
		}
	}
	left = numberedRowLeft(prMark(&pr), numSeg, pc.numW, pr.Title, tail, right, cols)
	return left, right
}

// prStatusCells builds the fixed-width status columns for one PR row: owner,
// triage gaps, then verdict. A column present in the geometry
// reserves its width on every row even when this PR has nothing for it, so the
// columns line up vertically down the table; composeLR flush-rights the
// resulting constant-width block. A column no PR in the list needs is zero-width
// in the geometry and skipped here, which is what keeps the empty ones free.
func prStatusCells(pr gh.OpenPR, pc prCols) []seg {
	const gap = 2
	var out []seg
	cell := func(s seg, w int) {
		if w <= 0 {
			return
		}
		if len(out) > 0 {
			out = append(out, seg{t: padN(gap)})
		}
		out = append(out, s, seg{t: padN(w - vlen(s.t))})
	}

	owner := seg{}
	if pr.Bucket != gh.BucketMine {
		owner = seg{t: "@" + pr.Author, fg: cMag}
	}
	cell(owner, pc.ownerW)
	cell(flagSeg(pr), pc.flagW)
	cell(verdictSeg(pr), pc.verdictW)
	return out
}

// gapLegend spells out what the ⚑ cell's letters mean, once, on the first
// section heading — the letters buy the table 27 cells a row, and this is what
// keeps them from being a private code. Nothing is drawn when no open PR is
// missing anything, or when the terminal is too narrow to carry it beside the
// heading's own label and count.
func gapLegend(prs []gh.OpenPR, cols int) []seg {
	const legend = "⚑ ARL = missing assignee / reviewer / labels"
	// What the heading itself needs beside the legend: its longest label
	// ("Waiting on others", 17), the count, and the gaps around both.
	const headingReserve = 24
	any := false
	for _, pr := range prs {
		if pr.Gaps != 0 {
			any = true
			break
		}
	}
	if !any || cols < vlen(legend)+headingReserve {
		return nil
	}
	return []seg{{t: legend, fg: cFaint}, {t: "   "}}
}

// prMark is a PR's state as one mark: the most urgent thing true of it wins, in
// the order conflicts ≠, checks failing ✗, changes requested ⊗, reply owed ◐,
// checks pending ◷, draft ◌, approved ●, in review ○. The PR number already says
// a PR exists, so the mark only has to say what it needs next.
func prMark(pr *gh.OpenPR) seg {
	switch {
	case pr.Mergeable == "CONFLICTING":
		return seg{t: "≠", fg: cRed}
	case pr.Checks == gh.ChecksFailing:
		return seg{t: "✗", fg: cRed}
	case pr.Review == gh.ReviewChangesRequested:
		return seg{t: "⊗", fg: cRed}
	case pr.Review == gh.ReviewNeedsResponse:
		return seg{t: "◐", fg: cYellow}
	case pr.Checks == gh.ChecksPending:
		return seg{t: "◷", fg: cYellow}
	case pr.IsDraft:
		return seg{t: "◌", fg: cMag}
	case pr.Review == gh.ReviewApproved:
		return seg{t: "●", fg: cGreen}
	}
	return seg{t: "○", fg: cDim}
}

// markNames is every mark prMark draws, most urgent first, with the word its
// legend gives it.
var markNames = []struct{ mark, name string }{
	{"≠", "conflicts"}, {"✗", "failing"}, {"⊗", "changes"}, {"◐", "reply owed"},
	{"◷", "pending"}, {"◌", "draft"}, {"●", "approved"}, {"○", "in review"},
}

// markLegend names the marks these PRs actually carry, in urgency order, so a
// mark is never a private code and a mark nobody has costs no width. nil when
// there are no PRs, or when the legend is wider than room.
func markLegend(prs []*gh.OpenPR, room int) []seg {
	present := map[string]seg{}
	for _, pr := range prs {
		mk := prMark(pr)
		present[mk.t] = mk
	}
	var out []seg
	w := 0
	for _, n := range markNames {
		mk, ok := present[n.mark]
		if !ok {
			continue
		}
		if len(out) > 0 {
			out = append(out, seg{t: "  "})
			w += 2
		}
		out = append(out, mk, seg{t: " " + n.name, fg: cFaint})
		w += vlen(mk.t) + 1 + vlen(n.name)
	}
	if w > room {
		return nil
	}
	return out
}

// flagSeg is the triage cell: the metadata the PR is missing. Conflicts, failing
// tests and draft are PR states, so prMark carries them.
func flagSeg(pr gh.OpenPR) seg {
	if l := pr.Gaps.Letters(); l != "" {
		// A assignee, R reviewer, L labels — see the PRs section of docs/api.md and
		// the help overlay. Spelled out, this cell was 32 cells wide on every row.
		return seg{t: "⚑ " + l, fg: cYellow}
	}
	return seg{}
}

// verdictSeg is the reviewer buckets' badge in the verdict column: the viewer's
// own standing, worded as whose move it is. Empty for the viewer's own and
// everyone-else PRs, whose review state is the PR's and so prMark's.
//
// The two reviewer buckets read differently on purpose. "Needs me" rows are
// yellow and say what the viewer owes. "Waiting on others" rows say what the
// viewer already did and name them as the actor — "✗ you asked for changes" is
// someone else's work to do, so it must never wear the same red as a PR of the
// viewer's own that is blocked on them. Only an approval keeps a colour there,
// the same green an approval carries on the viewer's own PRs; the rest go dim.
func verdictSeg(pr gh.OpenPR) seg {
	switch pr.Bucket {
	case gh.BucketAssigned:
		switch pr.MyReview {
		case gh.UpdatedSinceReview:
			return seg{t: "↻ updated", fg: cYellow}
		case gh.RepliedSinceReview:
			return seg{t: "↩ replied to you", fg: cYellow}
		case gh.NotReviewed:
			return seg{t: "● needs review", fg: cYellow}
		default:
			// Assigned, and they've commented without ruling — the assignment is what's
			// still outstanding, not a review, so it would be wrong to ask for one. A
			// submitted verdict clears the assignment outright and buckets the PR as
			// reviewed, so it never reaches here.
			return seg{t: "● assigned to you", fg: cYellow}
		}
	case gh.BucketReviewed:
		switch pr.MyReview {
		case gh.ReviewedApproved:
			return seg{t: "✓ you approved", fg: cGreen}
		case gh.ReviewedChangesRequested:
			return seg{t: "✗ you asked for changes", fg: cDim}
		default:
			return seg{t: "○ you commented", fg: cDim}
		}
	}
	// An author-side review state is the PR's own, so prMark carries it.
	return seg{}
}

// prCoreBadges builds the inline draft/conflicts/tests-failed badge run used by
// issuePRBadge (an issue's linked PR). The PRs tab lays these out as separate
// fixed columns instead (see prStatusCells), so it doesn't use this.
func prCoreBadges(isDraft bool, mergeable string, checks gh.CheckState) []seg {
	var badges []seg
	if isDraft {
		badges = append(badges, seg{t: "draft", fg: cFaint})
	}
	if mergeable == "CONFLICTING" {
		badges = append(badges, seg{t: "⚠ conflicts", fg: cRed})
	}
	if checks == gh.ChecksFailing {
		badges = append(badges, seg{t: "✗ tests failed", fg: cRed})
	}
	return badges
}

// joinSep interleaves a faint " · " separator between status badges.
func joinSep(badges []seg) []seg {
	if len(badges) <= 1 {
		return badges
	}
	out := make([]seg, 0, len(badges)*2-1)
	for i, b := range badges {
		if i > 0 {
			out = append(out, seg{t: "  ·  ", fg: cFaint})
		}
		out = append(out, b)
	}
	return out
}

func checksGlyph(c gh.CheckState) seg {
	switch c {
	case gh.ChecksPassing:
		return seg{t: "✓", fg: cGreen}
	case gh.ChecksFailing:
		return seg{t: "✗", fg: cRed}
	case gh.ChecksPending:
		return seg{t: "●", fg: cYellow}
	}
	return seg{t: "–", fg: cFaint}
}

func emptyOrLoading(haveData bool, empty string) string {
	if !haveData {
		return "loading…"
	}
	return empty
}

// worktreeByBranch maps each branch checked out in a linked worktree to that
// worktree's path; the main tree is omitted.
func (m Model) worktreeByBranch() map[string]string {
	return worktreesByBranch(m.local.wts, m.repoRoot)
}

func worktreesByBranch(wts []git.Worktree, repoRoot string) map[string]string {
	out := map[string]string{}
	for _, w := range wts {
		if w.Branch != "" && w.Path != repoRoot {
			out[w.Branch] = w.Path
		}
	}
	return out
}
