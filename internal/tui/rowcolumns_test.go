package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// These tests pin the remaining list panes as tables, the same way
// TestBranchRowsFormFixedColumns pins the Branches pane: every field has to
// occupy the same cells on every row whatever that row's neighbouring values
// happen to be. Sizing a row from its own content instead makes each value's
// position depend on the length of the one next to it, so nothing lines up and
// the pane reads as a run of strings rather than something you can scan down.

// rowLine composes one flat-tab row (PRs, Issues) into the full-width line the
// view renders, so a test can read a column straight out of it by offset.
func rowLine(r rowItem, cols int) []rune {
	return []rune(segsText(composeLR(r.leftSegs, r.rightSegs, cols)))
}

func rowsOfKind(rows []rowItem, k rowKind) []rowItem {
	var out []rowItem
	for _, r := range rows {
		if r.kind == k {
			out = append(out, r)
		}
	}
	return out
}

func TestCommitRowsFormFixedColumns(t *testing.T) {
	const w = 60
	m := seededModel()
	msgW := w - 1 - cmShaW - 2 - 1 - cmAgeW
	shaAt, msgAt, ageAt := 1, 1+cmShaW+2, w-cmAgeW

	for _, tc := range []struct {
		c                 git.Commit
		sha, subject, age string
	}{
		{git.Commit{SHA: "abc1234", Subject: "do a thing", When: "1 hour ago"}, "abc1234", "do a thing", "1h"},
		{
			git.Commit{SHA: "deadbee", Subject: strings.Repeat("a considerably longer subject ", 4), When: "11 months ago"},
			"deadbee", strings.Repeat("a considerably longer subject ", 4)[:msgW-1] + "…", "11mo",
		},
		{git.Commit{SHA: "0000000", Subject: "", When: "just now"}, "0000000", "", "now"},
	} {
		text := []rune(segsText(m.cmPanelLine(tc.c, w)))
		if len(text) != w {
			t.Fatalf("%s: row is %d cells wide, want %d: %q", tc.sha, len(text), w, string(text))
		}
		for _, c := range []struct{ name, want, got string }{
			{"sha", tc.sha, string(text[shaAt : shaAt+cmShaW])},
			{"subject", tc.subject, string(text[msgAt : msgAt+msgW])},
			{"age", tc.age, string(text[ageAt:])},
		} {
			if strings.TrimSpace(c.got) != strings.TrimSpace(c.want) {
				t.Errorf("%s: %s column holds %q, want %q — the columns have shifted", tc.sha, c.name, c.got, c.want)
			}
		}
	}
}

func TestStashRowsFormFixedColumns(t *testing.T) {
	const w = 50
	m := seededModel()
	msgW := w - 1 - stRefW - 2
	refAt, msgAt := 1, 1+stRefW+2

	for _, tc := range []struct{ s git.Stash }{
		{git.Stash{Ref: "stash@{0}", Subject: "wip"}},
		{git.Stash{Ref: "stash@{12}", Subject: "on feature: a longer stash subject"}},
		{git.Stash{Ref: "stash@{3}", Subject: ""}},
	} {
		text := []rune(segsText(m.stPanelLine(tc.s, w)))
		if len(text) != w {
			t.Fatalf("%s: row is %d cells wide, want %d: %q", tc.s.Ref, len(text), w, string(text))
		}
		if got := strings.TrimSpace(string(text[refAt : refAt+stRefW])); got != tc.s.Ref {
			t.Errorf("%s: ref column holds %q — the columns have shifted", tc.s.Ref, got)
		}
		if got := strings.TrimSpace(string(text[msgAt : msgAt+msgW])); got != truncR(tc.s.Subject, msgW) {
			t.Errorf("%s: subject column holds %q, want %q", tc.s.Ref, got, tc.s.Subject)
		}
	}
}

// TestNullStateRendersBlankCell pins the null-state rendering: a branch with no
// state renders a blank padded cell rather than the words "empty" or "no commits",
// while keeping the cell's full width so column offsets do not shift.
func TestNullStateRendersBlankCell(t *testing.T) {
	const w = 90
	m := seededModel()
	m.identity = config.Identity{Login: "me", Emails: []string{"me@example.com"}}
	m.local.branches = []git.BranchInfo{
		{Name: "commitless", LastCommit: "1 second ago"},                            // null state: no commits
		{Name: "old-merged", HasRemote: true, Ahead: 0, LastCommit: "3 months ago"}, // merged
	}
	m.local.firstAuthor = map[string]string{
		"commitless": "me@example.com",
		"old-merged": "me@example.com",
	}
	m.remote.MergedHeads["old-merged"] = "sha-old-merged"

	// Column positions computed the same way as TestBranchRowsFormFixedColumns.
	ageAt := w - brAgeW
	readyAt := ageAt - 1 - brReadyW
	holdAt := readyAt - 1 - brHoldW
	stateAt := holdAt - 1 - brStateW
	ownerAt := stateAt - 1 - brOwnerW

	commitlessRow := branchRowAt(t, m, "commitless", w)
	commitlessText := []rune(segsText(commitlessRow))
	if len(commitlessText) != w {
		t.Fatalf("commitless: row is %d cells wide, want %d", len(commitlessText), w)
	}

	// The null-state branch must have a blank state cell that is still brStateW wide.
	commitlessStateCell := string(commitlessText[stateAt : stateAt+brStateW])
	if len([]rune(commitlessStateCell)) != brStateW {
		t.Errorf("commitless: state cell is %d cells, want %d", len([]rune(commitlessStateCell)), brStateW)
	}
	if strings.TrimSpace(commitlessStateCell) != "" {
		t.Errorf("commitless: state cell should be blank but holds %q", commitlessStateCell)
	}

	mergedRow := branchRowAt(t, m, "old-merged", w)
	mergedText := []rune(segsText(mergedRow))
	if len(mergedText) != w {
		t.Fatalf("old-merged: row is %d cells wide, want %d", len(mergedText), w)
	}

	// The merged branch must still show its state word and keep the same column offsets.
	mergedStateCell := string(mergedText[stateAt : stateAt+brStateW])
	if !strings.Contains(mergedStateCell, "merged") {
		t.Errorf("old-merged: state cell should contain 'merged' but holds %q", mergedStateCell)
	}

	// Both rows must have the same column offsets: the blank cell doesn't shift anything.
	for _, c := range []struct {
		name  string
		at, n int
	}{
		{"owner", ownerAt, brOwnerW},
		{"hold", holdAt, brHoldW},
		{"ready", readyAt, brReadyW},
		{"age", ageAt, brAgeW},
	} {
		commitlessCol := string(commitlessText[c.at : c.at+c.n])
		mergedCol := string(mergedText[c.at : c.at+c.n])
		// Both should have the same width.
		if len([]rune(commitlessCol)) != len([]rune(mergedCol)) {
			t.Errorf("column %s: commitless is %d cells, merged is %d cells — null state shifted the columns",
				c.name, len([]rune(commitlessCol)), len([]rune(mergedCol)))
		}
	}
}

