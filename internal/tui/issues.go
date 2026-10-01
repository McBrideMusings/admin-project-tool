package tui

import (
	"strconv"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/slug"
)

// issueStage is an issue's local-progress indicator, shown only in the
// Assigned to me / Unassigned groups (see docs/adr/0001 for why it's never
// shown for issues assigned to someone else).
type issueStage int

const (
	stageNotStarted issueStage = iota
	stageBranchExists
	stageInProgress
	stagePROpen
)

// issueRows builds the Issues view: every open issue in the repo, grouped by
// who's supposed to do the work, in the order you'd actually triage them —
// yours first, then unclaimed, then someone else's (which you might still
// consider taking). Column widths (number/status/labels) are sized once
// across every group so the view reads as a proper table — every row's title
// ends at the same column, every status badge starts at the same column —
// instead of each row's right cluster floating to its own width.
func (m Model) issueRows() []rowItem {
	mine, unassigned, others := m.groupIssues()
	for _, s := range [][]gh.IssueItem{mine, unassigned, others} {
		sortIssuesBy(s, m.trackSort)
	}
	cols := m.cols()
	numW, statusW, labelW := m.issueColumnWidths(cols, mine, unassigned, others)

	secs := []struct {
		title     string
		items     []gh.IssueItem
		showStage bool
	}{
		{"Assigned to me", mine, true},
		{"Unassigned", unassigned, true},
		{"Assigned to someone else", others, false},
	}

	// While loading (no data yet) keep every section visible so the "loading…"
	// placeholder shows; once data is in, drop any section whose group is empty.
	var rows []rowItem
	for _, s := range secs {
		if m.haveData && len(s.items) == 0 {
			continue
		}
		if len(rows) > 0 {
			rows = append(rows, blankRow())
		}
		rows = append(rows, headerRow(s.title, countSeg(len(s.items))...), ruleRow())
		rows = append(rows, m.issueSection(s.items, s.showStage, cols, numW, statusW, labelW)...)
	}
	if len(rows) == 0 {
		rows = append(rows, rowItem{kind: rowText, label: emptyOrLoading(m.haveData, "none")})
	}
	return rows
}

// issueColumnWidths sizes the three right-hand columns shared by every issue
// row: the number column widens for 5-digit+ issue numbers; the status column
// fits the widest stage/assignee badge actually present; the label column
// mirrors the original per-row width-responsive budget (mirrors the
// prototype's issueLine), just computed once from the widest label list
// instead of per row, so every row's column boundaries line up.
func (m Model) issueColumnWidths(cols int, mine, unassigned, others []gh.IssueItem) (numW, statusW, labelW int) {
	numW = 5
	natLabelW := 0
	widen := func(it gh.IssueItem, showStage bool) {
		numW = max(numW, vlen("#"+strconv.Itoa(it.Number)))
		_, statusSegs := m.issueStatus(it, showStage)
		statusW = max(statusW, segsWidth(statusSegs))
		natLabelW = max(natLabelW, vlen(strings.Join(it.Labels, ", ")))
	}
	for _, it := range mine {
		widen(it, true)
	}
	for _, it := range unassigned {
		widen(it, true)
	}
	for _, it := range others {
		widen(it, false)
	}

	minTitle := 20
	if cols >= 72 {
		minTitle = 28
	}
	reserved := statusW
	if statusW > 0 {
		reserved += 5 // "  ·  " separator
	}
	maxLabel := cols - (1 + 1 + 1 + numW + 2) - 1 - minTitle - reserved
	if maxLabel < 0 {
		maxLabel = 0
	}
	if cols >= 58 {
		labelW = natLabelW
	}
	if labelW > maxLabel {
		switch {
		case cols >= 72:
			labelW = max(13, maxLabel)
		case cols >= 58:
			labelW = min(13, maxLabel)
		default:
			labelW = 0
		}
	}
	return numW, statusW, labelW
}

// groupIssues buckets every open issue by who's supposed to do the work — the
// viewer, nobody yet, or someone else — in priority order for deciding what
// to pick up next.
func (m Model) groupIssues() (mine, unassigned, others []gh.IssueItem) {
	for _, it := range m.remote.Issues {
		switch {
		case containsLogin(it.Assignees, m.remote.ViewerLogin):
			mine = append(mine, it)
		case len(it.Assignees) == 0:
			unassigned = append(unassigned, it)
		default:
			others = append(others, it)
		}
	}
	return mine, unassigned, others
}

func containsLogin(logins []string, login string) bool {
	if login == "" {
		return false
	}
	for _, l := range logins {
		if strings.EqualFold(l, login) {
			return true
		}
	}
	return false
}

// otherAssignee returns the first assignee that isn't the viewer, for the
// "assigned to someone else" group's @handle badge. An issue can technically
// have multiple assignees; only the first non-viewer one is shown.
func otherAssignee(assignees []string, viewerLogin string) string {
	for _, a := range assignees {
		if !strings.EqualFold(a, viewerLogin) {
			return a
		}
	}
	return ""
}