// TestPRVerdictSaysWhoseMoveItIs pins the wording split between the two reviewer
// buckets. A PR the viewer has already had their say on must never wear a bare
// author-POV verdict: "changes requested" on someone else's PR read as work the
// viewer owed, when in fact the viewer is the one who asked for it. Rows in the
// waiting bucket name the viewer as the actor and stay out of the alarm colours.
func TestPRVerdictSaysWhoseMoveItIs(t *testing.T) {
	tests := []struct {
		name string
		pr   gh.OpenPR
		want string
	}{
		{"asked, untouched", gh.OpenPR{Bucket: gh.BucketAssigned, MyReview: gh.NotReviewed}, "● needs review"},
		{"new commit since my word", gh.OpenPR{Bucket: gh.BucketAssigned, MyReview: gh.UpdatedSinceReview}, "↻ updated"},
		{"answered directly", gh.OpenPR{Bucket: gh.BucketAssigned, MyReview: gh.RepliedSinceReview}, "↩ replied to you"},
		// Reachable only via assignment, which a comment doesn't clear: asking for a
		// review they've already spoken in would be the wrong thing to put on the row.
		// A verdict does clear it, so ReviewedApproved never pairs with this bucket.
		{"assigned, already commented", gh.OpenPR{Bucket: gh.BucketAssigned, MyReview: gh.ReviewedCommented}, "● assigned to you"},
		{"I approved", gh.OpenPR{Bucket: gh.BucketReviewed, MyReview: gh.ReviewedApproved}, "✓ you approved"},
		{"I asked for changes", gh.OpenPR{Bucket: gh.BucketReviewed, MyReview: gh.ReviewedChangesRequested}, "✗ you asked for changes"},
		{"I only commented", gh.OpenPR{Bucket: gh.BucketReviewed, MyReview: gh.ReviewedCommented}, "○ you commented"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verdictSeg(tt.pr)
			if got.t != tt.want {
				t.Errorf("verdict = %q, want %q", got.t, tt.want)
			}
			if tt.pr.Bucket == gh.BucketReviewed && got.fg == cRed {
				t.Errorf("verdict %q is red — a PR waiting on someone else must not read as blocking the viewer", got.t)
			}
		})
	}
}

func TestPRRowsFormFixedColumns(t *testing.T) {
	const cols = 140
	// Pin the marker: the column offsets below assume its default two cells, and
	// an ambient REPO_BRANCH_GLYPH would otherwise move every one of them.
	t.Setenv("REPO_BRANCH_GLYPH", defaultBranchGlyph)
	m := seededModel()
	m.width = cols
	m.remote.PRs = []gh.OpenPR{
		{Number: 7, Title: "Short one", HeadBranch: "feature", Checks: gh.ChecksPassing, Review: gh.ReviewApproved, Bucket: gh.BucketMine},
		{
			Number: 1234, Title: "A considerably longer pull request title that keeps going",
			HeadBranch: "very-long-branch-name-goes-here", Author: "alice",
			Bucket: gh.BucketOther, Mergeable: "CONFLICTING",
		},
		{Number: 22, Title: "No head branch on this one", Author: "bob", Bucket: gh.BucketAssigned, MyReview: gh.NotReviewed},
	}
	pc := m.prColumns(cols)
	if pc.branchW == 0 || pc.verdictW == 0 {
		t.Fatalf("fixture produced no branch/verdict column: %+v", pc)
	}

	want := map[int]struct{ num, branch, verdict string }{
		7:    {"#7", "feature", "approved"},
		1234: {"#1234", "very-long-branch-name-goes-…", "in review"},
		22:   {"#22", "", "● needs review"},
	}
	branchAt := -1
	rightW := -1
	prRows := rowsOfKind(m.prRows(), rowPR)
	if len(prRows) != len(m.remote.PRs) {
		t.Fatalf("rendered %d PR rows, want %d", len(prRows), len(m.remote.PRs))
	}
	for _, r := range prRows {
		text := rowLine(r, cols)
		if len(text) != cols {
			t.Fatalf("#%d: row is %d cells wide, want %d", r.prNum, len(text), cols)
		}
		w := want[r.prNum]

		// The number column is shared, so the title (and everything after it)
		// starts at the same cell however many digits the PR number has.
		if got := strings.TrimSpace(string(text[2 : 2+pc.numW])); got != w.num {
			t.Errorf("#%d: number column holds %q, want %q", r.prNum, got, w.num)
		}
		// The whole right-hand status block is one constant-width run.
		if rw := segsWidth(r.rightSegs); rightW == -1 {
			rightW = rw
		} else if rw != rightW {
			t.Errorf("#%d: right cluster is %d cells, want %d — the status columns have shifted", r.prNum, rw, rightW)
		}
		if got := strings.TrimSpace(string(text[cols-pc.verdictW:])); got != w.verdict {
			t.Errorf("#%d: verdict column holds %q, want %q", r.prNum, got, w.verdict)
		}
		// The branch column keeps its cells even on a PR with no head branch.
		at := strings.Index(string(text), strings.TrimSpace(branchGlyph()))
		if w.branch == "" {
			if at != -1 {
				t.Errorf("#%d: branchless PR grew a branch glyph", r.prNum)
			}
			continue
		}
		at = len([]rune(string(text)[:at]))
		if branchAt == -1 {
			branchAt = at
		} else if at != branchAt {
			t.Errorf("#%d: branch column starts at cell %d, want %d — the columns have shifted", r.prNum, at, branchAt)
		}
		if got := strings.TrimSpace(string(text[at+2 : at+2+pc.branchW])); got != w.branch {
			t.Errorf("#%d: branch column holds %q, want %q", r.prNum, got, w.branch)
		}
	}
	if branchAt == -1 || rightW == -1 {
		t.Fatal("no PR rows rendered")
	}
	// The branchless PR has to leave that column blank rather than pull the
	// following columns left.
	for _, r := range rowsOfKind(m.prRows(), rowPR) {
		if r.prNum != 22 {
			continue
		}
		text := rowLine(r, cols)
		if got := string(text[branchAt : branchAt+2+pc.branchW]); strings.TrimSpace(got) != "" {
			t.Errorf("#22: branch column holds %q, want blank padding", got)
		}
	}
}

// A PR whose worst problem is its build has to say "✗ tests failed" in words at
// every width, not just wear a red ✗ next to its number. The flag columns used
// to drop as a group on a narrow terminal and leave the row reading "in review".
func TestFailingChecksAlwaysSpelledOut(t *testing.T) {
	for _, cols := range []int{50, 60, 80, 140, 200} {
		m := seededModel()
		m.width = cols
		m.remote.PRs = []gh.OpenPR{
			{Number: 7, Title: "Passing one", HeadBranch: "feature", Checks: gh.ChecksPassing, Bucket: gh.BucketMine},
			{
				Number: 1234, Title: "A considerably longer pull request title that keeps going",
				HeadBranch: "some-branch-with-a-decent-length", Checks: gh.ChecksFailing, Bucket: gh.BucketMine,
			},
		}
		for _, r := range rowsOfKind(m.prRows(), rowPR) {
			line := rowLine(r, cols)
			// A row wider than the terminal wraps, and a wrapped row destroys the
			// table. The branch column gives way to keep every row inside the width.
			// Below 60 cells it has already gone and the row is down to its floor —
			// number, four characters of title, and the status columns, which is the
			// point where there is nothing left to give that isn't the status itself.
			if cols >= 60 && len(line) > cols {
				t.Errorf("cols=%d: #%d row is %d cells wide — it spills past the right edge", cols, r.prNum, len(line))
			}
			text := string(line)
			if r.prNum == 1234 && !strings.Contains(text, "✗ tests failed") {
				t.Errorf("cols=%d: failing PR row reads %q, want it to say \"✗ tests failed\"", cols, strings.TrimSpace(text))
			}
			if r.prNum == 7 && strings.Contains(text, "tests failed") {
				t.Errorf("cols=%d: passing PR row claims failing tests: %q", cols, strings.TrimSpace(text))
			}
		}
	}
}

// The flag column costs no width at all when no open PR carries a flag — that
// reservation is what used to squeeze the title until the flag columns dropped.
func TestUnusedFlagColumnsTakeNoWidth(t *testing.T) {
	const cols = 140
	m := seededModel()
	m.width = cols
	m.remote.PRs = []gh.OpenPR{
		{Number: 7, Title: "Passing one", HeadBranch: "feature", Checks: gh.ChecksPassing, Bucket: gh.BucketMine},
	}
	if pc := m.prColumns(cols); pc.flagW != 0 {
		t.Errorf("no PR is draft/conflicting/failing/untriaged, but the flag column is %d cells, want 0", pc.flagW)
	}
	m.remote.PRs[0].IsDraft = true
	if pc := m.prColumns(cols); pc.flagW != vlen("draft") {
		t.Errorf("one draft PR gives a flag column of %d cells, want %d", pc.flagW, vlen("draft"))
	}
}

// Four sparse flags share one column, so the cell names the worst one and counts
// the rest rather than growing a column each. It must never hide that there are
// more: the "+N" is what keeps one bounded column honest.
func TestFlagCellNamesWorstAndCountsRest(t *testing.T) {
	cases := []struct {
		name string
		pr   gh.OpenPR
		want string
	}{
		{"clean", gh.OpenPR{Number: 1}, ""},
		{"labels only", gh.OpenPR{Number: 2, Gaps: gh.GapLabels}, "⚑ L"},
		{"all three gaps", gh.OpenPR{Number: 3, Gaps: gh.GapAssignee | gh.GapReviewer | gh.GapLabels}, "⚑ ARL"},
		{"tests beat draft", gh.OpenPR{Number: 4, Checks: gh.ChecksFailing, IsDraft: true}, "✗ tests failed +1"},
		{"conflicts beat everything", gh.OpenPR{
			Number: 5, Mergeable: "CONFLICTING", Checks: gh.ChecksFailing,
			IsDraft: true, Gaps: gh.GapLabels,
		}, "⚠ conflicts +3"},
	}
	for _, c := range cases {
		if got := flagSeg(c.pr).t; got != c.want {
			t.Errorf("%s: flag cell reads %q, want %q", c.name, got, c.want)
		}
	}
}

// The branch column takes every cell above the title floor, so freeing width
// anywhere else on the row lands in the branch column unless the floor moves
// with it. A branch name here is a slug of the title beside it — when only one
// of the two fits, the title is the one that has to survive.
func TestBranchColumnNeverEatsTheTitle(t *testing.T) {
	prs := []gh.OpenPR{
		{Number: 1876, Title: "Cross-play shootouts land on the shared table", HeadBranch: "1876-cross-play-shootouts", Bucket: gh.BucketMine, Checks: gh.ChecksPassing, Review: gh.ReviewApproved},
		{Number: 1828, Title: "perf(ui): restore lobby scroll position", HeadBranch: "1828-perf-ui-restore-lobby-scroll", Bucket: gh.BucketMine, IsDraft: true, Mergeable: "CONFLICTING", Gaps: gh.GapReviewer | gh.GapLabels},
		{Number: 1911, Title: "feat(friends): multi-select invites from the lobby", HeadBranch: "1911-friends-multi-select", Author: "biggmackk17", Bucket: gh.BucketOther, Checks: gh.ChecksFailing, Gaps: gh.GapAssignee | gh.GapLabels},
	}
	for _, cols := range []int{80, 100, 128, 160, 200} {
		m := seededModel()
		m.width = cols
		m.remote.PRs = prs
		pc := m.prColumns(cols)
		title := cols - (pc.numW + 6) - pc.rightWidth()
		if pc.branchW > 0 {
			// The floor bounds what the branch column may take. It cannot conjure
			// width the number and status block have already spent, so it only binds
			// while there is a branch column left to give any back.
			if title -= pc.branchW + 4 + vlen(branchGlyph()); title < titleFloor(cols) {
				t.Errorf("cols=%d: branch column takes %d cells and leaves the title %d, floor is %d",
					cols, pc.branchW, title, titleFloor(cols))
			}
		}
		// Rows still have to fit the terminal at every width.
		for _, r := range rowsOfKind(m.prRows(), rowPR) {
			if n := len(rowLine(r, cols)); n > cols {
				t.Errorf("cols=%d: #%d row is %d cells wide", cols, r.prNum, n)
			}
		}
	}
	// At 128 the branch column is the field that gives way; at 200 there is
	// genuine surplus and it comes back.
	narrow := seededModel()
	narrow.width, narrow.remote.PRs = 128, prs
	if w := narrow.prColumns(128).branchW; w != 0 {
		t.Errorf("at 128 cells the branch column is %d wide, want it dropped for the title", w)
	}
	wide := seededModel()
	wide.width, wide.remote.PRs = 200, prs
	if w := wide.prColumns(200).branchW; w == 0 {
		t.Error("at 200 cells there is surplus, but the branch column is still dropped")
	}
}

// Merging the flag columns is only worth doing if the title gets the cells back.
func TestMergedFlagColumnLeavesRoomForTheTitle(t *testing.T) {
	const cols = 128
	m := seededModel()
	m.width = cols
	m.remote.PRs = []gh.OpenPR{
		{Number: 1912, Title: "Passing one", Author: "alexthemighty", Bucket: gh.BucketOther, Gaps: gh.GapAssignee | gh.GapLabels},
		{Number: 1869, Title: "Conflicting draft", Author: "alexthemighty", Bucket: gh.BucketOther, IsDraft: true, Mergeable: "CONFLICTING"},
		{Number: 1911, Title: "Failing one", Author: "biggmackk17", Bucket: gh.BucketOther, Checks: gh.ChecksFailing, Gaps: gh.GapAssignee},
	}
	pc := m.prColumns(cols)
	// numberedRowLeft budgets numW+6 for the glyph/number cluster.
	title := cols - (pc.numW + 6) - pc.rightWidth()
	if pc.branchW > 0 {
		title -= pc.branchW + 4 + vlen(branchGlyph())
	}
	if title < 55 {
		t.Errorf("at %d cells the title gets %d (right block %d) — the flag merge bought nothing", cols, title, pc.rightWidth())
	}
}