func (m Model) issueSection(items []gh.IssueItem, showStage bool, cols, numW, statusW, labelW int) []rowItem {
	if len(items) == 0 {
		return []rowItem{{kind: rowText, label: emptyOrLoading(m.haveData, "none")}}
	}
	var rows []rowItem
	for _, it := range items {
		left, right := m.issueRow(it, showStage, cols, numW, statusW, labelW)
		rows = append(rows, rowItem{
			kind: rowIssue, leftSegs: left, rightSegs: right,
			issueNum: it.Number, issueTitle: it.Title, url: gh.IssueURL(m.nwo, it.Number),
		})
	}
	return rows
}

// issueStatus returns an issue's left glyph and status badge: the progress
// stage (mine/unassigned groups) or the @handle badge (someone-else group).
func (m Model) issueStatus(it gh.IssueItem, showStage bool) (glyph seg, statusSegs []seg) {
	glyph = seg{t: " "}
	if st, ok := livework.Default.WorkerStates()[strconv.Itoa(it.Number)]; ok {
		g := workerRowGlyph(st)
		return g, []seg{{t: "worker " + string(st), fg: g.fg}}
	}
	if !showStage {
		if a := otherAssignee(it.Assignees, m.remote.ViewerLogin); a != "" {
			statusSegs = []seg{{t: "@" + a, fg: cMag}}
		}
		return glyph, statusSegs
	}
	switch m.issueStageOf(it) {
	case stagePROpen:
		glyph = checksGlyph(it.PR.Checks)
		statusSegs = issuePRBadge(*it.PR)
	case stageInProgress:
		glyph = seg{t: "●", fg: cYellow}
		statusSegs = []seg{{t: "in progress", fg: cYellow}}
	case stageBranchExists:
		glyph = seg{t: "○", fg: cDim}
		statusSegs = []seg{{t: "branch started", fg: cDim}}
	}
	return glyph, statusSegs
}

// issueRow builds segments for one issue: a left glyph + padded number +
// title, and a right-hand cluster laid out in the fixed-width status/label
// columns shared by every row in the view (see issueColumnWidths) — each
// column is left-aligned and blank-padded to its shared width so rows form a
// proper table instead of each one's right cluster floating to its own width.
func (m Model) issueRow(it gh.IssueItem, showStage bool, cols, numW, statusW, labelW int) (left, right []seg) {
	glyph, statusSegs := m.issueStatus(it, showStage)

	if statusW > 0 {
		// statusW is always >= the natural width of statusSegs (computed as a
		// max across every row), so fitSegs only ever pads here, never
		// truncates — same effect as a dedicated pad-only helper, without one.
		right = append(right, fitSegs(statusSegs, statusW)...)
		if labelW > 0 {
			right = append(right, seg{t: "  ·  ", fg: cFaint})
		}
	}
	if labelW > 0 {
		labelsFull := strings.Join(it.Labels, ", ")
		right = append(right, seg{t: padEnd(truncR(labelsFull, labelW), labelW), fg: cDim})
	}

	// The number column is padded to the shared numW so every row's number
	// (and everything after it) starts at the same column.
	numStr := "#" + strconv.Itoa(it.Number)
	numSeg := seg{t: padEnd(numStr, numW), fg: cCyan, link: gh.IssueURL(m.nwo, it.Number)}
	left = numberedRowLeft(glyph, numSeg, numW, it.Title, nil, right, cols)
	return left, right
}

// issuePRBadge renders an issue's linked-PR status — draft/conflicts/tests
// failed, the same badge vocabulary as the PRs tab (via prCoreBadges), but
// without review classification (see gh.IssuePR's doc comment for why that
// doesn't generalize to a PR the viewer may not have authored).
func issuePRBadge(pr gh.IssuePR) []seg {
	badges := prCoreBadges(pr.IsDraft, pr.Mergeable, pr.Checks)
	if len(badges) == 0 {
		badges = append(badges, seg{t: "PR open", fg: cGreen})
	}
	return joinSep(badges)
}

// issueStageOf derives an issue's progress stage: an open linked PR always
// wins; otherwise it's derived from matching a local branch/worktree against
// slug.FromIssue's naming convention ("<number>-<slug>", or "issue-<number>"
// when the title yields no usable words) and its ahead-count and dirty/unsafe
// state. No matching branch means not started.
func (m Model) issueStageOf(it gh.IssueItem) issueStage {
	if it.PR != nil {
		return stagePROpen
	}
	prefix := strconv.Itoa(it.Number) + "-"
	bare := "issue-" + strconv.Itoa(it.Number)
	for _, br := range m.local.branches {
		if !strings.HasPrefix(br.Name, prefix) && br.Name != bare {
			continue
		}
		dirty := m.local.branchUnsafe[br.Name] || (br.WorktreePath != "" && m.local.wtState[br.WorktreePath].dirty)
		if br.Ahead > 0 || dirty {
			return stageInProgress
		}
		return stageBranchExists
	}
	return stageNotStarted
}

// issueBranch derives the branch name a worktree gets when you start work on an issue.
func issueBranch(number int, title string) string {
	return slug.FromIssue(number, title)
}