func TestIssueRowsFormFixedColumns(t *testing.T) {
	const cols = 140
	m := seededModel()
	m.width = cols
	m.remote.Issues = []gh.IssueItem{
		{Number: 5, Title: "Fix bug", Labels: []string{"bug"}, Assignees: []string{"me"}},
		{Number: 1234, Title: "A considerably longer issue title", Labels: []string{"enhancement", "documentation"}},
		{Number: 77, Title: "Someone else's issue", Assignees: []string{"alice"}},
	}
	mine, unassigned, others := m.groupIssues()
	numW, statusW, labelW := m.issueColumnWidths(cols, mine, unassigned, others)
	if labelW == 0 || statusW == 0 {
		t.Fatalf("fixture produced no label/status column: numW=%d statusW=%d labelW=%d", numW, statusW, labelW)
	}
	labelAt := cols - labelW
	statusAt := labelAt - 5 - statusW // 5 = the "  ·  " separator

	want := map[int]struct{ num, status, labels string }{
		5:    {"#5", "", "bug"},
		1234: {"#1234", "", "enhancement, documentation"},
		77:   {"#77", "@alice", ""},
	}
	issueRows := rowsOfKind(m.issueRows(), rowIssue)
	if len(issueRows) != len(m.remote.Issues) {
		t.Fatalf("rendered %d issue rows, want %d", len(issueRows), len(m.remote.Issues))
	}
	for _, r := range issueRows {
		text := rowLine(r, cols)
		if len(text) != cols {
			t.Fatalf("#%d: row is %d cells wide, want %d", r.issueNum, len(text), cols)
		}
		w := want[r.issueNum]
		for _, c := range []struct{ name, got, want string }{
			{"number", string(text[2 : 2+numW]), w.num},
			{"status", string(text[statusAt : statusAt+statusW]), w.status},
			{"labels", string(text[labelAt:]), w.labels},
		} {
			if strings.TrimSpace(c.got) != c.want {
				t.Errorf("#%d: %s column holds %q, want %q — the columns have shifted", r.issueNum, c.name, c.got, c.want)
			}
		}
	}
}

// TestBeadRowsFormFixedColumns pins the Beads pane as a table: id, priority,
// type, how much work the row unblocks, labels, assignee and age each hold the
// same cells on every row, in every section, whatever that row's own values are.
//
// The unblocks column also pins its own thinness: it is exactly as wide as the
// widest count in the list and no wider, and a bead that frees nothing leaves the
// cell blank rather than printing a "0" on most of the pane.
func TestBeadRowsFormFixedColumns(t *testing.T) {
	const cols = 140
	m := seededModel()
	m.width = cols
	m.haveBeads = true
	m.hasBeads = true
	m.beadPrefix = "bdtest"
	m.beads = []bd.Bead{
		{ID: "bdtest-38t", Title: "Auth token expiry off by one", Status: "open", Type: "bug", Priority: 0, Labels: []string{"backend", "security"}, Assignee: "pierce"},
		{ID: "bdtest-qt0", Title: "Add password reset", Status: "open", Type: "feature", Priority: 1, Labels: []string{"backend"}},
		{ID: "bdtest-1xn", Title: "Bump deps", Status: "in_progress", Type: "chore", Priority: 2, Labels: []string{"tooling"}, Assignee: "pierce"},
		{ID: "bdtest-isd", Title: "Wire reset email", Status: "open", Type: "task", Priority: 1, Blockers: []string{"bdtest-qt0"}},
		{ID: "bdtest-63h", Title: "Docs for reset", Status: "open", Type: "task", Priority: 2, Assignee: "alice", Blockers: []string{"bdtest-qt0", "bdtest-isd", "bdtest-38t"}},
	}
	// "owner" is not in the default set — a solo workspace spends nine cells on
	// one name — so the fixture asks for it, which is what the column exists for.
	m.beadCols = []string{"id", "priority", "type", "glyph", "title", "unblocks", "labels", "owner", "age"}
	human, ready, inProgress, blocked := groupBeads(m.beads)
	l := beadLayoutFor(m.beadCols, cols, m.beadCtx(false), human, ready, inProgress, blocked)
	wID, wType := l.widths[l.indexOf("id")], l.widths[l.indexOf("type")]
	wLabel, wOwner := l.widths[l.indexOf("labels")], l.widths[l.indexOf("owner")]
	wUnblocks := l.widths[l.indexOf("unblocks")]
	if wLabel == 0 || wOwner == 0 {
		t.Fatalf("fixture produced an empty column: labels=%d owner=%d", wLabel, wOwner)
	}
	// The fixture's largest count is 2, so the column is one cell. This is the
	// thinness guarantee: the column costs a digit, not a reserved field.
	if wUnblocks != 1 {
		t.Fatalf("unblocks column is %d cells, want 1 — it must fit the widest count and no more", wUnblocks)
	}

	// The lead (cursor) cell is prepended by renderRow, and the widths are
	// budgeted for it, so the line has to be composed with it here too — without
	// it every left-anchored column sits one cell early.
	idAt := 2
	prioAt := idAt + wID + 2
	typeAt := prioAt + 2 + 2
	ageAt := cols - beadAgeW
	assigneeAt := ageAt - 2 - wOwner
	labelAt := assigneeAt - 2 - wLabel
	unblocksAt := labelAt - 2 - wUnblocks

	// The id cell holds the *shortened* id: the workspace prefix every row shares
	// is dropped, so what is left is the part that tells two beads apart.
	//
	// The unblocks counts come out of the fixture's own graph: 63h waits on qt0,
	// isd and 38t, and isd waits on qt0. So qt0 frees two (isd directly, 63h once
	// isd clears), isd and 38t free one each, and the two nothing waits on are
	// blank rather than "0".
	want := map[string]struct{ id, prio, typ, unblocks, labels, assignee string }{
		"bdtest-38t": {"38t", "P0", "bug", "1", "backend, security", "pierce"},
		"bdtest-qt0": {"qt0", "P1", "feature", "2", "backend", ""},
		"bdtest-1xn": {"1xn", "P2", "chore", "", "tooling", "pierce"},
		"bdtest-isd": {"isd", "P1", "task", "1", "", ""},
		"bdtest-63h": {"63h", "P2", "task", "", "", "alice"},
	}
	beadRows := rowsOfKind(m.beadRows(), rowBead)
	if len(beadRows) != len(m.beads) {
		t.Fatalf("rendered %d bead rows, want %d", len(beadRows), len(m.beads))
	}
	for _, r := range beadRows {
		text := []rune(segsText(composeLR(append([]seg{leadSeg(false)}, r.leftSegs...), r.rightSegs, cols)))
		if len(text) != cols {
			t.Fatalf("%s: row is %d cells wide, want %d", r.beadID, len(text), cols)
		}
		b := want[r.beadID]
		for _, c := range []struct{ name, got, want string }{
			{"id", string(text[idAt : idAt+wID]), b.id},
			{"priority", string(text[prioAt : prioAt+2]), b.prio},
			{"type", string(text[typeAt : typeAt+wType]), b.typ},
			{"unblocks", string(text[unblocksAt : unblocksAt+wUnblocks]), b.unblocks},
			{"labels", string(text[labelAt : labelAt+wLabel]), b.labels},
			{"assignee", string(text[assigneeAt : assigneeAt+wOwner]), b.assignee},
		} {
			if strings.TrimSpace(c.got) != c.want {
				t.Errorf("%s: %s column holds %q, want %q — the columns have shifted", r.beadID, c.name, c.got, c.want)
			}
		}
	}
}

// TestBeadBlockingRowsFormFixedColumns pins the overlay's Blocking tab as a
// table: id, priority, type and title each hold the same cells on every row,
// whatever that row's own values are, per this repo's row-layout rule.
func TestBeadBlockingRowsFormFixedColumns(t *testing.T) {
	const innerW = 100
	m := seededModel()
	m.beadPrefix = "bdtest"
	target := bd.Bead{ID: "bdtest-p", Title: "Parent", Status: "open", Type: "task", Priority: 1}
	m.beads = []bd.Bead{
		target,
		{ID: "bdtest-a", Title: "Short one", Status: "open", Type: "bug", Priority: 0, Blockers: []string{"bdtest-p"}},
		{ID: "bdtest-longid9", Title: "A rather longer title here", Status: "open", Type: "feature", Priority: 2, Blockers: []string{"bdtest-p"}},
		{ID: "bdtest-c", Title: "Chore item", Status: "open", Type: "chore", Priority: 1, Blockers: []string{"bdtest-p"}},
	}

	rows := m.beadBlockingLines(target, innerW)
	if len(rows) != 3 {
		t.Fatalf("blocking rows for bdtest-p = %d, want 3", len(rows))
	}

	wID, wType := 0, 0
	for _, d := range m.beads[1:] {
		if l := vlen(shortBeadID(d.ID, m.beadPrefix)); l > wID {
			wID = l
		}
		if l := vlen(d.Type); l > wType {
			wType = l
		}
	}
	idAt := 1
	prioAt := idAt + wID + 2
	typeAt := prioAt + 2 + 2
	titleAt := typeAt + wType + 2

	want := map[string]struct{ prio, typ, title string }{
		"a":       {"P0", "bug", "Short one"},
		"longid9": {"P2", "feature", "A rather longer title here"},
		"c":       {"P1", "chore", "Chore item"},
	}
	for _, row := range rows {
		text := []rune(segsText(row))
		if len(text) < titleAt {
			t.Fatalf("row %q is too narrow for the fixed columns", string(text))
		}
		id := strings.TrimSpace(string(text[idAt : idAt+wID]))
		b, ok := want[id]
		if !ok {
			t.Fatalf("row id column holds %q, which is not one of the fixture's dependents", id)
		}
		if got := strings.TrimSpace(string(text[prioAt : prioAt+2])); got != b.prio {
			t.Errorf("%s: priority column holds %q, want %q — the columns have shifted", id, got, b.prio)
		}
		if got := strings.TrimSpace(string(text[typeAt : typeAt+wType])); got != b.typ {
			t.Errorf("%s: type column holds %q, want %q — the columns have shifted", id, got, b.typ)
		}
		if got := strings.TrimSpace(string(text[titleAt:])); got != b.title {
			t.Errorf("%s: title column holds %q, want %q — the columns have shifted", id, got, b.title)
		}
	}
}

// TestShortBeadIDStripsPrefixAndCaps pins what the id column is allowed to
// spend. Every id in a workspace carries the same prefix, so the prefix
// distinguishes nothing and comes off; what is left is capped from the left,
// because a timestamp-import id differs at its tail.
func TestShortBeadIDStripsPrefixAndCaps(t *testing.T) {
	cases := []struct {
		name, id, prefix, want string
	}{
		{"prefix with dash", "bdtest-4f2", "bdtest", "4f2"},
		{"prefix already carrying the dash", "bdtest-4f2", "bdtest-", "4f2"},
		{"no prefix configured", "bdtest-4f2", "", "bdtest-4f2"},
		{"unrelated prefix leaves the id alone", "other-4f2", "bdtest", "other-4f2"},
		{
			"timestamp import caps to 12 keeping the tail",
			"apple-notepad-1786207837888-76-90c93d5a.11", "apple-notepad",
			"…90c93d5a.11",
		},
	}
	for _, c := range cases {
		got := shortBeadID(c.id, c.prefix)
		if got != c.want {
			t.Errorf("%s: shortBeadID(%q, %q) = %q, want %q", c.name, c.id, c.prefix, got, c.want)
		}
		if vlen(got) > beadIDW {
			t.Errorf("%s: %q is %d cells wide, over the %d cap", c.name, got, vlen(got), beadIDW)
		}
	}
}

// TestBeadTreePutsBlockersAboveBlocked is the whole point of the tree: a bead
// you cannot start sits under the bead you have to finish first. A bead waiting
// on two open blockers can only sit under one of them, so it carries the other
// as a count rather than appearing twice.
func TestBeadTreePutsBlockersAboveBlocked(t *testing.T) {
	beads := []bd.Bead{
		{ID: "bdtest-qt0", Title: "Add password reset", Status: "open"},
		{ID: "bdtest-isd", Title: "Wire reset email", Status: "open", Blockers: []string{"bdtest-qt0"}},
		{ID: "bdtest-63h", Title: "Docs for reset", Status: "open", Blockers: []string{"bdtest-qt0", "bdtest-isd"}},
		{ID: "bdtest-zzz", Title: "On ice", Status: "deferred"},
	}
	got := beadForest(beads)
	want := []struct {
		id           string
		depth, extra int
	}{
		{"bdtest-qt0", 0, 0},
		{"bdtest-isd", 1, 0},
		{"bdtest-63h", 2, 1},
	}
	if len(got) != len(want) {
		t.Fatalf("forest has %d nodes, want %d — a deferred bead is rendering, or one dropped out", len(got), len(want))
	}
	for i, w := range want {
		n := got[i]
		if n.b.ID != w.id || n.depth != w.depth || n.extra != w.extra {
			t.Errorf("node %d is %s depth=%d extra=%d, want %s depth=%d extra=%d",
				i, n.b.ID, n.depth, n.extra, w.id, w.depth, w.extra)
		}
	}
}

// TestBeadTreeSurvivesACycle: `bd dep cycles` exists because a blocks-cycle is
// possible, and neither bead in one is a root. They still both have to render —
// a bead that vanishes from the pane is worse than one shown at the wrong depth.
func TestBeadTreeSurvivesACycle(t *testing.T) {
	beads := []bd.Bead{
		{ID: "bdtest-aaa", Title: "A", Status: "open", Blockers: []string{"bdtest-bbb"}},
		{ID: "bdtest-bbb", Title: "B", Status: "open", Blockers: []string{"bdtest-aaa"}},
	}
	got := beadForest(beads)
	if len(got) != 2 {
		t.Fatalf("cycle rendered %d rows, want 2", len(got))
	}
	seen := map[string]bool{}
	for _, n := range got {
		seen[n.b.ID] = true
	}
	if !seen["bdtest-aaa"] || !seen["bdtest-bbb"] {
		t.Errorf("cycle lost a bead: rendered %v", seen)
	}
}

// TestCleanUpRowsDropTheirPurposeSubline pins the Clean Up gate at the real
// renderer: a Clean Up row is about to be deleted, so its purpose subline
// ("↳ ...") — what the branch is FOR — is dead weight and must never appear,
// while every other section keeps its subline exactly as before. Asserting
// against branchPaneLines rather than rebuilding its loop is what makes this
// test fail if the gate in branchPaneLines is ever removed.
func TestCleanUpRowsDropTheirPurposeSubline(t *testing.T) {
	m := seededModel()
	// seededModel already files old-merged under secCleanup (MergedHeads has it).
	// feature lands in some non-Cleanup section. Give both a note so each would
	// draw a subline if the gate didn't stop Clean Up's.
	m.local.branchNote = map[string]string{
		"old-merged": "leftover experiment",
		"feature":    "the thing I'm actually working on",
	}

	rows := m.branchRows()
	byName := map[string]git.BranchInfo{}
	for _, b := range m.local.branches {
		byName[b.Name] = b
	}
	counts := map[section]int{}
	for _, r := range rows {
		counts[r.sec]++
	}
	const w = 100
	lines, itemLine := m.branchPaneLines(rows, byName, counts, w)

	// Locate the Clean Up heading and the next heading/blank after it, and scan
	// only that span for the purpose glyph — it appears nowhere else in this pane.
	cleanAt := -1
	for i, l := range lines {
		if strings.Contains(segsText(l), "Clean Up") {
			cleanAt = i
			break
		}
	}
	if cleanAt == -1 {
		t.Fatal("no Clean Up heading rendered — fixture no longer files old-merged there")
	}
	cleanEnd := len(lines)
	for i := cleanAt + 1; i < len(lines); i++ {
		if len(lines[i]) == 1 && lines[i][0].t == "" {
			cleanEnd = i
			break
		}
	}
	cleanUpLineCount := cleanEnd - cleanAt - 1 // minus the heading itself
	for i := cleanAt + 1; i < cleanEnd; i++ {
		if strings.Contains(segsText(lines[i]), "↳") {
			t.Errorf("Clean Up line %d carries a purpose subline: %q", i, segsText(lines[i]))
		}
	}
	if cleanUpLineCount != counts[secCleanup] {
		t.Errorf("Clean Up drew %d lines for %d rows — a row grew a subline", cleanUpLineCount, counts[secCleanup])
	}

	// feature's own (non-Cleanup) section must still emit its subline.
	featureLine := -1
	for i, r := range rows {
		if r.name == "feature" {
			featureLine = itemLine[i]
		}
	}
	if featureLine == -1 {
		t.Fatal("feature row not found")
	}
	if !strings.Contains(segsText(lines[featureLine+1]), "↳") {
		t.Errorf("feature's line after its row is %q, want a purpose subline", segsText(lines[featureLine+1]))
	}
}

// TestCleanUpSectionHoistsConstantColumns pins the acceptance case for issue #20
// against the real renderer (branchPaneLines), the same way
// TestCleanUpRowsDropTheirPurposeSubline pins the Clean Up gate against it. Two
// Clean Up rows agree on state ("merged") and the delete verdict ("ready"), so
// both are hoisted onto the Clean Up rule as a single chip and dropped from the
// rows; their owners differ, so the owner column survives on the rows exactly
// as before. A section holding only one row (secFixes here) hoists nothing at
// all, and its narrower name column proves the freed width in Clean Up actually
// went somewhere.
func TestCleanUpSectionHoistsConstantColumns(t *testing.T) {
	const w = 100
	m := seededModel()
	m.identity = config.Identity{Login: "me", Emails: []string{"me@example.com"}}
	m.local.branches = []git.BranchInfo{
		{Name: "alpha-done", HasRemote: true, LastCommit: "10 days ago"},
		{Name: "beta-done", HasRemote: true, LastCommit: "20 days ago"},
		{Name: "solo-fix", HasRemote: true, LastCommit: "2 days ago"},
	}
	m.local.firstAuthor = map[string]string{
		"alpha-done": "amy@example.com",
		"beta-done":  "chris@example.com",
		"solo-fix":   "me@example.com",
	}
	m.remote.MergedHeads = map[string]string{"alpha-done": "sha1", "beta-done": "sha2"}
	m.remote.PRs = []gh.OpenPR{
		{Number: 99, Title: "broken pr", HeadBranch: "solo-fix", Checks: gh.ChecksFailing, Bucket: gh.BucketMine},
	}

	rows := m.branchRows()
	byName := map[string]git.BranchInfo{}
	for _, b := range m.local.branches {
		byName[b.Name] = b
	}
	counts := map[section]int{}
	for _, r := range rows {
		counts[r.sec]++
	}
	if counts[secCleanup] != 2 {
		t.Fatalf("fixture setup: expected 2 Clean Up rows, got %d (rows=%+v)", counts[secCleanup], rows)
	}
	if counts[secFixes] != 1 {
		t.Fatalf("fixture setup: expected 1 Fixes Needed row, got %d (rows=%+v)", counts[secFixes], rows)
	}
	lines, itemLine := m.branchPaneLines(rows, byName, counts, w)

	cleanAt := -1
	for i, l := range lines {
		if strings.Contains(segsText(l), "Clean Up") {
			cleanAt = i
			break
		}
	}
	if cleanAt == -1 {
		t.Fatal("no Clean Up heading rendered")
	}
	heading := segsText(lines[cleanAt])
	if !strings.Contains(heading, "merged · ready") {
		t.Errorf("Clean Up rule missing the hoisted chip: %q", heading)
	}

	var alphaLine, betaLine, soloLine int = -1, -1, -1
	for i, r := range rows {
		switch r.name {
		case "alpha-done":
			alphaLine = itemLine[i]
		case "beta-done":
			betaLine = itemLine[i]
		case "solo-fix":
			soloLine = itemLine[i]
		}
	}
	if alphaLine == -1 || betaLine == -1 || soloLine == -1 {
		t.Fatalf("fixture rows not all found: alpha=%d beta=%d solo=%d", alphaLine, betaLine, soloLine)
	}

	// Neither hoisted word appears on either Clean Up row — they were dropped, not
	// just left unmentioned by coincidence.
	for _, ln := range []int{alphaLine, betaLine} {
		text := segsText(lines[ln])
		if strings.Contains(text, "merged") {
			t.Errorf("line %d still carries the hoisted state word: %q", ln, text)
		}
		if strings.Contains(text, "ready") {
			t.Errorf("line %d still carries the hoisted ready word: %q", ln, text)
		}
	}
	// The owners disagree, so that column must survive on both rows.
	if !strings.Contains(segsText(lines[alphaLine]), "amy") {
		t.Errorf("alpha-done row lost its owner column: %q", segsText(lines[alphaLine]))
	}
	if !strings.Contains(segsText(lines[betaLine]), "chris") {
		t.Errorf("beta-done row lost its owner column: %q", segsText(lines[betaLine]))
	}

	// solo-fix is alone in Fixes Needed — a section of one hoists nothing, so its
	// own verdict must still be printed on its row.
	if !strings.Contains(segsText(lines[soloLine]), "keep") {
		t.Errorf("solo-fix (a one-row section) lost its own ready column: %q", segsText(lines[soloLine]))
	}

	// The width Clean Up freed by hoisting has to go into ITS name column, wider
	// than the one-row Fixes Needed section gets at the same pane width.
	var cleanupRow, fixesRow paneRow
	for _, r := range rows {
		if r.name == "alpha-done" {
			cleanupRow = r
		}
		if r.name == "solo-fix" {
			fixesRow = r
		}
	}
	var cleanupRows, fixesRows []paneRow
	for _, r := range rows {
		if r.sec == secCleanup {
			cleanupRows = append(cleanupRows, r)
		}
		if r.sec == secFixes {
			fixesRows = append(fixesRows, r)
		}
	}
	cleanupPlan := planColumns(secCleanup, cleanupRows)
	fixesPlan := planColumns(secFixes, fixesRows)
	cleanupLayout := m.branchRowLayout(cleanupRow, w, cleanupPlan)
	fixesLayout := m.branchRowLayout(fixesRow, w, fixesPlan)
	if cleanupLayout.nameW <= fixesLayout.nameW {
		t.Errorf("Clean Up's name column is %d cells, want wider than Fixes Needed's %d at the same width",
			cleanupLayout.nameW, fixesLayout.nameW)
	}

	// Select mode must shrink BOTH sections' name column by exactly brSelectW —
	// the hoisting section (Clean Up) and the non-hoisting one (Fixes Needed)
	// alike, since gutterWidth(selectMode) is the one function both the row's
	// gutter segments and this width math read from. If the two disagreed, the
	// hoisting section's extra-wide name column would drift out of step with the
	// narrower one's the moment select mode turned on.
	m.selected = map[string]bool{selectableBranch(t, m): true}
	cleanupLayoutSel := m.branchRowLayout(cleanupRow, w, cleanupPlan)
	fixesLayoutSel := m.branchRowLayout(fixesRow, w, fixesPlan)
	if cleanupLayout.nameW-cleanupLayoutSel.nameW != brSelectW {
		t.Errorf("Clean Up (hoisting): select mode changed nameW by %d, want brSelectW=%d",
			cleanupLayout.nameW-cleanupLayoutSel.nameW, brSelectW)
	}
	if fixesLayout.nameW-fixesLayoutSel.nameW != brSelectW {
		t.Errorf("Fixes Needed (non-hoisting): select mode changed nameW by %d, want brSelectW=%d",
			fixesLayout.nameW-fixesLayoutSel.nameW, brSelectW)
	}
	// The two sections' relative ordering must survive select mode too — the
	// freed width still flows to Clean Up's already-wider name column, not just
	// to whichever section happens to render first.
	if cleanupLayoutSel.nameW <= fixesLayoutSel.nameW {
		t.Errorf("in select mode, Clean Up's name column is %d cells, want wider than Fixes Needed's %d",
			cleanupLayoutSel.nameW, fixesLayoutSel.nameW)
	}
	m.selected = map[string]bool{}
}

// TestBeadTreeKeepsFixedColumns is the table guarantee in tree mode: the indent
// lives inside the title cell, so id, priority, type, the status glyph, labels,
// assignee and age hold the same cells on a root and on a depth-2 child.
func TestBeadTreeKeepsFixedColumns(t *testing.T) {
	const cols = 140
	m := seededModel()
	m.width = cols
	m.haveBeads = true
	m.hasBeads = true
	m.beadTree = true
	m.beadPrefix = "bdtest"
	m.beads = []bd.Bead{
		{ID: "bdtest-38t", Title: "Auth token expiry off by one", Status: "open", Type: "bug", Priority: 0, Labels: []string{"backend", "security"}, Assignee: "pierce"},
		{ID: "bdtest-qt0", Title: "Add password reset", Status: "open", Type: "feature", Priority: 1, Labels: []string{"backend"}},
		{ID: "bdtest-1xn", Title: "Bump deps", Status: "in_progress", Type: "chore", Priority: 2, Labels: []string{"tooling"}, Assignee: "pierce"},
		{ID: "bdtest-isd", Title: "Wire reset email", Status: "open", Type: "task", Priority: 1, Blockers: []string{"bdtest-qt0"}},
		{ID: "bdtest-63h", Title: "Docs for reset", Status: "open", Type: "task", Priority: 2, Assignee: "alice", Blockers: []string{"bdtest-qt0", "bdtest-isd"}},
	}
	m.beadCols = []string{"id", "priority", "type", "glyph", "title", "labels", "owner", "age"}
	l := beadLayoutFor(m.beadCols, cols, m.beadCtx(true), m.beads)
	wID, wType := l.widths[l.indexOf("id")], l.widths[l.indexOf("type")]
	wGlyph := l.widths[l.indexOf("glyph")]
	wLabel, wOwner := l.widths[l.indexOf("labels")], l.widths[l.indexOf("owner")]
	if wGlyph != 1 {
		t.Fatalf("tree mode has no status glyph column: %d cells", wGlyph)
	}

	idAt := 2
	prioAt := idAt + wID + 2
	typeAt := prioAt + 2 + 2
	glyphAt := typeAt + wType + 2
	titleAt := glyphAt + wGlyph + 2
	ageAt := cols - beadAgeW
	assigneeAt := ageAt - 2 - wOwner
	labelAt := assigneeAt - 2 - wLabel

	want := map[string]struct{ id, prio, typ, glyph, title, labels, assignee string }{
		"bdtest-38t": {"38t", "P0", "bug", "", "Auth token expiry off by one", "backend, security", "pierce"},
		"bdtest-qt0": {"qt0", "P1", "feature", "", "Add password reset", "backend", ""},
		"bdtest-isd": {"isd", "P1", "task", "", "  └╴ Wire reset email", "", ""},
		"bdtest-63h": {"63h", "P2", "task", "", "    └╴ Docs for reset  +1", "", "alice"},
		"bdtest-1xn": {"1xn", "P2", "chore", "▶", "Bump deps", "tooling", "pierce"},
	}
	beadRows := rowsOfKind(m.beadRows(), rowBead)
	if len(beadRows) != len(m.beads) {
		t.Fatalf("rendered %d bead rows, want %d", len(beadRows), len(m.beads))
	}
	for _, r := range beadRows {
		text := []rune(segsText(composeLR(append([]seg{leadSeg(false)}, r.leftSegs...), r.rightSegs, cols)))
		if len(text) != cols {
			t.Fatalf("%s: row is %d cells wide, want %d", r.beadID, len(text), cols)
		}
		b := want[r.beadID]
		for _, c := range []struct{ name, got, want string }{
			{"id", string(text[idAt : idAt+wID]), b.id},
			{"priority", string(text[prioAt : prioAt+2]), b.prio},
			{"type", string(text[typeAt : typeAt+wType]), b.typ},
			{"glyph", string(text[glyphAt : glyphAt+wGlyph]), b.glyph},
			{"labels", string(text[labelAt : labelAt+wLabel]), b.labels},
			{"assignee", string(text[assigneeAt : assigneeAt+wOwner]), b.assignee},
		} {
			if strings.TrimSpace(c.got) != c.want {
				t.Errorf("%s: %s column holds %q, want %q — the columns have shifted", r.beadID, c.name, c.got, c.want)
			}
		}
		// The title cell is compared untrimmed: the indent is the assertion.
		if got := string(text[titleAt : titleAt+vlen(b.title)]); got != b.title {
			t.Errorf("%s: title cell holds %q, want %q", r.beadID, got, b.title)
		}
	}
}

// beadColumnFixture is the model the column-configuration tests measure. Every
// bead carries a label and an owner, so a column that drops is a column the
// fixture had something to put in.
func beadColumnFixture(cols int, names []string) Model {
	m := seededModel()
	m.width = cols
	m.haveBeads = true
	m.hasBeads = true
	m.beadPrefix = "bdtest"
	m.beadCols = names
	m.beads = []bd.Bead{
		{ID: "bdtest-38t", Title: "Auth token expiry off by one", Status: "open", Type: "bug", Priority: 0, Labels: []string{"backend", "security"}, Assignee: "pierce"},
		{ID: "bdtest-qt0", Title: "Add password reset", Status: "open", Type: "feature", Priority: 1, Labels: []string{"backend"}, Assignee: "alice"},
		{ID: "bdtest-1xn", Title: "Bump deps", Status: "in_progress", Type: "chore", Priority: 2, Labels: []string{"tooling"}, Assignee: "pierce"},
	}
	return m
}

// beadTitleAt is the cell the title column starts at, read off the layout the
// pane actually rendered with rather than off a constant, so the assertion moves
// when the configuration does.
func beadTitleAt(l beadLayout) int {
	at := 2
	for i, col := range l.cols {
		if col.side != beadLeft || col.name == "title" || l.widths[i] <= 0 {
			continue
		}
		at += l.widths[i] + 2
	}
	return at
}

// TestBeadColumnsDropOne is the point of making the columns configuration: a
// column left out of the list costs nothing, and its cells go to the title rather
// than being left as a hole where the column used to be.
func TestBeadColumnsDropOne(t *testing.T) {
	const cols = 140
	with := []string{"id", "priority", "type", "title", "labels", "owner", "age"}
	without := []string{"id", "priority", "type", "title", "age"}

	full := beadLayoutFor(with, cols, beadCtx{prefix: "bdtest"}, beadColumnFixture(cols, with).beads)
	lean := beadLayoutFor(without, cols, beadCtx{prefix: "bdtest"}, beadColumnFixture(cols, without).beads)

	dropped := full.widths[full.indexOf("labels")] + 2 + full.widths[full.indexOf("owner")] + 2
	if dropped <= 4 {
		t.Fatalf("fixture dropped no width: labels and owner were %d cells", dropped)
	}
	if got, want := lean.titleW(cols), full.titleW(cols)+dropped; got != want {
		t.Errorf("title is %d cells without labels and owner, want %d — the freed width went nowhere", got, want)
	}
	// The columns that stayed hold the same cells they held before, because the
	// width came out of the right-hand end, not out of the middle.
	if got, want := beadTitleAt(lean), beadTitleAt(full); got != want {
		t.Errorf("title starts at cell %d without labels and owner, want %d", got, want)
	}
	// And the age column is still the right-hand edge: what shifted left is the
	// boundary between the title and the right cluster, by exactly what was freed.
	if got, want := lean.rightW(lean.labelsW()), full.rightW(full.labelsW())-dropped; got != want {
		t.Errorf("right cluster is %d cells, want %d", got, want)
	}
}

// TestBeadColumnsAlwaysKeepTitle: a list that forgot the title still gets one. A
// pane of identifiers with no titles is never what a missing entry meant.
func TestBeadColumnsAlwaysKeepTitle(t *testing.T) {
	const cols = 140
	m := beadColumnFixture(cols, []string{"id", "age"})
	rows := rowsOfKind(m.beadRows(), rowBead)
	if len(rows) != len(m.beads) {
		t.Fatalf("rendered %d bead rows, want %d", len(rows), len(m.beads))
	}
	for _, r := range rows {
		text := segsText(composeLR(append([]seg{leadSeg(false)}, r.leftSegs...), r.rightSegs, cols))
		if !strings.Contains(text, r.beadTitle) {
			t.Errorf("%s row reads %q — it has no title", r.beadID, strings.TrimSpace(text))
		}
	}
}

// TestBeadColumnsIgnoreUnknown: a typo costs the column it names, not the pane.
func TestBeadColumnsIgnoreUnknown(t *testing.T) {
	got := beadColumnsFor([]string{"id", "nonsense", "title"})
	var names []string
	for _, c := range got {
		names = append(names, c.name)
	}
	if strings.Join(names, ",") != "id,title" {
		t.Errorf("resolved columns are %v, want [id title]", names)
	}
}

// TestNameCandidateRowsFormFixedColumns pins the `w` prompt's bead search as a
// table, modelled on TestBeadRowsFormFixedColumns: id, priority and type each
// hold the same cells on every candidate line whatever that row's own values
// are, exactly like the Beads pane itself — nameCandLines draws from the same
// beadLayoutFor machinery, so the two panes can never disagree about where a
// column sits.
func TestNameCandidateRowsFormFixedColumns(t *testing.T) {
	const cols = 140
	m := seededModel()
	m.width = cols
	m.beadPrefix = "bdtest"
	m.beads = []bd.Bead{
		{ID: "bdtest-38t", Title: "Auth token expiry off by one", Status: "open", Type: "bug", Priority: 0},
		{ID: "bdtest-qt0", Title: "Add password reset", Status: "open", Type: "feature", Priority: 1},
		{ID: "bdtest-1xn", Title: "Bump deps", Status: "in_progress", Type: "chore", Priority: 2},
	}
	m.naming = true
	m.nameCands = m.beads
	m.nameSel = 1

	names := []string{"id", "priority", "type", "title"}
	l := beadLayoutFor(names, cols, m.beadCtx(false), m.nameCands)
	wID, wType := l.widths[l.indexOf("id")], l.widths[l.indexOf("type")]

	// The lead (cursor/highlight) cell is prepended the same way renderRow
	// prepends it for every other list pane — without it every left-anchored
	// column would sit one cell early.
	idAt := 2
	prioAt := idAt + wID + 2
	typeAt := prioAt + 2 + 2

	want := map[string]struct{ id, prio, typ string }{
		"bdtest-38t": {"38t", "P0", "bug"},
		"bdtest-qt0": {"qt0", "P1", "feature"},
		"bdtest-1xn": {"1xn", "P2", "chore"},
	}
	for i, b := range m.beads {
		left, right := l.row(beadNode{b: b}, cols)
		line := append([]seg{leadSeg(i == m.nameSel)}, left...)
		text := []rune(segsText(fitSegs(composeLR(line, right, cols), cols)))
		if len(text) != cols {
			t.Fatalf("%s: row is %d cells wide, want %d", b.ID, len(text), cols)
		}
		w := want[b.ID]
		for _, c := range []struct{ name, got, want string }{
			{"id", string(text[idAt : idAt+wID]), w.id},
			{"priority", string(text[prioAt : prioAt+2]), w.prio},
			{"type", string(text[typeAt : typeAt+wType]), w.typ},
		} {
			if strings.TrimSpace(c.got) != c.want {
				t.Errorf("%s: %s column holds %q, want %q — the columns have shifted", b.ID, c.name, c.got, c.want)
			}
		}
	}

	lines := m.nameCandLines(cols)
	if len(lines) != len(m.beads) {
		t.Fatalf("nameCandLines returned %d lines, want %d", len(lines), len(m.beads))
	}
}

// A terminal whose font lacks the default marker has no way to tell us, so
// REPO_BRANCH_GLYPH is the only escape. Pin all three states: unset gives the
// Powerline icon, a value replaces it, and empty prints no marker at all.
func TestBranchGlyphHonoursTheOverride(t *testing.T) {
	for _, c := range []struct {
		name, set, want string
		unset           bool
	}{
		{name: "default", unset: true, want: defaultBranchGlyph + " "},
		{name: "override", set: "br", want: "br "},
		{name: "suppressed", set: "", want: ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("REPO_BRANCH_GLYPH", c.set) // t.Setenv restores after the test
			if c.unset {
				os.Unsetenv("REPO_BRANCH_GLYPH")
			}
			if got := branchGlyph(); got != c.want {
				t.Errorf("branchGlyph() = %q, want %q", got, c.want)
			}
		})
	}
}

// Suppressing the marker must not disturb the table: every PR row still fills
// exactly the pane width, branch or no branch, so the columns stay aligned.
func TestSuppressedBranchGlyphKeepsTheColumns(t *testing.T) {
	t.Setenv("REPO_BRANCH_GLYPH", "")
	const cols = 140
	m := seededModel()
	m.width = cols
	m.remote.PRs = []gh.OpenPR{
		{Number: 7, Title: "Has a branch", HeadBranch: "feature-branch-name", Bucket: gh.BucketMine},
		{Number: 22, Title: "No head branch", Bucket: gh.BucketMine},
	}
	if pc := m.prColumns(cols); pc.branchW == 0 {
		t.Fatalf("fixture produced no branch column: %+v", pc)
	}
	for _, r := range rowsOfKind(m.prRows(), rowPR) {
		if text := rowLine(r, cols); len(text) != cols {
			t.Errorf("#%d: row is %d cells wide, want %d", r.prNum, len(text), cols)
		}
	}
}
