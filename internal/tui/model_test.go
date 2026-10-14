package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/wthooks"
)

func seededModel() Model {
	m := New(context.Background(), "/repo", "owner/repo", "feature", "main")
	m.herdr = false // pin the non-herdr (quit-and-cd) path; New reads ambient HERDR_ENV
	// admin-70hb: New() now opens on the tab bar; this fixture's whole point is
	// asserting the body's own render and key handling, so pin focus there.
	m.focus = focusBody
	m.width, m.height = 120, 40
	// The default fixture is a GitHub-tracked, PR-using repo — the two keys
	// admin-3rgp.22 reads have to be declared explicitly now that the tab bar
	// no longer infers them from remote.PRs or a bd probe. Tests exercising a
	// different declaration (beads, none, PRs off) override this.
	m.manifest = &manifest.Manifest{
		Trackers: []string{"github"}, HasTracker: true,
		UsesPullRequests: true, HasUsesPullRequests: true,
	}
	m.local = localData{
		status: git.Status{Unstaged: 2},
		branches: []git.BranchInfo{
			{Name: "feature", Current: true, HasRemote: true, Ahead: 1, LastCommit: "1 hour ago"},
			{Name: "old-merged", HasRemote: true, LastCommit: "3 days ago"},
			{Name: "wt-branch", WorktreePath: "/repo/.worktrees/wt-branch", LastCommit: "yesterday"},
		},
		commits:      []git.Commit{{SHA: "abc123", Subject: "do a thing", When: "1 hour ago"}},
		stashes:      []git.Stash{{Ref: "stash@{0}", Subject: "wip"}},
		wts:          []git.Worktree{{Path: "/repo/.worktrees/wt-branch", Branch: "wt-branch"}},
		signals:      map[string]gitSignals{"feature": {ahead: true}},
		wtState:      map[string]wtState{},
		branchUnsafe: map[string]bool{},
	}
	m.haveLocal = true
	m.remote = data.Remote{
		PRs: []gh.OpenPR{
			{Number: 10, Title: "My change", HeadBranch: "feature", Checks: gh.ChecksPassing, Review: gh.ReviewApproved, Bucket: gh.BucketMine},
			{Number: 22, Title: "Their change", Author: "alice", Bucket: gh.BucketAssigned, MyReview: gh.NotReviewed},
		},
		ViewerLogin: "me",
		Issues: []gh.IssueItem{
			{Number: 5, Title: "Fix bug", Labels: []string{"bug"}, Assignees: []string{"me"}},
			{Number: 7, Title: "Add feature"},
		},
		MergedHeads: map[string]string{"old-merged": "abc123"},
	}
	m.haveData = true
	return m
}

func TestLocalViewRenders(t *testing.T) {
	m := seededModel()
	out := m.View()
	for _, want := range []string{"owner/repo", "feature", "Branches", "old-merged", "Commits", "Stashes"} {
		if !strings.Contains(out, want) {
			t.Errorf("Local view missing %q\n%s", want, out)
		}
	}
}

func TestPRsAndIssuesViewsRender(t *testing.T) {
	m := seededModel()
	m.active = tabPRs
	out := m.View()
	for _, want := range []string{"Mine", "#10", "Needs me", "#22", "alice"} {
		if !strings.Contains(out, want) {
			t.Errorf("PRs view missing %q\n%s", want, out)
		}
	}
	m.active = tabIssues
	out = m.View()
	for _, want := range []string{"Assigned to me", "#5", "Unassigned", "#7"} {
		if !strings.Contains(out, want) {
			t.Errorf("Issues view missing %q\n%s", want, out)
		}
	}
}

func TestHeaderPinnedAndHeightBounded(t *testing.T) {
	m := seededModel()
	m.active = tabIssues
	// Flood the Issues view with far more rows than fit on screen.
	var many []gh.IssueItem
	for i := 0; i < 60; i++ {
		many = append(many, gh.IssueItem{Number: 1000 + i, Title: "issue number " + strings.Repeat("x", 30)})
	}
	m.remote.Issues = many
	m.height, m.width = 20, 100

	check := func(label string) {
		out := m.View()
		lines := strings.Split(out, "\n")
		if len(lines) != m.height {
			t.Errorf("%s: got %d lines, want exactly %d", label, len(lines), m.height)
		}
		// Header (with the repo name) must be the first line, every time.
		if !strings.Contains(lines[0], "owner/repo") {
			t.Errorf("%s: header not pinned at top; line[0]=%q", label, lines[0])
		}
		// Footer keybindings must be on the last line.
		if !strings.Contains(lines[len(lines)-1], "quit") {
			t.Errorf("%s: footer not pinned at bottom; last=%q", label, lines[len(lines)-1])
		}
		// No rendered line may exceed the width.
		for i, l := range lines {
			if w := lipgloss.Width(l); w > m.width {
				t.Errorf("%s: line %d width %d exceeds %d: %q", label, i, w, m.width, l)
			}
		}
	}

	check("top of list")
	// Scroll to the bottom; header must still be pinned.
	for i := 0; i < 70; i++ {
		m.moveCursor(1)
	}
	check("bottom of list")
}

func TestCursorSkipsHeaders(t *testing.T) {
	m := seededModel()
	m.clampCursor()
	m.moveCursor(1) // from initial, must land on a selectable row
	r, ok := m.currentRow()
	if !ok {
		t.Fatal("no selectable row after move")
	}
	if r.kind == rowHeader || r.kind == rowText {
		t.Fatalf("cursor landed on non-selectable row: %+v", r)
	}
}

// Entering a list tab must leave the cursor on a selectable row: row 0 is a
// section header, so a cursor parked there makes enter/d/o silent no-ops until
// the user presses j once.
func TestTabEntryLandsOnSelectableRow(t *testing.T) {
	for _, tb := range []tab{tabPRs, tabIssues} {
		m := seededModel()
		m.switchTab(tb)
		if m.active != tb {
			t.Fatalf("tab %v is not drawn in the seeded model", tb)
		}
		r, ok := m.currentRow()
		if !ok {
			t.Fatalf("tab %v: no selectable row at cursor %d on entry", tb, m.cursor[tb])
		}
		if !r.selectable() {
			t.Fatalf("tab %v: cursor landed on non-selectable row: %+v", tb, r)
		}
	}
}

func TestCleanupQuarantinesDirtyWorktree(t *testing.T) {
	m := seededModel()
	// Mark the merged branch's would-be worktree dirty by adding a dirty merged worktree.
	m.local.wts = append(m.local.wts, git.Worktree{Path: "/repo/.worktrees/old-merged", Branch: "old-merged"})
	m.remote.MergedHeads["old-merged"] = "abc123"
	m.local.wtState["/repo/.worktrees/old-merged"] = wtState{dirty: true, changed: 2}
	// pathExists would be false for the fake path (→ pruned); stub statFn so the
	// path "exists" and the dirty worktree lands in quarantine instead.
	statFn = func(string) (os.FileInfo, error) { return nil, nil }
	defer func() { statFn = os.Stat }()

	// The dirty merged worktree must route to quarantine, never the safe bucket.
	set := m.cleanupTargets()
	if len(set.quarantine) != 1 {
		t.Fatalf("expected 1 quarantined target, got %d", len(set.quarantine))
	}
	for _, t2 := range set.mergedWts {
		if t2.path == "/repo/.worktrees/old-merged" {
			t.Errorf("dirty worktree leaked into the safe (mergedWts) bucket")
		}
	}

	// It renders in the Branches pane row itself saying what is actually at stake:
	// files that live only in that directory. There is no separate Cleanup box any
	// more — every deletable branch is marked in place.
	m.active, m.pane = tabLocal, paneBranches
	row := branchRow(t, m, "old-merged")
	text := segsText(row)
	if !strings.Contains(text, "~2") {
		t.Errorf("dirty merged worktree row is missing its changed-file count: %q", text)
	}
	if struckText(row) != "" {
		t.Errorf("dirty merged worktree is struck through as finished: %q", text)
	}
}

// branchRow renders one branch's row from the Branches panel, resolved exactly as
// renderLocalPanels resolves it.
func branchRow(t *testing.T, m Model, name string) []seg {
	t.Helper()
	return branchRowAt(t, m, name, 90)
}

// branchRowAt is branchRow at an explicit width, for the column-geometry tests.
// It computes the pane-wide columnPlan the same way branchPaneLines does, so the
// row matches the live renderer.
func branchRowAt(t *testing.T, m Model, name string, w int) []seg {
	t.Helper()
	rows := m.branchRows()
	for _, r := range rows {
		if r.name == name {
			return m.brPanelLine(r, m.branchInfo(name), w, planColumns(rows))
		}
	}
	t.Fatalf("no branch %q in fixture", name)
	return nil
}

// branchSection is the section the pane files a branch under.
func branchSection(t *testing.T, m Model, name string) section {
	t.Helper()
	for _, r := range m.branchRows() {
		if r.name == name {
			return r.sec
		}
	}
	t.Fatalf("no branch %q in fixture", name)
	return secMain
}

func segsText(segs []seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.t)
	}
	return b.String()
}

func struckText(segs []seg) string {
	var b strings.Builder
	for _, s := range segs {
		if s.strike {
			b.WriteString(s.t)
		}
	}
	return b.String()
}

// TestBranchRowsFormFixedColumns pins the Branches pane as a table. Every field
// right of the name has to occupy the same cells on every row of the SAME
// SECTION, whatever that row's owner, PR cell or git cell happens to be — laying
// the cluster out flush-right per row instead makes each value's position depend
// on the length of its neighbours, so nothing lines up and the list reads as a
// run of strings. The guarantee is per section, not per pane: see
// TestBranchRowsHoistConstantSectionColumns for the case where a section's
// column IS allowed to move, because every row in it agreed on it.
//
// It also pins the harder half: a value LONGER than its column is truncated rather
// than allowed to widen it. A cell that grows to fit shoves every column right of
// it out of alignment and, at the end of the row, straight out of the box into
// whatever is drawn beside it.
//
// Rows are built directly in Reviewing — the one section that draws the owner
// column — so all three right-hand columns are drawn.
func TestBranchRowsFormFixedColumns(t *testing.T) {
	const w = 90
	m := seededModel()
	branches := []git.BranchInfo{
		{Name: "short", HasRemote: true, LastCommit: "2 days ago"},
		{Name: "a-considerably-longer-branch-name", WorktreePath: "/repo/.worktrees/x", LastCommit: "6 weeks ago"},
		{Name: "even", HasRemote: true, LastCommit: "49 seconds ago"},
		{Name: "bare", LastCommit: "1 second ago"},
	}
	byName := map[string]git.BranchInfo{}
	for _, b := range branches {
		byName[b.Name] = b
	}
	draft := &gh.OpenPR{Number: 12, IsDraft: true, Checks: gh.ChecksPassing}
	rows := []paneRow{
		{name: "short", sec: secTheirs, owner: ownership{who: "amy"}, ahead: 4, behind: 123},
		{name: "a-considerably-longer-branch-name", sec: secTheirs, owner: ownership{who: "averyverylongpersonname"}, pr: draft, prNum: 12, changed: 2},
		{name: "even", sec: secTheirs, owner: ownership{who: "someone"}, pr: &gh.OpenPR{Number: 1234, Review: gh.ReviewApproved, Checks: gh.ChecksFailing}, prNum: 1234},
		{name: "bare", sec: secTheirs, owner: ownership{who: "casey"}}, // nothing in either cell
	}
	byRowName := map[string]paneRow{}
	for _, r := range rows {
		byRowName[r.name] = r
	}
	plan := planColumns(rows)
	if plan.drop["owner"] || plan.drop["pr"] || plan.drop["git"] {
		t.Fatalf("fixture setup: expected every column drawn, got drop=%v", plan.drop)
	}
	rowAt := func(name string) []seg {
		return m.brPanelLine(byRowName[name], byName[name], w, plan)
	}

	// Column boundaries, counted back from the right edge the way the row builds them.
	// Every column is followed by a one-cell gap, so a value that exactly fills its
	// width never butts against the next one.
	ageAt := w - brAgeW
	gitAt := ageAt - 1 - plan.gitW
	prAt := gitAt - 1 - plan.prW
	ownerAt := prAt - 1 - brOwnerW
	if plan.prW != vlen("#1234 ✗") || plan.gitW != vlen("↑4 ↓123") {
		t.Fatalf("plan sized pr/git to %d/%d, want the section's longest values", plan.prW, plan.gitW)
	}

	for _, r := range rows {
		text := []rune(segsText(rowAt(r.name)))
		if len(text) != w {
			t.Fatalf("%s: row is %d cells wide, want %d: %q", r.name, len(text), w, string(text))
		}
		for _, c := range []struct {
			name  string
			at, n int
			want  string
		}{
			{"owner", ownerAt, brOwnerW, ""},
			{"pr", prAt, plan.prW, prText(r)},
			{"git", gitAt, plan.gitW, gitText(r)},
			{"age", ageAt, brAgeW, ""},
		} {
			got := string(text[c.at : c.at+c.n])
			if c.at+c.n < w && string(text[c.at+c.n]) != " " {
				t.Errorf("%s: %s column is not followed by a gap: %q", r.name, c.name, string(text[c.at:min(w, c.at+c.n+1)]))
			}
			if c.want != "" && strings.TrimRight(got, " ") != c.want {
				t.Errorf("%s: %s column holds %q, want %q — the columns have shifted", r.name, c.name, got, c.want)
			}
		}
	}
	// Approved with failing checks: one mark, and the more urgent one wins.
	if got := prText(byRowName["even"]); got != "#1234 ✗" {
		t.Errorf("PR cell = %q, want the number and the single most urgent mark", got)
	}
	if got := prText(byRowName["a-considerably-longer-branch-name"]); got != "#12 ◌" {
		t.Errorf("draft PR cell = %q, want the number and the draft mark", got)
	}
	if got := gitText(byRowName["short"]); got != "↑4 ↓123" {
		t.Errorf("git cell = %q, want git-status notation with zero counts left out", got)
	}

	// The over-long owner must be truncated into its column, not allowed to widen it.
	owner := string([]rune(segsText(rowAt("a-considerably-longer-branch-name")))[ownerAt : ownerAt+brOwnerW])
	if !strings.Contains(owner, "…") {
		t.Errorf("an over-long owner was not truncated: %q — a cell that grows breaks every column right of it", owner)
	}
}

// TestColumnsAlignAcrossSections pins one geometry for the whole pane: the name,
// PR and git columns start at the same cell in every section, whatever each
// section's own longest values are. Sections that sized their own columns drew
// Continue Working's PR cell at a different offset from Waiting on Review's.
func TestColumnsAlignAcrossSections(t *testing.T) {
	const w = 120
	m := seededModel()
	rows := []paneRow{
		{name: "a-much-longer-branch-name-in-continue", sec: secContinue, ahead: 12, behind: 345, purpose: purpose{text: "work"}},
		{name: "short", sec: secWaiting, state: "in review", prNum: 7, purpose: purpose{text: "review"}},
		{name: "gone", sec: secCleanup, state: "merged", ready: "ready"},
	}
	plan := planColumns(rows)
	at := func(r paneRow, cell string) int {
		text := segsText(m.brPanelLine(r, git.BranchInfo{Name: r.name, LastCommit: "1 day ago"}, w, plan))
		if len([]rune(text)) != w {
			t.Fatalf("%s: row is %d cells, want %d", r.name, len([]rune(text)), w)
		}
		return len([]rune(text[:strings.Index(text, cell)]))
	}
	if a, b := at(rows[0], "work"), at(rows[1], "review"); a != b {
		t.Errorf("purpose starts at %d in Continue Working and %d in Waiting on Review — the name column differs", a, b)
	}
	if a, b := at(rows[0], "↑12"), at(rows[1], "#7")+plan.prW+1; a != b {
		t.Errorf("git cell starts at %d in Continue Working, %d in Waiting on Review", a, b)
	}
	if a, b := at(rows[1], "#7"), at(rows[2], "merged"); a != b {
		t.Errorf("PR cell starts at %d in Waiting on Review and %d in Clean Up", a, b)
	}
}

// TestBranchNameOutranksPurpose pins the name's priority over the purpose: the
// name column is sized to the section's longest name, and the purpose is drawn
// on the row only with what is left. When too little is left, the purpose moves
// to its own line rather than taking cells from the name.
func TestBranchNameOutranksPurpose(t *testing.T) {
	m := seededModel()
	long := "social-chess-l37.2-give-alertscontroller-its-own-clock"
	rows := []paneRow{
		{name: long, sec: secContinue, ahead: 1, purpose: purpose{text: "feat(client): give AlertsController its own clock"}},
		{name: "short", sec: secContinue, purpose: purpose{text: "something"}},
	}
	plan := planColumns(rows)
	if plan.nameW != len(long) {
		t.Fatalf("plan.nameW = %d, want the longest name, %d", plan.nameW, len(long))
	}
	wide := m.branchRowLayout(rows[1], 150, plan)
	if wide.nameW != len(long) || wide.purposeW < brPurposeMinW {
		t.Errorf("at 150 cells: name %d, purpose %d; want the whole longest name and an inline purpose", wide.nameW, wide.purposeW)
	}
	narrow := m.branchRowLayout(rows[0], 85, plan)
	if narrow.purposeW != 0 || narrow.nameW < len(long) {
		t.Errorf("at 85 cells: name %d, purpose %d; want the purpose on its own line and the name whole", narrow.nameW, narrow.purposeW)
	}
}

// TestBranchRowGutterCollapsesOutsideSelectMode pins the one-glyph gutter:
// outside select mode the row opens with a plain 2-cell gutter (no checkbox
// anywhere), a worktree-backed branch's glyph differs from a bare branch's,
// and an owned branch's glyph/color differs from an unowned one's \u2014 all while
// every row stays a fixed width. Select mode (space) is a SEPARATE test below,
// because it changes the gutter's width, not just its content.
func TestBranchRowGutterCollapsesOutsideSelectMode(t *testing.T) {
	const w = 90
	m := seededModel()
	m.identity = data.Identity{Login: "me", Email: "me@example.com"}
	m.local.branches = []git.BranchInfo{
		{Name: "main", Current: false, LastCommit: "1 hour ago"},
		{Name: "mine", HasRemote: true, LastCommit: "2 days ago"},
		{Name: "unclaimed", HasRemote: true, LastCommit: "5 days ago"},
		{Name: "has-wt", WorktreePath: "/repo/.worktrees/has-wt", LastCommit: "6 weeks ago"},
	}
	m.local.firstAuthor = map[string]string{"mine": "me@example.com"}

	if m.selectMode() {
		t.Fatalf("fixture setup: expected select mode to start off")
	}

	bare := branchRowAt(t, m, "mine", w)
	bareText := segsText(bare)
	if strings.Contains(bareText, "[") {
		t.Errorf("outside select mode the row still shows a checkbox: %q", bareText)
	}
	if strings.Contains(bareText, "\u25c7") {
		t.Errorf("a branch with no worktree shows the worktree glyph: %q", bareText)
	}

	withWt := branchRowAt(t, m, "has-wt", w)
	wtText := segsText(withWt)
	if !strings.Contains(wtText, "\u25c7") {
		t.Errorf("a branch with a worktree is missing the worktree glyph: %q", wtText)
	}

	mineGlyph := bare[0]
	unclaimedGlyph := branchRowAt(t, m, "unclaimed", w)[0]
	if mineGlyph.t == unclaimedGlyph.t && mineGlyph.fg == unclaimedGlyph.fg {
		t.Errorf("an owned branch's glyph is indistinguishable from an unowned one's: %+v vs %+v", mineGlyph, unclaimedGlyph)
	}

	for _, name := range []string{"main", "mine", "unclaimed", "has-wt"} {
		row := branchRowAt(t, m, name, w)
		if got := len([]rune(segsText(row))); got != w {
			t.Errorf("%s: row is %d cells, want %d", name, got, w)
		}
	}
}

// TestBranchRowSelectModeGrowsTheGutter pins select mode's effect on the same
// row geometry: one selected row anywhere in the pane opens every row \u2014 main
// included \u2014 with a 3-cell checkbox plus its gap, shifts every row's name
// column start by exactly brSelectW, and leaves every row's total width
// unchanged.
func TestBranchRowSelectModeGrowsTheGutter(t *testing.T) {
	const w = 90
	m := seededModel()
	m.local.branches = []git.BranchInfo{
		{Name: "main", Current: false, LastCommit: "1 hour ago"},
		{Name: "no-wt", HasRemote: true, LastCommit: "2 days ago"},
		{Name: "other", HasRemote: true, LastCommit: "3 days ago"},
	}

	before := branchRowAt(t, m, "no-wt", w)
	beforeText := segsText(before)

	// Select mode has no state beyond the selection, so a different row being
	// selected is what puts this one in select mode with an empty checkbox.
	m.selected = map[string]bool{"other": true}
	if !m.selectMode() {
		t.Fatalf("fixture setup: a nonempty selection did not enter select mode")
	}

	unselected := branchRowAt(t, m, "no-wt", w)
	uText := segsText(unselected)
	if !strings.HasPrefix(uText, "[ ] ") {
		t.Errorf("unselected row in select mode does not start with the blank checkbox: %q", uText)
	}

	m.selected["no-wt"] = true
	selected := branchRowAt(t, m, "no-wt", w)
	sText := segsText(selected)
	if !strings.HasPrefix(sText, "[x] ") {
		t.Errorf("selected row does not start with the checked box: %q", sText)
	}
	if len([]rune(sText)) != len([]rune(beforeText)) {
		t.Errorf("select mode changed the row's total width: %d vs %d", len([]rune(sText)), len([]rune(beforeText)))
	}

	// The default branch can't be selected or deleted, but select mode still
	// draws its blank checkbox \u2014 the gutter's width can't depend on this one
	// row's eligibility or the columns to its right would shift only on main.
	main := branchRowAt(t, m, "main", w)
	mText := segsText(main)
	if !strings.HasPrefix(mText, "    \u25c6 ") {
		t.Errorf("main row in select mode does not open with a blank checkbox before its glyph: %q", mText)
	}

	// The name column must lose exactly brSelectW cells of width in select mode —
	// the freed gutter width and the name column's width are computed from the
	// same gutterWidth(selectMode) call, so they can never disagree.
	var r paneRow
	for _, rr := range m.branchRows() {
		if rr.name == "no-wt" {
			r = rr
		}
	}
	plan := planColumns([]paneRow{r})
	m.selected = map[string]bool{}
	offLayout := m.branchRowLayout(r, w, plan)
	m.selected = map[string]bool{"other": true}
	onLayout := m.branchRowLayout(r, w, plan)
	// The flexible region is the name plus, when the section draws it inline, the
	// purpose column and the gap before it.
	flex := func(l branchRowLayout) int {
		if l.purposeW > 0 {
			return l.nameW + 1 + l.purposeW
		}
		return l.nameW
	}
	if flex(offLayout)-flex(onLayout) != brSelectW {
		t.Errorf("select mode changed the flexible width by %d cells, want brSelectW=%d", flex(offLayout)-flex(onLayout), brSelectW)
	}
}

// TestBranchRowNeverOverflowsAnyWidth is the property the last redesign round
// failed by eye: at every plausible pane width, every row and every purpose
// subline is exactly that many cells. A row one cell too long does not wrap, it
// overwrites the box border and the panel beside it.
func TestBranchRowNeverOverflowsAnyWidth(t *testing.T) {
	m := seededModel()
	m.identity = data.Identity{Login: "me", Email: "me@example.com"}
	// Swept both outside and inside select mode — the gutter's width changes
	// between the two, and that is exactly where the row and its width math
	// (gutterWidth) could disagree and overflow.
	for _, selecting := range []bool{false, true} {
		m.selected = map[string]bool{}
		if selecting {
			// Select mode is exactly "something is selected"; which row holds the
			// selection makes no difference to the width property.
			m.selected[selectableBranch(t, m)] = true
		}
		for w := 40; w <= 200; w += 7 {
			for _, r := range m.branchRows() {
				// A single row at a time, no section-wide plan — this test is the raw
				// width property, not hoisting, so it exercises the zero (no-drop, no-chip)
				// plan, exactly as a one-row section would.
				row := segsText(m.brPanelLine(r, m.branchInfo(r.name), w, columnPlan{}))
				if got := len([]rune(row)); got != w {
					t.Fatalf("selecting=%v width %d: row %q is %d cells: %q", selecting, w, r.name, got, row)
				}
				if sub := m.brSubLine(r, w, columnPlan{}); sub != nil {
					if got := len([]rune(segsText(sub))); got > w {
						t.Fatalf("selecting=%v width %d: subline for %q is %d cells: %q", selecting, w, r.name, got, segsText(sub))
					}
				}
				if got := len([]rune(segsText(brSectionLine(r.sec, 3, w, nil)))); got > w {
					t.Fatalf("selecting=%v width %d: heading for section %v is %d cells", selecting, w, r.sec, got)
				}
			}
		}
	}
}

// selectableBranch is the first branch row the multi-select would actually
// accept — anything outside the protected main section.
func selectableBranch(t *testing.T, m Model) string {
	t.Helper()
	for _, r := range m.branchRows() {
		if r.sec != secMain {
			return r.name
		}
	}
	t.Fatalf("fixture has no selectable branch row")
	return ""
}

// TestBranchRowMarksDeletable checks a merged branch is struck through in the
// Branches list and carries its reason, while a live branch is left alone.
func TestBranchRowMarksDeletable(t *testing.T) {
	m := seededModel()

	dead := branchRow(t, m, "old-merged")
	if !strings.Contains(struckText(dead), "old-merged") {
		t.Errorf("merged branch name not struck through: %q", segsText(dead))
	}
	if !strings.Contains(segsText(dead), "merged") {
		t.Errorf("merged branch row missing its reason: %q", segsText(dead))
	}

	live := branchRow(t, m, "feature")
	if s := struckText(live); s != "" {
		t.Errorf("current branch struck through: %q", s)
	}
}

// TestBranchRowNeverStrikesHeld checks a deletable branch holding uncommitted work
// is flagged but never struck — it must not read as finished when removing its
// worktree would destroy files that exist nowhere else.
func TestBranchRowNeverStrikesHeld(t *testing.T) {
	m := seededModel()
	m.local.wtState["/repo/.worktrees/old-merged"] = wtState{dirty: true, changed: 3}
	for i := range m.local.branches {
		if m.local.branches[i].Name == "old-merged" {
			m.local.branches[i].WorktreePath = "/repo/.worktrees/old-merged"
		}
	}

	row := branchRow(t, m, "old-merged")
	if s := struckText(row); s != "" {
		t.Errorf("held branch struck through: %q", s)
	}
	if !strings.Contains(segsText(row), "~3") {
		t.Errorf("held branch row missing its changed-file count: %q", segsText(row))
	}
}

// TestProvenLandedIsNotAlsoUnpushed pins the one place a git fact is deliberately
// not repeated. A squash-merged branch whose remote copy was deleted holds commits
// that are on no other local ref, so the unpushed check reports true — but the
// merge is proof the work landed, and flagging it as unsaved would put every
// merged branch behind a warning the merge itself already answered.
func TestProvenLandedIsNotAlsoUnpushed(t *testing.T) {
	m := seededModel()
	m.local.branchUnsafe["old-merged"] = true

	for _, r := range m.branchRows() {
		if r.name == "old-merged" && r.ready != "ready" {
			t.Errorf("a proven-merged branch is not sweepable: ready = %q, holds = %v", r.ready, r.holds)
		}
	}
}

// TestDeadReasonSignals checks every safe-to-drop signal fires, in priority
// order, and that a merged PR whose branch gained new local commits stays live.
func TestDeadReasonSignals(t *testing.T) {
	m := seededModel()
	m.local.signals = map[string]gitSignals{
		"reachable":        {mergedLocal: true},
		"pushed":           {fullyPushed: true},
		"deleted-upstream": {gone: true},
		"old-merged":       {gone: true, fullyPushed: true},
		"reworked":         {ahead: true},
	}
	m.remote.MergedHeads["reworked"] = "sha-reworked"

	for _, tc := range []struct{ branch, want string }{
		{"deleted-upstream", "gone"},
		{"old-merged", "gone"}, // gone wins over the other signals it also matches
		{"reachable", "merged"},
		{"pushed", "on origin"},
		{"reworked", ""}, // merged PR, but local commits landed since
		{"feature", ""},
	} {
		if got := m.deadReason(tc.branch); got != tc.want {
			t.Errorf("deadReason(%q) = %q, want %q", tc.branch, got, tc.want)
		}
	}
}

// TestSupersededBranchClassifiesInstantly pins the superseded probe: an empty or
// fully-pushed local branch whose issue already has someone else's PR — an open one
// still linked to the issue, or a merged one that closed the issue and dropped it
// out of the open-issue cache — is dead the moment that PR appears, with no
// staleness clock. A branch with unpushed commits stays live, the viewer's own PR
// is not a takeover, a PR from the viewer's own pushed branch is not a takeover, and
// the viewer's own merged PR reads as "merged" rather than "superseded".
func TestSupersededBranchClassifiesInstantly(t *testing.T) {
	m := seededModel()
	m.local.signals = map[string]gitSignals{
		"14-open-other":   {empty: true},
		"20-merged-other": {fullyPushed: true},
		"30-ahead":        {ahead: true},
		"40-my-open-pr":   {empty: true},
		"50-my-head":      {fullyPushed: true},
		"60-i-merged-it":  {fullyPushed: true},
	}
	m.remote.ViewerLogin = "me"
	m.remote.MergedHeads["60-i-merged-it"] = "sha-60"
	m.remote.Issues = []gh.IssueItem{
		{Number: 14, Title: "Open, taken by alice", PR: &gh.IssuePR{Number: 101}},
		{Number: 30, Title: "Ahead but taken", PR: &gh.IssuePR{Number: 103}},
		{Number: 40, Title: "My own PR", PR: &gh.IssuePR{Number: 104}},
		{Number: 50, Title: "PR from my own branch", PR: &gh.IssuePR{Number: 105}},
		{Number: 60, Title: "I landed it, alice also has one", PR: &gh.IssuePR{Number: 106}},
	}
	m.remote.PRs = []gh.OpenPR{
		{Number: 101, Author: "alice", HeadBranch: "alice/fix-14"},
		{Number: 103, Author: "alice", HeadBranch: "alice/fix-30"},
		{Number: 104, Author: "me", HeadBranch: "me/fix-40"},
		{Number: 105, Author: "alice", HeadBranch: "50-my-head"},
		{Number: 106, Author: "alice", HeadBranch: "alice/fix-60"},
	}
	m.remote.MergedByIssue = map[int][]data.MergedPRRef{
		20: {{Author: "alice", HeadBranch: "alice/fix-20"}},
	}

	for _, tc := range []struct{ branch, want string }{
		{"14-open-other", "superseded"},   // someone else's open PR on my open issue
		{"20-merged-other", "superseded"}, // someone else's merged PR; the issue has since closed
		{"30-ahead", ""},                  // taken, but I have unpushed commits: still live
		{"40-my-open-pr", "empty"},        // my own PR isn't a takeover
		{"50-my-head", "on origin"},       // the PR's head is my own pushed branch
		{"60-i-merged-it", "merged"},      // my merged PR outranks alice's open one
	} {
		if got := m.deadReason(tc.branch); got != tc.want {
			t.Errorf("deadReason(%q) = %q, want %q", tc.branch, got, tc.want)
		}
	}

	// Without a known viewer login, "someone else" is unprovable — the probe stays
	// off rather than sweep the branch on what might be the viewer's own PR.
	m.remote.ViewerLogin = ""
	if got := m.deadReason("14-open-other"); got != "empty" {
		t.Errorf("with no viewer login, deadReason(14-open-other) = %q, want %q", got, "empty")
	}
}

// TestMergedPRsSurviveALocalReload pins the merged-PR set against the local-git
// reload that used to wipe it. The set arrives from GitHub on its own schedule
// while local git is re-read on every fetch --prune and after every mutation; when
// it lived inside localData, the next reload replaced the whole struct and blanked
// it — and since the gh call fires once per session, nothing refilled it. Every
// squash-merged branch then read as live for the rest of the session.
func TestMergedPRsSurviveALocalReload(t *testing.T) {
	m := seededModel()
	if got := m.deadReason("old-merged"); got != "merged" {
		t.Fatalf("precondition: deadReason(old-merged) = %q, want %q", got, "merged")
	}

	// What loadLocalCmd delivers: a localData built from scratch out of git alone,
	// carrying nothing GitHub-derived.
	reloaded := localData{
		branches:     m.local.branches,
		wts:          m.local.wts,
		signals:      map[string]gitSignals{},
		wtState:      map[string]wtState{},
		branchUnsafe: map[string]bool{},
	}
	updated, _ := m.Update(localDataMsg{d: reloaded})
	m = updated.(Model)

	if got := m.deadReason("old-merged"); got != "merged" {
		t.Errorf("deadReason(old-merged) = %q after a local reload, want %q — the merged-PR set was blanked", got, "merged")
	}
}

// TestClassificationIsOrderIndependent pins the structural property the whole
// classification layer exists for: git and GitHub answer on their own schedules,
// and whichever lands second must not erase what the first said. Both arrival
// orders have to classify every branch identically. A classification stored in a
// struct one of the two sources replaces wholesale cannot pass this.
func TestClassificationIsOrderIndependent(t *testing.T) {
	branches := []git.BranchInfo{
		{Name: "squash-merged", HasRemote: true, LastCommit: "3 days ago"},
		{Name: "locally-merged", LastCommit: "4 days ago"},
		{Name: "deleted-upstream", LastCommit: "5 days ago"},
		{Name: "live", HasRemote: true, Ahead: 2, LastCommit: "1 hour ago"},
	}
	local := localDataMsg{d: localData{
		branches: branches,
		signals: map[string]gitSignals{
			"squash-merged":    {},
			"locally-merged":   {mergedLocal: true},
			"deleted-upstream": {gone: true},
			"live":             {ahead: true},
		},
		wtState:      map[string]wtState{},
		branchUnsafe: map[string]bool{},
	}}
	merged := syncDoneMsg{r: data.Remote{
		MergedHeads:     map[string]string{"squash-merged": "sha-sq", "live": "sha-live"},
		MergedFetchedAt: time.Now(),
	}}

	reasonsAfter := func(first, second tea.Msg) map[string]string {
		m := New(context.Background(), "/repo", "owner/repo", "live", "main")
		updated, _ := m.Update(first)
		updated, _ = updated.(Model).Update(second)
		m = updated.(Model)
		out := map[string]string{}
		for _, br := range branches {
			out[br.Name] = m.deadReason(br.Name)
		}
		return out
	}

	gitFirst := reasonsAfter(local, merged)
	ghFirst := reasonsAfter(merged, local)

	want := map[string]string{
		"squash-merged":    "merged", // only GitHub can see this one
		"locally-merged":   "merged", // only git can see this one
		"deleted-upstream": "gone",
		"live":             "", // merged PR, but local commits landed since
	}
	for _, br := range branches {
		if gitFirst[br.Name] != want[br.Name] {
			t.Errorf("git-then-GitHub: deadReason(%q) = %q, want %q", br.Name, gitFirst[br.Name], want[br.Name])
		}
		if ghFirst[br.Name] != gitFirst[br.Name] {
			t.Errorf("deadReason(%q) = %q when GitHub landed first but %q when git did — one source erased the other",
				br.Name, ghFirst[br.Name], gitFirst[br.Name])
		}
	}
}

// TestDefaultAndCheckedOutBranchesAreNeverDead pins the guard that keeps `main`
// out of the cleanup set. `git branch --merged origin/main` lists main itself, and
// a checked-out branch is routinely fully-pushed — so both signals fire on it. The
// br.Current check alone doesn't cover this: launched from a linked worktree, the
// current branch is the worktree's, and main sits in the main working tree.
func TestDefaultAndCheckedOutBranchesAreNeverDead(t *testing.T) {
	m := seededModel()
	// Simulate launching from a linked worktree: current is wt-branch, main is held
	// by the main working tree at repoRoot.
	m.current = "wt-branch"
	m.local.branches = []git.BranchInfo{
		{Name: "main", HasRemote: true, LastCommit: "1 hour ago"},
		{Name: "wt-branch", Current: true, WorktreePath: "/repo/.worktrees/wt-branch", LastCommit: "yesterday"},
	}
	m.local.wts = []git.Worktree{
		{Path: "/repo", Branch: "main"},
		{Path: "/repo/.worktrees/wt-branch", Branch: "wt-branch"},
	}
	m.local.signals = map[string]gitSignals{
		"main":      {mergedLocal: true, fullyPushed: true},
		"wt-branch": {mergedLocal: true},
	}

	if got := m.deadReason("main"); got != "" {
		t.Errorf("default branch classified dead as %q — it would be struck through and swept by ⇧X", got)
	}
	if got := m.deadReason("wt-branch"); got == "" {
		t.Error("a linked worktree's branch should still be classifiable; only the main tree's is exempt")
	}
	for _, tgt := range m.cleanupTargets().all() {
		if tgt.branch == "main" {
			t.Errorf("main reached the cleanup set as %+v", tgt)
		}
	}
}

// TestCleanupFindsLocallyMergedAndPushedBranches checks the two signals repo-dash
// previously lacked actually produce cleanup targets — a branch reachable from the
// default branch, and one whose commits all exist on origin.
func TestCleanupFindsLocallyMergedAndPushedBranches(t *testing.T) {
	m := seededModel()
	m.local.branches = append(m.local.branches,
		git.BranchInfo{Name: "reachable", HasRemote: true, LastCommit: "2 days ago"},
		git.BranchInfo{Name: "pushed", HasRemote: true, LastCommit: "4 days ago"},
	)
	m.local.signals = map[string]gitSignals{
		"reachable": {mergedLocal: true},
		"pushed":    {fullyPushed: true},
	}

	got := map[string]string{}
	for _, tgt := range m.cleanupTargets().all() {
		got[tgt.branch] = tgt.reason
	}
	if got["reachable"] != "merged" {
		t.Errorf("branch merged into the default branch not a cleanup target (reason %q)", got["reachable"])
	}
	if got["pushed"] != "on origin" {
		t.Errorf("fully-pushed branch not a cleanup target (reason %q)", got["pushed"])
	}
}

// errStubbed short-circuits a stubbed delete step before it reaches real git.
var errStubbed = errors.New("stubbed")

// stageBranchDelete puts the Branches cursor on a branch and presses x, returning
// the resulting model.
func stageBranchDelete(t *testing.T, m Model, branch string) Model {
	t.Helper()
	// Addressed through branchRows, the same ordering the pane draws and the delete
	// path reads — positions in localPanes no longer correspond to rows.
	m.active, m.pane = tabLocal, paneBranches
	found := false
	for i, r := range m.branchRows() {
		if r.name == branch {
			m.paneCursor[paneBranches], found = i, true
		}
	}
	if !found {
		t.Fatalf("branch %q is not in the pane's rows", branch)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	return updated.(Model)
}

// TestBranchDeleteTakesItsWorktree checks x on the Branches pane stages the branch
// together with the worktree holding it, so the worktree is removed before the ref.
func TestBranchDeleteTakesItsWorktree(t *testing.T) {
	m := stageBranchDelete(t, seededModel(), "wt-branch")
	if !m.confirming || len(m.confirmTargets) != 1 {
		t.Fatalf("expected 1 staged target, got confirming=%v targets=%+v", m.confirming, m.confirmTargets)
	}
	got := m.confirmTargets[0]
	if got.kind != cleanupWorktree {
		t.Errorf("kind = %v, want cleanupWorktree — the worktree must go first or the branch delete fails", got.kind)
	}
	if got.path != "/repo/.worktrees/wt-branch" {
		t.Errorf("path = %q, want the worktree path", got.path)
	}
}

// TestBranchDeleteWithoutWorktree checks a bare branch stages as a branch delete.
func TestBranchDeleteWithoutWorktree(t *testing.T) {
	m := stageBranchDelete(t, seededModel(), "old-merged")
	if len(m.confirmTargets) != 1 {
		t.Fatalf("expected 1 staged target, got %+v", m.confirmTargets)
	}
	if got := m.confirmTargets[0]; got.kind != cleanupBranch || got.path != "" {
		t.Errorf("kind = %v path = %q, want cleanupBranch with no path", got.kind, got.path)
	}
}

// TestBranchDeleteForcesAPausedRebase pins that `x` on a mid-rebase worktree
// actually clears it. A plain `git worktree remove` refuses a worktree with a
// rebase stopped in it, so the staged target has to carry the paused op: without
// it the delete goes out unforced and fails on exactly the worktree most in need
// of clearing — and the row keeps sitting there through every attempt.
func TestBranchDeleteForcesAPausedRebase(t *testing.T) {
	m := seededModel()
	const branch, path = "stuck-rebase", "/repo/.worktrees/stuck-rebase"
	m.local.branches = append(m.local.branches, git.BranchInfo{Name: branch, WorktreePath: path, LastCommit: "2 weeks ago"})
	m.local.wts = append(m.local.wts, git.Worktree{Path: path, Branch: branch, Op: "rebase"})

	m = stageBranchDelete(t, m, branch)
	if len(m.confirmTargets) != 1 {
		t.Fatalf("mid-rebase branch did not stage: %+v", m.confirmTargets)
	}
	tgt := m.confirmTargets[0]
	if tgt.op != "rebase" {
		t.Errorf("staged op = %q, want %q", tgt.op, "rebase")
	}
	if !tgt.quarantined {
		t.Error("mid-rebase worktree staged unquarantined — the removal would run without --force and git would refuse it")
	}
	if got := cleanupReason(tgt); got != "rebasing" {
		t.Errorf("label = %q, want %q", got, "rebasing")
	}

	// And the removal it dispatches actually forces.
	var gotForce, called bool
	restore := removeWorktreeFn
	removeWorktreeFn = func(_ context.Context, _, _ string, force bool) error {
		called, gotForce = true, force
		return errStubbed
	}
	defer func() { removeWorktreeFn = restore }()
	deleteStepCmd(context.Background(), t.TempDir(), m.confirmTargets, 0, false)()
	if !called || !gotForce {
		t.Errorf("removal called=%v force=%v, want called with force", called, gotForce)
	}
}

// TestBranchDeleteAllowsLiveBranches checks the Branches pane is not limited to
// dead branches — a live branch with unpushed work stages, flagged quarantined so
// the confirm modal marks it held.
func TestBranchDeleteAllowsLiveBranches(t *testing.T) {
	m := seededModel()
	m.local.branches = append(m.local.branches,
		git.BranchInfo{Name: "live-work", HasRemote: true, Ahead: 3, LastCommit: "1 hour ago"})
	m.local.branchUnsafe["live-work"] = true

	m = stageBranchDelete(t, m, "live-work")
	if len(m.confirmTargets) != 1 {
		t.Fatalf("live branch did not stage: %+v", m.confirmTargets)
	}
	if got := m.confirmTargets[0]; !got.quarantined || got.reason != "" {
		t.Errorf("live branch staged as quarantined=%v reason=%q, want quarantined with no dead reason", got.quarantined, got.reason)
	}
}

// TestBranchDeleteRefusesCurrentAndDefault checks the two branches git itself
// cannot delete are refused before the modal opens.
func TestBranchDeleteRefusesCurrentAndDefault(t *testing.T) {
	for _, branch := range []string{"feature", "main"} {
		m := seededModel()
		m.local.branches = append(m.local.branches, git.BranchInfo{Name: "main", LastCommit: "1 day ago"})
		m = stageBranchDelete(t, m, branch)
		if m.confirming {
			t.Errorf("%q staged for deletion; want refused", branch)
		}
	}
}

// freshWorktreeModel seeds the situation that lost real work: a branch created off
// main minutes ago, never committed to, with a worktree an agent is working in. To
// git it is indistinguishable from leftovers — zero commits, reachable from
// origin/main — so it classified as merged and `⇧X` deleted the tree.
func freshWorktreeModel(t *testing.T, touched time.Time, herdrOpen bool) Model {
	t.Helper()
	m := seededModel()
	const branch, path = "1378-fresh-work", "/repo/.worktrees/1378-fresh-work"
	m.local.branches = append(m.local.branches, git.BranchInfo{Name: branch, WorktreePath: path, LastCommit: "14 hours ago"})
	m.local.wts = append(m.local.wts, git.Worktree{Path: path, Branch: branch})
	m.local.signals = map[string]gitSignals{branch: {mergedLocal: true, empty: true}}
	m.local.touched = map[string]time.Time{branch: touched}
	m.local.herdrOpen = map[string]string{}
	if herdrOpen {
		m.local.herdrOpen[path] = "w1"
	}
	statFn = func(string) (os.FileInfo, error) { return nil, nil }
	t.Cleanup(func() { statFn = os.Stat })
	return m
}

// targetFor finds a branch's cleanup target and which bucket it landed in.
func targetFor(t *testing.T, m Model, branch string) (cleanupTarget, string) {
	t.Helper()
	set := m.cleanupTargets()
	for _, b := range []struct {
		name    string
		targets []cleanupTarget
	}{{"mergedWts", set.mergedWts}, {"mergedBranches", set.mergedBranches}, {"quarantine", set.quarantine}} {
		for _, tgt := range b.targets {
			if tgt.branch == branch {
				return tgt, b.name
			}
		}
	}
	t.Fatalf("no cleanup target for %q", branch)
	return cleanupTarget{}, ""
}

// TestFreshEmptyWorktreeIsNotSwept pins the fix for the sweep that deleted a
// worktree an agent was working in. The branch is still deletable — there are no
// commits on it — but it is quarantined as `active`, so the batch sweep passes it
// over and removing it takes a deliberate pick.
func TestFreshEmptyWorktreeIsNotSwept(t *testing.T) {
	m := freshWorktreeModel(t, time.Now().Add(-10*time.Minute), false)

	tgt, bucket := targetFor(t, m, "1378-fresh-work")
	if bucket != "quarantine" {
		t.Errorf("a worktree touched 10 minutes ago landed in %s — ⇧X would delete it", bucket)
	}
	if tgt.reason != "empty" {
		t.Errorf("reason = %q, want %q — a branch nothing ever landed on did not merge", tgt.reason, "empty")
	}
	if got := cleanupReason(tgt); got != "active" {
		t.Errorf("label = %q, want %q", got, "active")
	}

	// The branch row must not read as dead while something is working in it.
	if s := struckText(branchRow(t, m, "1378-fresh-work")); s != "" {
		t.Errorf("active branch struck through: %q", s)
	}
}

// TestStaleEmptyBranchIsSwept is the other half: the branch you made, thought
// better of, and forgot. Once it has sat untouched past staleAfter it sweeps —
// reported as `empty`, not `merged`, so the list says nothing ever happened on it
// rather than implying its work landed.
func TestStaleEmptyBranchIsSwept(t *testing.T) {
	m := freshWorktreeModel(t, time.Now().Add(-30*24*time.Hour), false)

	tgt, bucket := targetFor(t, m, "1378-fresh-work")
	if bucket == "quarantine" {
		t.Error("a month-old untouched branch was quarantined; it is exactly the cruft cleanup exists to find")
	}
	if got := cleanupReason(tgt); got != "empty" {
		t.Errorf("label = %q, want %q", got, "empty")
	}
	if !strings.Contains(struckText(branchRow(t, m, "1378-fresh-work")), "1378-fresh-work") {
		t.Error("stale empty branch not struck through in the branch list")
	}
}

// TestHerdrOpenWorktreeIsNeverSwept checks the strongest probe stands alone: a
// workspace open on the tree means something is in it now, whatever the
// timestamps say.
func TestHerdrOpenWorktreeIsNeverSwept(t *testing.T) {
	m := freshWorktreeModel(t, time.Now().Add(-30*24*time.Hour), true)

	tgt, bucket := targetFor(t, m, "1378-fresh-work")
	if bucket != "quarantine" {
		t.Errorf("a worktree with a herdr workspace open on it landed in %s", bucket)
	}
	if got := cleanupReason(tgt); got != "active" {
		t.Errorf("label = %q, want %q", got, "active")
	}
}

// TestQuarantinedWorktreeDeletesWithForce pins that a quarantined worktree removal
// forces: a plain `git worktree remove` refuses a dirty worktree, which every
// quarantined one is, so without force the delete could only ever fail.
func TestQuarantinedWorktreeDeletesWithForce(t *testing.T) {
	var gotForce, called bool
	restore := removeWorktreeFn
	// Return an error so the step stops here and never runs a real `git branch -D`.
	removeWorktreeFn = func(_ context.Context, _, _ string, force bool) error {
		called, gotForce = true, force
		return errStubbed
	}
	defer func() { removeWorktreeFn = restore }()

	targets := []cleanupTarget{{
		id: "wt:/repo/.worktrees/x", kind: cleanupWorktree,
		branch: "x", path: "/repo/.worktrees/x", quarantined: true,
	}}
	deleteStepCmd(context.Background(), t.TempDir(), targets, 0, false)()
	if !called {
		t.Fatal("worktree removal never ran")
	}
	if !gotForce {
		t.Error("quarantined worktree removed without --force; git would refuse it")
	}
}

// A pre_remove veto (ADR-0024) stops the delete step before the stash and the
// removal, its error is the row's reason, and it is a refusal the `f` retry
// re-offers — which passes force to the hook. Quarantine alone does not.
func TestPreRemoveVetoStopsDeleteStep(t *testing.T) {
	var gotForce bool
	restorePre := preRemoveFn
	preRemoveFn = func(_ context.Context, _, _ string, force bool) error {
		gotForce = force
		if force {
			return nil
		}
		return &wthooks.Error{Hook: wthooks.HookPreRemove, Exit: 1, Output: "draining\nstill serving"}
	}
	defer func() { preRemoveFn = restorePre }()
	var touched []string
	restoreStash := stashWorktreeFn
	stashWorktreeFn = func(_ context.Context, _ string) error { touched = append(touched, "stash"); return nil }
	defer func() { stashWorktreeFn = restoreStash }()
	restoreRemove := removeWorktreeFn
	removeWorktreeFn = func(_ context.Context, _, _ string, _ bool) error {
		touched = append(touched, "remove")
		return errStubbed
	}
	defer func() { removeWorktreeFn = restoreRemove }()

	targets := []cleanupTarget{{
		id: "wt:/repo/.worktrees/x", kind: cleanupWorktree,
		branch: "x", path: "/repo/.worktrees/x", dirty: true, quarantined: true,
	}}
	msg := deleteStepCmd(context.Background(), t.TempDir(), targets, 0, false)().(deleteStepMsg)
	if got := deleteErrLine(msg.err); got != "pre_remove hook failed (exit 1): still serving" {
		t.Errorf("row reason = %q, want the hook's one-line veto", got)
	}
	if !isBlocked(msg.err) {
		t.Error("veto not re-offered under f")
	}
	if len(touched) != 0 {
		t.Errorf("veto still ran %v", touched)
	}
	if gotForce {
		t.Error("a quarantined first delete passed force to pre_remove")
	}

	deleteStepCmd(context.Background(), t.TempDir(), targets, 0, true)()
	if !gotForce {
		t.Error("the f retry did not pass force to pre_remove")
	}
	if want := []string{"stash", "remove"}; strings.Join(touched, ",") != strings.Join(want, ",") {
		t.Errorf("forced retry ran %v, want %v", touched, want)
	}
}

// TestForcedDirtyWorktreeStashesBeforeRemoval pins that a dirty worktree's
// uncommitted files are stashed before the forced removal that would otherwise
// destroy them, and that the resulting stash@{n} rides back on the step message.
func TestForcedDirtyWorktreeStashesBeforeRemoval(t *testing.T) {
	var order []string
	restoreStash := stashWorktreeFn
	stashWorktreeFn = func(_ context.Context, path string) error {
		order = append(order, "stash:"+path)
		return nil
	}
	defer func() { stashWorktreeFn = restoreStash }()

	restoreRemove := removeWorktreeFn
	// Error out so the step stops here and never runs a real `git branch -D`.
	removeWorktreeFn = func(_ context.Context, _, path string, _ bool) error {
		order = append(order, "remove:"+path)
		return errStubbed
	}
	defer func() { removeWorktreeFn = restoreRemove }()

	targets := []cleanupTarget{{
		id: "wt:/repo/.worktrees/x", kind: cleanupWorktree,
		branch: "x", path: "/repo/.worktrees/x", dirty: true, quarantined: true,
	}}
	msg, ok := deleteStepCmd(context.Background(), t.TempDir(), targets, 0, false)().(deleteStepMsg)
	if !ok {
		t.Fatal("expected a deleteStepMsg")
	}
	want := []string{"stash:/repo/.worktrees/x", "remove:/repo/.worktrees/x"}
	if len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
		t.Errorf("call order = %v, want %v (the stash must precede the removal)", order, want)
	}
	if msg.stashRef != "stash@{0}" {
		t.Errorf("stashRef = %q, want stash@{0}", msg.stashRef)
	}
}

// TestCleanAndUnpushedWorktreeSkipAutoStash pins the two cases that must never
// stash: a clean tree has nothing to save, and an unpushed-only worktree keeps
// its commits in the reflog already.
func TestCleanAndUnpushedWorktreeSkipAutoStash(t *testing.T) {
	var stashed bool
	restoreStash := stashWorktreeFn
	stashWorktreeFn = func(_ context.Context, _ string) error {
		stashed = true
		return nil
	}
	defer func() { stashWorktreeFn = restoreStash }()

	restoreRemove := removeWorktreeFn
	removeWorktreeFn = func(_ context.Context, _, _ string, _ bool) error { return errStubbed }
	defer func() { removeWorktreeFn = restoreRemove }()

	clean := []cleanupTarget{{kind: cleanupWorktree, branch: "x", path: "/repo/.worktrees/x"}}
	deleteStepCmd(context.Background(), t.TempDir(), clean, 0, false)()
	if stashed {
		t.Error("clean worktree auto-stashed; there was nothing uncommitted to save")
	}

	stashed = false
	unpushed := []cleanupTarget{{
		kind: cleanupWorktree, branch: "x", path: "/repo/.worktrees/x",
		unpushed: true, quarantined: true,
	}}
	deleteStepCmd(context.Background(), t.TempDir(), unpushed, 0, false)()
	if stashed {
		t.Error("unpushed-only worktree auto-stashed; the reflog already covers its commits")
	}
}

// TestBatchStashRefsAccountForLaterStashes pins that when several dirty worktrees
// are deleted in one batch, each surfaced stash@{n} is the ref that item lands at
// after the later deletes stash on top of it, not the stash@{0} it held at birth.
func TestBatchStashRefsAccountForLaterStashes(t *testing.T) {
	restoreStash := stashWorktreeFn
	stashWorktreeFn = func(_ context.Context, _ string) error { return nil }
	defer func() { stashWorktreeFn = restoreStash }()

	restoreRemove := removeWorktreeFn
	removeWorktreeFn = func(_ context.Context, _, _ string, _ bool) error { return errStubbed }
	defer func() { removeWorktreeFn = restoreRemove }()

	targets := []cleanupTarget{
		{kind: cleanupWorktree, branch: "a", path: "/w/a", dirty: true, quarantined: true},
		{kind: cleanupWorktree, branch: "b", path: "/w/b", dirty: true, quarantined: true},
	}
	if msg := deleteStepCmd(context.Background(), t.TempDir(), targets, 0, false)().(deleteStepMsg); msg.stashRef != "stash@{1}" {
		t.Errorf("first-deleted stashRef = %q, want stash@{1} (the second delete stashes on top)", msg.stashRef)
	}
	if msg := deleteStepCmd(context.Background(), t.TempDir(), targets, 1, false)().(deleteStepMsg); msg.stashRef != "stash@{0}" {
		t.Errorf("last-deleted stashRef = %q, want stash@{0} (newest stash)", msg.stashRef)
	}
}

// TestConflictedWorktreeRefusesRatherThanFailingAtTheStash pins the refusal that
// replaced a guaranteed failure. `git stash push` cannot run on an unmerged index,
// so the old path always got git's "could not write index / <path>: needs merge"
// back — clipped to "stash failed: error" in the modal — and stopped anyway. The
// step now refuses first, touching neither the stash nor the removal, and says
// which operation is paused and how many files are unresolved.
func TestConflictedWorktreeRefusesRatherThanFailingAtTheStash(t *testing.T) {
	var stashed, removed bool
	restoreStash := stashWorktreeFn
	stashWorktreeFn = func(_ context.Context, _ string) error {
		stashed = true
		return nil
	}
	defer func() { stashWorktreeFn = restoreStash }()

	restoreRemove := removeWorktreeFn
	removeWorktreeFn = func(_ context.Context, _, _ string, _ bool) error {
		removed = true
		return errStubbed
	}
	defer func() { removeWorktreeFn = restoreRemove }()

	targets := []cleanupTarget{{
		kind: cleanupWorktree, branch: "x", path: "/repo/.worktrees/x",
		op: "merge", dirty: true, conflicts: 1, quarantined: true,
	}}
	msg, ok := deleteStepCmd(context.Background(), t.TempDir(), targets, 0, false)().(deleteStepMsg)
	if !ok {
		t.Fatal("expected a deleteStepMsg")
	}
	if stashed {
		t.Error("conflicted worktree attempted a stash; git refuses that outright")
	}
	if removed {
		t.Error("conflicted worktree was removed; its files have no stash to come back from")
	}
	if !isBlocked(msg.err) {
		t.Fatalf("err = %v, want a blockedError so the done phase can offer the retry", msg.err)
	}
	for _, want := range []string{"paused merge", "1 conflict"} {
		if !strings.Contains(msg.err.Error(), want) {
			t.Errorf("err %q missing %q — the message has to name what is stuck", msg.err, want)
		}
	}
}

// TestUnreadableWorktreeStatusRefusesToo pins the fail-safe on the other side of
// the status read. When `git status` errors, the reload knows the worktree is
// unsafe but not *how* — and a conflict count of zero then means "unknown", not
// "clean". Letting it read as clean is how the old parallel-map shape put the
// delete straight back into the stash refusal it was built to avoid.
func TestUnreadableWorktreeStatusRefusesToo(t *testing.T) {
	var stashed bool
	restoreStash := stashWorktreeFn
	stashWorktreeFn = func(_ context.Context, _ string) error {
		stashed = true
		return nil
	}
	defer func() { stashWorktreeFn = restoreStash }()

	restoreRemove := removeWorktreeFn
	removeWorktreeFn = func(_ context.Context, _, _ string, _ bool) error { return errStubbed }
	defer func() { removeWorktreeFn = restoreRemove }()

	var tgt cleanupTarget
	tgt.kind, tgt.branch, tgt.path = cleanupWorktree, "x", "/repo/.worktrees/x"
	tgt.applyWtState(wtState{dirty: true, unread: true})
	tgt.quarantined = tgt.held()
	if tgt.conflicts != 0 {
		t.Fatalf("fixture conflicts = %d, want 0 — the point is a zero that means unknown", tgt.conflicts)
	}

	msg := deleteStepCmd(context.Background(), t.TempDir(), []cleanupTarget{tgt}, 0, false)().(deleteStepMsg)
	if stashed {
		t.Error("unreadable worktree attempted a stash nothing knows would succeed")
	}
	if !isBlocked(msg.err) {
		t.Fatalf("err = %v, want a blockedError", msg.err)
	}
	if !strings.Contains(msg.err.Error(), "can't read its status") {
		t.Errorf("err %q should say the status couldn't be read, not report a conflict count", msg.err)
	}
}

// TestConflictsAloneQuarantines pins that quarantine reads the unstashable state
// directly rather than relying on dirty happening to be set alongside it. The
// forced removal passes `quarantined` as its --force, so a target that is
// conflicted but not flagged would send the one delete that most needs forcing
// out unforced.
func TestConflictsAloneQuarantines(t *testing.T) {
	t1 := cleanupTarget{kind: cleanupWorktree, branch: "x", path: "/w/x", conflicts: 1}
	if !t1.held() {
		t.Error("a target with unresolved conflicts is not quarantined")
	}
	t2 := cleanupTarget{kind: cleanupWorktree, branch: "y", path: "/w/y", statusUnread: true}
	if !t2.held() {
		t.Error("a target whose status could not be read is not quarantined")
	}
	clean := cleanupTarget{kind: cleanupWorktree, branch: "z", path: "/w/z", reason: "merged"}
	if clean.held() {
		t.Error("a clean, readable, unheld target was quarantined")
	}
}

// TestForcedConflictedWorktreeRemovesWithoutStashing pins the `f` retry: the stash
// is skipped (it cannot succeed) and the removal runs, which is the whole point —
// the files are knowingly given up in exchange for clearing the worktree.
func TestForcedConflictedWorktreeRemovesWithoutStashing(t *testing.T) {
	var stashed, gotForce, removed bool
	restoreStash := stashWorktreeFn
	stashWorktreeFn = func(_ context.Context, _ string) error {
		stashed = true
		return nil
	}
	defer func() { stashWorktreeFn = restoreStash }()

	restoreRemove := removeWorktreeFn
	removeWorktreeFn = func(_ context.Context, _, _ string, force bool) error {
		removed, gotForce = true, force
		return errStubbed
	}
	defer func() { removeWorktreeFn = restoreRemove }()

	targets := []cleanupTarget{{
		kind: cleanupWorktree, branch: "x", path: "/repo/.worktrees/x",
		op: "rebase", dirty: true, conflicts: 2, quarantined: true,
	}}
	msg := deleteStepCmd(context.Background(), t.TempDir(), targets, 0, true)().(deleteStepMsg)
	if stashed {
		t.Error("forced delete still tried to stash an unmerged index")
	}
	if !removed {
		t.Fatal("forced delete never reached the removal")
	}
	if !gotForce {
		t.Error("forced delete dropped --force; git refuses a conflicted worktree without it")
	}
	if isBlocked(msg.err) {
		t.Error("forced delete still reported the conflict refusal")
	}
	if msg.stashRef != "" {
		t.Errorf("stashRef = %q, want empty — nothing was stashed", msg.stashRef)
	}
}

// TestFailedStepKeepsGitsFullOutputForTheOverlay pins the split a one-line row
// forces. Git's refusals run to several lines and each modal row holds one, so the
// row shows the headline and `e` shows the rest. Before the stash went through
// internal/git it built its own error out of the raw multi-line output, and the
// row clipped that to its first word — `stash failed: error` — with the line
// naming the actual file nowhere on screen.
func TestFailedStepKeepsGitsFullOutputForTheOverlay(t *testing.T) {
	full := "error: could not write index\napps/devvit/src/state/locks.test.ts: needs merge"
	m := Model{
		shell: shell{deleting: true, deleteDone: true, deleteResults: []deleteResult{{
			target: cleanupTarget{kind: cleanupWorktree, branch: "x", path: "/w/x"},
			status: stFailed,
			err: fmt.Errorf("stash failed: %w", &git.CmdError{
				Err:      errStubbed,
				Headline: "error: could not write index",
				Output:   full,
			}),
		}}},
	}

	// The row stays one line — a modal row has no room for more.
	row := m.deleteResults[0].err.Error()
	if strings.Contains(row, "\n") {
		t.Errorf("row text spans lines, so the modal will clip it: %q", row)
	}
	if !strings.HasPrefix(row, "stash failed: ") {
		t.Errorf("row = %q, should name the step that failed", row)
	}

	// The lines it had no room for are reachable.
	title, detail := m.deleteErrDetail()
	if title == "" || !strings.Contains(detail, "locks.test.ts: needs merge") {
		t.Errorf("detail overlay lost git's own explanation: title=%q detail=%q", title, detail)
	}

	// `e` opens it, and dismissing the overlay comes back to the summary rather
	// than dropping the whole batch off screen.
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	opened := next.(Model)
	if !opened.errOpen {
		t.Fatal("e on the done modal did not open the detail overlay")
	}
	if !opened.deleting {
		t.Error("opening the overlay tore down the delete modal underneath it")
	}
	back, _ := opened.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if r := back.(Model); r.errOpen || !r.deleting {
		t.Errorf("dismissing the overlay should return to the summary; errOpen=%v deleting=%v", r.errOpen, r.deleting)
	}
}

// TestForceRetryTakesOnlyBlockedItems pins that `f` re-runs the conflict refusals
// and nothing else: a genuine git failure in the same batch is not a thing to
// retry destructively, and a success must not be attempted twice.
func TestForceRetryTakesOnlyBlockedItems(t *testing.T) {
	blocked := cleanupTarget{kind: cleanupWorktree, branch: "blocked", path: "/w/blocked", conflicts: 1}
	m := Model{
		shell: shell{deleting: true, deleteDone: true, deleteResults: []deleteResult{
			{target: cleanupTarget{kind: cleanupWorktree, branch: "ok", path: "/w/ok"}, status: stDone},
			{target: blocked, status: stFailed, err: &blockedError{op: "merge", conflicts: 1}},
			{target: cleanupTarget{kind: cleanupBranch, branch: "boom"}, status: stFailed, err: errStubbed},
		}},
	}
	if got := m.blockedCount(); got != 1 {
		t.Fatalf("blockedCount = %d, want 1", got)
	}

	restoreRemove := removeWorktreeFn
	removeWorktreeFn = func(_ context.Context, _, _ string, _ bool) error { return errStubbed }
	defer func() { removeWorktreeFn = restoreRemove }()

	next, _ := m.forceBlocked()
	got := next.(Model)
	if len(got.deleteTargets) != 1 || got.deleteTargets[0].branch != "blocked" {
		t.Errorf("retry batch = %+v, want only the conflict-blocked target", got.deleteTargets)
	}
	if !got.deleteForce {
		t.Error("retry batch did not carry force; it would refuse all over again")
	}
	if got.deleteDone {
		t.Error("retry left the modal in its done phase")
	}
}

// TestDoneModalDismissesOnKeysOtherThanForce pins that `f` only means "delete
// anyway" while something is actually blocked — otherwise it dismisses like any
// other key, so a stray press can never destroy files.
func TestDoneModalDismissesOnKeysOtherThanForce(t *testing.T) {
	blockedModel := func() Model {
		return Model{
			shell: shell{deleting: true, deleteDone: true, deleteResults: []deleteResult{{
				target: cleanupTarget{kind: cleanupWorktree, branch: "x", path: "/w/x", conflicts: 1},
				status: stFailed, err: &blockedError{op: "merge", conflicts: 1},
			}}},
		}
	}
	m := blockedModel()
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if next.(Model).deleting {
		t.Error("q on the done modal did not dismiss it")
	}

	noneBlocked := Model{
		shell: shell{deleting: true, deleteDone: true, deleteResults: []deleteResult{{
			target: cleanupTarget{kind: cleanupBranch, branch: "x"}, status: stFailed, err: errStubbed,
		}}},
	}
	next, _ = noneBlocked.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if next.(Model).deleting {
		t.Error("f with nothing blocked did not dismiss the modal")
	}
}

// TestKeybarDropsPairsRatherThanTruncating pins that a keymap too wide for the
// terminal loses whole bindings from the middle, never its ends. The footer is
// truncated to the width by the caller, so an overlong keymap used to silently eat
// whatever sat last — always `←→ tab`, the one binding every view shares.
func TestKeybarDropsPairsRatherThanTruncating(t *testing.T) {
	m := seededModel()
	m.active, m.pane = tabLocal, paneBranches
	full := m.keymap()
	if len(full) < 4 {
		t.Fatalf("Branches keymap too short to exercise dropping: %v", full)
	}
	first, last := full[0], full[len(full)-1]

	for _, width := range []int{40, 60, 80, 96, 200} {
		bar := stripANSI(m.keybar(width))
		if w := lipgloss.Width(bar); w > width {
			t.Errorf("width %d: keybar rendered %d cells: %q", width, w, bar)
		}
		if !strings.Contains(bar, first[0]+" "+first[1]) {
			t.Errorf("width %d: dropped the leading %q pair: %q", width, first[0], bar)
		}
		if !strings.Contains(bar, last[0]+" "+last[1]) {
			t.Errorf("width %d: dropped the pinned %q pair: %q", width, last[0], bar)
		}
		if strings.Contains(bar, "…") {
			t.Errorf("width %d: keybar truncated mid-binding instead of dropping whole pairs: %q", width, bar)
		}
	}
}

// TestKeybarNeverDropsQuitBeforeTabHint is admin-3rgp.27's own regression:
// growing the Issues/Beads keymap by two pairs (`space`/`w`) at the width the
// TestHeaderPinnedAndHeightBounded fixture uses (100) used to push `q quit`
// — the pair directly ahead of `←→ tab` on every tab but the Branches pane's
// own — out of the footer entirely, since the old trimming loop protected
// only the single last pair. `quit` has to survive alongside the tab hint,
// not instead of it.
func TestKeybarNeverDropsQuitBeforeTabHint(t *testing.T) {
	m := seededModel()
	m.active = tabIssues
	for _, width := range []int{40, 60, 80, 96, 100, 200} {
		bar := stripANSI(m.keybar(width))
		if !strings.Contains(bar, "q") || !strings.Contains(bar, "quit") {
			t.Errorf("width %d: keybar dropped quit: %q", width, bar)
		}
		if !strings.Contains(bar, "tab") {
			t.Errorf("width %d: keybar dropped the tab hint: %q", width, bar)
		}
		if w := lipgloss.Width(bar); w > width {
			t.Errorf("width %d: keybar rendered %d cells", width, w)
		}
	}
}

// TestQuarantineLabelsRankByWhatIsAtStake pins the label order. Every one of these
// says "don't sweep this", so the word shown has to be the one that costs most to
// lose: the stuck git state first, then live use, then files that die with the
// directory, and only then commits the reflog would give back.
func TestQuarantineLabelsRankByWhatIsAtStake(t *testing.T) {
	dirty := cleanupTarget{kind: cleanupWorktree, branch: "b", reason: "gone", dirty: true, quarantined: true}
	if got := cleanupReason(dirty); got != "uncommitted" {
		t.Errorf("plain dirty worktree = %q, want %q", got, "uncommitted")
	}

	unpushed := cleanupTarget{kind: cleanupBranch, branch: "b", reason: "gone", unpushed: true, quarantined: true}
	if got := cleanupReason(unpushed); got != "unpushed" {
		t.Errorf("branch with local-only commits = %q, want %q", got, "unpushed")
	}

	both := dirty
	both.unpushed = true
	if got := cleanupReason(both); got != "uncommitted" {
		t.Errorf("worktree with both = %q, want %q — the unrecoverable half must be the one named", got, "uncommitted")
	}

	active := dirty
	active.active = true
	if got := cleanupReason(active); got != "active" {
		t.Errorf("in-use worktree = %q, want %q", got, "active")
	}

	rebasing := active
	rebasing.op = "rebase"
	if got := cleanupReason(rebasing); got != "rebasing" {
		t.Errorf("mid-rebase worktree = %q, want %q — the generic words hide what's actually holding it", got, "rebasing")
	}
	bisecting := active
	bisecting.op = "bisect"
	if got := cleanupReason(bisecting); got != "bisecting" {
		t.Errorf("mid-bisect worktree = %q, want %q", got, "bisecting")
	}
}

// TestLaunchSyncFiresOnceAndDoesNotLoop checks the launch sync stays one sync.
// Every sync ends by re-reading local git (the prune moved the refs), and that
// reload lands as localDataMsg — the same message that starts the launch sync —
// so an ungated flag would sync, reload, sync, forever.
func TestLaunchSyncFiresOnceAndDoesNotLoop(t *testing.T) {
	local := localDataMsg{d: localData{
		signals:      map[string]gitSignals{},
		wtState:      map[string]wtState{},
		branchUnsafe: map[string]bool{},
	}}

	m := New(context.Background(), "/repo", "owner/repo", "main", "main")
	// tracker = ["beads"] declared: the launch sync must wait for the probe.
	m.manifest = &manifest.Manifest{Trackers: []string{"beads"}, HasTracker: true}
	updated, cmd := m.Update(local)
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("the launch sync went out before the beads probe answered — in a beads repo that spends the issue query nothing will render")
	}
	updated, cmd = m.Update(beadsDetectedMsg{})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("the beads probe landing did not start the launch sync")
	}
	if !m.synced || !m.syncing {
		t.Fatalf("launch sync flags not set: synced=%v syncing=%v", m.synced, m.syncing)
	}

	// The reload every sync ends with must not start another one.
	m.syncing = false
	updated, cmd = m.Update(local)
	if cmd != nil {
		t.Error("a later local reload started a second sync; the sync → reload → sync cycle would never settle")
	}
	_ = updated
}

// TestSyncIsTabBlind pins that the poll refreshes everything regardless of which
// tab is on screen. The old per-tab fetch meant the list you were staring at was
// the one that went stale — sitting on the PRs tab, nothing ever re-fetched PRs,
// because only a tab *visit* could.
func TestSyncIsTabBlind(t *testing.T) {
	for _, at := range []tab{tabLocal, tabPRs, tabIssues} {
		m := seededModel()
		m.active = at
		m.syncing, m.synced = false, true
		m.remote.PRsFetchedAt = time.Now().Add(-2 * data.TTL)

		updated, cmd := m.Update(syncTickMsg{})
		if !updated.(Model).syncing {
			t.Errorf("tab %v: the poll did not sync a stale cache", at)
		}
		if cmd == nil {
			t.Errorf("tab %v: the poll did not re-arm", at)
		}
	}

	// Switching tabs spends nothing: the poll owns freshness now.
	m := seededModel()
	m.remote.PRsFetchedAt = time.Now().Add(-2 * data.TTL)
	m.switchTab(tabPRs)
	if m.syncing {
		t.Error("switching tabs fired a fetch; freshness must not depend on where the cursor is")
	}
}

// TestSyncGateSkipsFreshCacheAndInFlightSync pins the two things the single gate
// has to refuse: a relaunch inside TTL (the disk cache is good, spend no gh call)
// and a second sync while one is already running. Force overrides the first.
func TestSyncGateSkipsFreshCacheAndInFlightSync(t *testing.T) {
	now := time.Now()
	fresh := func() Model {
		m := seededModel()
		m.remote.PRsFetchedAt = now
		m.remote.IssuesFetchedAt = now
		m.remote.MergedFetchedAt = now
		m.syncing = false
		return m
	}

	m := fresh()
	if cmd := m.startSyncCmd(false); cmd != nil {
		t.Error("a fresh cache should render off disk, not spend a sync")
	}
	if cmd := m.startSyncCmd(true); cmd == nil {
		t.Error("force should sync even a fresh cache — that's the whole point of ⇧R")
	}

	stale := fresh()
	stale.remote.MergedFetchedAt = now.Add(-2 * data.TTL)
	if cmd := stale.startSyncCmd(false); cmd == nil {
		t.Error("a stale cache should sync")
	}
	if cmd := stale.startSyncCmd(false); cmd != nil {
		t.Error("the in-flight guard should suppress a second sync")
	}
	if cmd := stale.startSyncCmd(true); cmd != nil {
		t.Error("even force must not double up on a sync already running")
	}
}

// TestSyncDoneKeepsCachedSetOnFailure pins that a failed section of the sync keeps
// the data it already had — otherwise one transient gh hiccup would make every
// squash-merged branch read as live until the next poll.
func TestSyncDoneKeepsCachedSetOnFailure(t *testing.T) {
	m := seededModel()
	m.remote.MergedHeads = map[string]string{"squash-merged": "sha-sq"}
	m.syncing = true

	updated, cmd := m.Update(syncDoneMsg{r: data.Remote{MergedFetchError: "boom"}})
	m = updated.(Model)
	if m.remote.MergedHeads["squash-merged"] == "" {
		t.Error("a failed merged section blanked the cached set")
	}
	if m.syncing {
		t.Error("the in-flight flag should clear even on a failed sync")
	}
	if cmd == nil {
		t.Error("a sync must re-read local git afterwards — the prune moved the remote-tracking refs")
	}

	// A clean sync adds to the set without dropping what was already there — the
	// merged-PR fetch reads a sliding window, so replacing would lose every branch
	// that merged before it (see applyMergedTo).
	updated, _ = m.Update(syncDoneMsg{r: data.Remote{
		MergedHeads:     map[string]string{"other": "sha-other"},
		MergedFetchedAt: time.Now(),
	}})
	m = updated.(Model)
	if m.remote.MergedHeads["squash-merged"] == "" {
		t.Errorf("a clean sync dropped a head that fell out of the window: %v", m.remote.MergedHeads)
	}
	if m.remote.MergedHeads["other"] == "" {
		t.Errorf("a clean sync did not add the new head: %v", m.remote.MergedHeads)
	}
}

// TestSyncFailureSaysWhatFailed pins that a failed sync names its failed
// sections and time in the header, puts the first error on the status line, and
// keeps every error verbatim behind `e` — and that a clean sync takes the header
// back.
func TestSyncFailureSaysWhatFailed(t *testing.T) {
	m := seededModel()
	at := time.Date(2026, 10, 2, 14, 3, 7, 0, time.Local)
	updated, _ := m.Update(syncDoneMsg{r: data.Remote{
		PRsFetchError:    "exit status 1: HTTP 502\nbad gateway",
		MergedFetchError: "context deadline exceeded",
	}, at: at})
	m = updated.(Model)

	if h := stripANSI(m.headerLine(200)); !strings.Contains(h, "⚠ sync failed · PRs, merged · 2:03pm") {
		t.Errorf("header does not name the failed sections and time: %q", h)
	}
	if want := "sync failed: PRs: exit status 1: HTTP 502 bad gateway"; m.status != want {
		t.Errorf("status = %q, want %q", m.status, want)
	}

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = next.(Model)
	if !m.errOpen {
		t.Fatal("`e` did not open the sync error detail")
	}
	for _, want := range []string{"PRs: exit status 1: HTTP 502\nbad gateway", "merged: context deadline exceeded", "log: " + data.SyncLogPath()} {
		if !strings.Contains(m.errDetail, want) {
			t.Errorf("detail is missing %q:\n%s", want, m.errDetail)
		}
	}

	updated, _ = m.Update(syncDoneMsg{r: data.Remote{
		PRsFetchedAt: time.Now(), MergedFetchedAt: time.Now(),
	}, at: time.Now()})
	m = updated.(Model)
	if h := stripANSI(m.headerLine(200)); strings.Contains(h, "sync failed") {
		t.Errorf("a clean sync left the failure in the header: %q", h)
	}
	if m.errDetail != "" || m.errOpen || m.status != "" {
		t.Errorf("a clean sync left the failure behind `e` or on the status line: open=%v status=%q detail=%q", m.errOpen, m.status, m.errDetail)
	}
}

// TestPersistingSyncFailureDoesNotClobber pins the two guards on a failure that
// simply persists across polls: an action's error stashed since the first
// announcement stays behind `e`, and an open overlay stays open on what it shows.
// A clean sync then withdraws nothing it did not put up.
func TestPersistingSyncFailureDoesNotClobber(t *testing.T) {
	m := seededModel()
	failing := syncDoneMsg{r: data.Remote{PRsFetchError: "HTTP 502"}, at: time.Now()}
	updated, _ := m.Update(failing)
	m = updated.(Model)

	updated, _ = m.Update(actionMsg{text: "checkout failed", err: checkoutRefusal()})
	m = updated.(Model)
	actionDetail := m.errDetail
	updated, _ = m.Update(failing)
	m = updated.(Model)
	if m.errDetail != actionDetail {
		t.Errorf("an unchanged sync failure replaced the action's detail:\n%s", m.errDetail)
	}

	m.errOpen = true
	updated, _ = m.Update(syncDoneMsg{r: data.Remote{PRsFetchError: "HTTP 503"}, at: time.Now()})
	m = updated.(Model)
	if !m.errOpen || m.errDetail != actionDetail {
		t.Errorf("a new sync failure swapped the open overlay: open=%v\n%s", m.errOpen, m.errDetail)
	}

	updated, _ = m.Update(syncDoneMsg{r: data.Remote{PRsFetchedAt: time.Now()}, at: time.Now()})
	m = updated.(Model)
	if m.errDetail != actionDetail {
		t.Errorf("a clean sync withdrew an action's detail:\n%s", m.errDetail)
	}
}

// TestSyncLogsPruneFailureWithGitsWholeOutput pins the prune half of the sync:
// its failure reaches the log with git's whole output, not CmdError's headline.
func TestSyncLogsPruneFailureWithGitsWholeOutput(t *testing.T) {
	prevFetch, prevPrune := fetchAllFn, fetchPruneFn
	t.Cleanup(func() { fetchAllFn, fetchPruneFn = prevFetch, prevPrune })
	fetchAllFn = func(context.Context, string, data.SyncOpts) data.Remote { return data.Remote{} }
	fetchPruneFn = func(context.Context, string) error {
		return &git.CmdError{Err: errors.New("exit status 128"), Headline: "fatal: unable to access", Output: "fatal: unable to access\nhint: check your network"}
	}

	syncCmd(context.Background(), "/repo", "owner/prune-probe", false, false)()

	b, err := os.ReadFile(data.SyncLogPath())
	if err != nil {
		t.Fatal(err)
	}
	want := `sync owner/prune-probe prune FAIL`
	wantErr := `err="fatal: unable to access\nhint: check your network"`
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, want) {
			if !strings.Contains(line, wantErr) {
				t.Errorf("prune line lost git's output: %q", line)
			}
			return
		}
	}
	t.Errorf("no %q line in the sync log:\n%s", want, b)
}

// TestRowRefreshDroppedAfterSync pins the generation guard: a row-scoped refresh
// that comes back after a whole-repo sync has landed is older news than what is
// already on screen, so it must be dropped instead of applied.
func TestRowRefreshDroppedAfterSync(t *testing.T) {
	m := seededModel()
	m.remote.Issues = []gh.IssueItem{{Number: 7, Title: "from the sync"}}
	gen := m.syncGen

	// A sync lands while the row fetch is out.
	updated, _ := m.Update(syncDoneMsg{r: data.Remote{IssuesFetchedAt: time.Now()}})
	m = updated.(Model)

	updated, _ = m.Update(issueRowMsg{gen: gen, number: 7, ok: true, item: gh.IssueItem{Number: 7, Title: "stale row"}})
	m = updated.(Model)
	if len(m.remote.Issues) > 0 && m.remote.Issues[0].Title == "stale row" {
		t.Error("a row result from before the sync stomped the fresher wholesale data")
	}
}

// TestCleanupBoxCollapsesUnfocused checks the Cleanup box is a counts+hint summary
// until its pane is focused, at which point it expands to the pickable checkboxes.
// TestBranchesFooterAdvertisesCleanup checks the Branches box's bottom border
// carries the sweep affordance in place of the old, separate Cleanup box — the
// count of ready rows and the ⇧X hint — and says nothing when there is nothing
// ready to sweep.
func TestBranchesFooterAdvertisesCleanup(t *testing.T) {
	m := seededModel()
	m.active, m.pane = tabLocal, paneBranches

	out := stripANSI(m.View())
	if !strings.Contains(out, "clean up") || !strings.Contains(out, "⇧X") {
		t.Errorf("Branches footer is missing the sweep affordance\n%s", out)
	}
	if strings.Contains(out, "┌─ Cleanup") {
		t.Errorf("a separate Cleanup box is still drawn\n%s", out)
	}

	m.remote.MergedHeads = map[string]string{}
	m.local.signals = map[string]gitSignals{}
	// wt-branch's worktree path is fake, so it would otherwise count as a stale
	// record to prune; stub it present so the fixture reaches genuinely nothing.
	statFn = func(string) (os.FileInfo, error) { return nil, nil }
	defer func() { statFn = os.Stat }()
	out = stripANSI(m.View())
	if strings.Contains(out, "clean up") {
		t.Errorf("footer advertised cleanup with nothing to clean\n%s", out)
	}
}

func TestHeaderShowsDataFreshness(t *testing.T) {
	m := seededModel()
	m.active = tabPRs

	// A known age renders as "cached · Nm ago" for the active remote tab.
	m.remote.PRsFetchedAt = time.Now().Add(-90 * time.Second)
	out := stripANSI(m.View())
	if !strings.Contains(out, "cached · 1m ago") {
		t.Errorf("header missing freshness label %q\nheader: %q", "cached · 1m ago", strings.SplitN(out, "\n", 2)[0])
	}

	// A never-fetched section (zero time) falls back to a plain "cached".
	m.remote.PRsFetchedAt = time.Time{}
	out = stripANSI(m.View())
	if strings.Contains(out, "cached · ") {
		t.Errorf("zero fetch time should render plain 'cached', not an age\nheader: %q", strings.SplitN(out, "\n", 2)[0])
	}
}

func TestEnterOnWorktreeBranchQuits(t *testing.T) {
	m := seededModel()
	// move cursor to the worktree branch row
	for i := 0; i < 6; i++ {
		if r, ok := m.currentRow(); ok && r.wtPath != "" {
			break
		}
		m.moveCursor(1)
	}
	updated, cmd := m.activate()
	um := updated.(Model)
	if um.Pending == nil || um.Pending.Kind != "cd" {
		t.Fatalf("expected pending cd action, got %+v", um.Pending)
	}
	if cmd == nil {
		t.Fatal("expected quit command")
	}
	_ = tea.Quit // ensure import used
}

// In herdr mode, enter on a worktree branch does NOT quit — it kicks off the
// inline open (marking the dashboard busy) and leaves Pending nil so the
// dashboard stays up.
func TestEnterOnWorktreeBranchInHerdrStaysOpen(t *testing.T) {
	m := seededModel()
	m.herdr = true
	for i := 0; i < 6; i++ {
		if r, ok := m.currentRow(); ok && r.wtPath != "" {
			break
		}
		m.moveCursor(1)
	}
	updated, cmd := m.activate()
	um := updated.(Model)
	if um.Pending != nil {
		t.Fatalf("herdr mode should not set a pending action, got %+v", um.Pending)
	}
	if !um.creating {
		t.Error("herdr mode should mark the dashboard busy (creating)")
	}
	if cmd == nil {
		t.Fatal("expected an inline worktree command, got nil")
	}
}

// Capital W is a plain-letter fallback for terminals that swallow alt+enter; it
// routes to the same worktreeCurrentBranch handler. This pins that the fallback
// fires the worktree flow on the Local branch pane.
func TestCapitalWWorktreesCurrentBranch(t *testing.T) {
	m := seededModel()
	m.herdr = true
	m.active, m.pane = tabLocal, paneBranches
	for i := 0; i < 6; i++ {
		if r, ok := m.currentRow(); ok && r.wtPath != "" {
			break
		}
		m.moveCursor(1)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'W'}})
	um := updated.(Model)
	if !um.creating {
		t.Error("W should mark the dashboard busy (creating), same as alt+enter")
	}
	if cmd == nil {
		t.Fatal("expected an inline worktree command from W, got nil")
	}
}

// TestBeadsGroupByWhatBlocksThem pins the Beads pane's sections. A bead is sorted
// by whether anything is holding it up, not by who owns it: work you can start
// now, work already moving, work waiting on something else. A blocker that has
// been closed is gone from the workspace's own list, so it stops holding
// anything up — and a deferred bead is off the board while still blocking
// whatever depends on it.
func TestBeadsGroupByWhatBlocksThem(t *testing.T) {
	beads := []bd.Bead{
		{ID: "p-1", Title: "start me", Status: "open"},
		{ID: "p-2", Title: "moving", Status: "in_progress"},
		{ID: "p-3", Title: "waits on p-1", Status: "open", Blockers: []string{"p-1"}},
		{ID: "p-4", Title: "waits on something closed", Status: "open", Blockers: []string{"p-gone"}},
		{ID: "p-5", Title: "on ice", Status: "deferred"},
		{ID: "p-6", Title: "waits on the deferred one", Status: "open", Blockers: []string{"p-5"}},
	}
	human, ready, inProgress, blocked := groupBeads(beads)

	ids := func(bs []bd.Bead) []string {
		out := make([]string, 0, len(bs))
		for _, b := range bs {
			out = append(out, b.ID)
		}
		return out
	}
	for _, c := range []struct {
		section string
		got     []string
		want    []string
	}{
		{"Needs you", ids(human), nil},
		{"Ready", ids(ready), []string{"p-1", "p-4"}},
		{"In progress", ids(inProgress), []string{"p-2"}},
		{"Blocked", ids(blocked), []string{"p-3", "p-6"}},
	} {
		if strings.Join(c.got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s holds %v, want %v", c.section, c.got, c.want)
		}
	}
}

// TestTrackerKeyChoosesTheTrackerTab pins admin-3rgp.22 (and its
// admin-j00d extension to a list): which tracker tab(s) draw — Issues,
// Beads, both (in declared order), or none — follow the manifest's declared
// `tracker` list alone. Neither a `bd where` probe nor any other disk state
// gets a vote: a "github" tracker with a beads workspace sitting on disk
// still draws only Issues, and an undeclared tracker draws no tracker tab
// even with one on disk.
func TestTrackerKeyChoosesTheTrackerTab(t *testing.T) {
	github := seededModel() // seededModel declares tracker = ["github"]
	if got := github.visibleTabs(); len(got) != 4 || got[2] != tabIssues {
		t.Fatalf("tracker = [\"github\"] drew %v, want Repo/PRs/Issues/Config", got)
	}

	beads := seededModel()
	beads.manifest.Trackers = []string{"beads"}
	got := beads.visibleTabs()
	if len(got) != 4 || got[2] != tabBeads {
		t.Fatalf("tracker = [\"beads\"] drew %v, want Repo/PRs/Beads/Config", got)
	}
	beads.switchTabAt(2)
	if beads.active != tabBeads {
		t.Errorf("`3` landed on %v, want the third drawn tab (Beads)", beads.active)
	}
	beads.switchTab(tabIssues)
	if beads.active != tabBeads {
		t.Error("switching to a tab that isn't drawn moved the active tab anyway")
	}

	// The declared tracker wins over a live probe pointing the other way.
	githubWithBeadsOnDisk := seededModel()
	githubWithBeadsOnDisk.feeds.beads.present = true
	if got := githubWithBeadsOnDisk.visibleTabs(); len(got) != 4 || got[2] != tabIssues {
		t.Fatalf("tracker = [\"github\"] with a beads workspace on disk drew %v, want Issues still", got)
	}

	// Both declared draws both, in array order.
	both := seededModel()
	both.manifest.Trackers = []string{"beads", "github"}
	if got := both.visibleTabs(); len(got) != 5 || got[2] != tabBeads || got[3] != tabIssues {
		t.Fatalf("tracker = [\"beads\", \"github\"] drew %v, want Repo/PRs/Beads/Issues/Config", got)
	}

	// Reversed declared order draws reversed.
	reversed := seededModel()
	reversed.manifest.Trackers = []string{"github", "beads"}
	if got := reversed.visibleTabs(); len(got) != 5 || got[2] != tabIssues || got[3] != tabBeads {
		t.Fatalf("tracker = [\"github\", \"beads\"] drew %v, want Repo/PRs/Issues/Beads/Config", got)
	}

	// An undeclared tracker draws no tracker tab at all — even with a beads
	// workspace sitting on disk.
	none := seededModel()
	none.manifest.Trackers = nil
	none.manifest.HasTracker = false
	none.feeds.beads.present = true
	if got := none.visibleTabs(); len(got) != 3 {
		t.Fatalf("undeclared tracker drew %v, want Repo/PRs/Config with no tracker tab", got)
	}
	for _, tb := range none.visibleTabs() {
		if tb == tabIssues || tb == tabBeads {
			t.Errorf("a tracker tab (%v) drew with no tracker declared", tb)
		}
	}

	// A repo with no remote draws no PRs tab either, so the tracker tab moves
	// up to index 1.
	local := seededModel()
	local.nwo = ""
	local.manifest.Trackers = []string{"beads"}
	if got := local.visibleTabs(); len(got) != 3 || got[1] != tabBeads {
		t.Fatalf("a remote-less beads repo drew %v, want Repo/Beads/Config", got)
	}
	local.switchTabAt(1)
	if local.active != tabBeads {
		t.Errorf("`2` landed on %v, want Beads", local.active)
	}
}

// TestUsesPullRequestsKeyControlsThePRsTab pins admin-3rgp.22: the PRs tab's
// presence follows the declared `uses_pull_requests` key alone. Zero open PRs
// is a legitimate state prRows renders as an empty section, not a reason for
// the tab itself to disappear — and the key is independent of tracker, so a
// beads project that also declares PRs shows both tabs at once.
func TestUsesPullRequestsKeyControlsThePRsTab(t *testing.T) {
	noPRsOpen := seededModel()
	noPRsOpen.remote.PRs = nil
	if got := noPRsOpen.visibleTabs(); len(got) != 4 || got[1] != tabPRs {
		t.Fatalf("uses_pull_requests = true with zero open PRs drew %v, want the tab to stay (Repo/PRs/Issues/Config)", got)
	}

	undeclared := seededModel()
	undeclared.manifest.UsesPullRequests, undeclared.manifest.HasUsesPullRequests = false, false
	if got := undeclared.visibleTabs(); len(got) != 3 {
		t.Fatalf("uses_pull_requests undeclared drew %v, want no PRs tab", got)
	}

	declaredFalse := seededModel()
	declaredFalse.manifest.UsesPullRequests = false // HasUsesPullRequests stays true: an explicit false
	if got := declaredFalse.visibleTabs(); len(got) != 3 {
		t.Fatalf("uses_pull_requests = false drew %v, want no PRs tab", got)
	}

	both := seededModel()
	both.manifest.Trackers = []string{"beads"}
	if got := both.visibleTabs(); len(got) != 4 || got[1] != tabPRs || got[2] != tabBeads {
		t.Fatalf("a beads repo with uses_pull_requests = true drew %v, want Repo/PRs/Beads/Config", got)
	}
}

// TestEnterOnABeadBuildsAWorktreeAndWritesNothing pins that starting work on a
// bead creates the branch and stops there. Moving the bead to in_progress is the
// job of whoever does the work; the dashboard only opens the window.
func TestEnterOnABeadBuildsAWorktreeAndWritesNothing(t *testing.T) {
	m := seededModel()
	m.feeds.beads.present = true
	m.feeds.beads.have = true
	m.active = tabBeads
	m.feeds.beads.beads = []bd.Bead{{ID: "bd-4f2", Title: "Add password reset", Status: "open", Type: "feature"}}
	m.clampCursor()
	m.moveCursor(1)

	r, ok := m.currentRow()
	if !ok || r.kind != rowBead {
		t.Fatalf("no bead row under the cursor: ok=%v kind=%v", ok, r.kind)
	}
	updated, _ := m.activate()
	um := updated.(Model)
	if um.Pending == nil {
		t.Fatal("enter on a bead did not queue the worktree action")
	}
	if got, want := um.Pending.Branch, "bd-4f2-add-password-reset"; got != want {
		t.Errorf("enter on a bead builds branch %q, want %q", got, want)
	}
}

// typeIntoNamePrompt feeds a string into the naming prompt one rune at a time,
// the way real keystrokes do, so refreshNameCands runs after every character
// exactly as it would live.
func typeIntoNamePrompt(m Model, s string) Model {
	for _, r := range s {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	return m
}

// resolvedBranch presses enter on the naming prompt, runs the resulting
// resolveNameCmd synchronously, and feeds its nameResolvedMsg back through
// Update — mirroring the real round trip without a live goroutine — then
// returns the branch the dashboard queued a worktree for.
func resolvedBranch(t *testing.T, m Model) string {
	t.Helper()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("enter on the naming prompt produced no command")
	}
	updated, _ = m.Update(cmd())
	um := updated.(Model)
	if um.Pending == nil {
		t.Fatal("enter did not resolve to a queued worktree action")
	}
	return um.Pending.Branch
}

// TestNamePromptBeadSearch pins the `w` prompt's bead search (issue #17): the
// candidate list never overrides a plain typed name unless a row is actually
// highlighted, an exact id match is the only thing that highlights on its own,
// and editing the buffer always drops whatever was highlighted before it.
func TestNamePromptBeadSearch(t *testing.T) {
	beads := []bd.Bead{
		{ID: "bd-4f2", Title: "Add password reset", Status: "open", Type: "feature"},
		{ID: "bd-9a1", Title: "Widget one", Status: "open", Type: "task"},
		{ID: "bd-9a2", Title: "Widget two", Status: "open", Type: "task"},
		{ID: "bd-9a3", Title: "Widget three", Status: "open", Type: "task"},
		{ID: "bd-9a4", Title: "Widget four", Status: "open", Type: "task"},
		{ID: "bd-9a5", Title: "Widget five", Status: "open", Type: "task"},
		{ID: "bd-9a6", Title: "Widget six", Status: "open", Type: "task"},
	}

	base := seededModel()
	base.feeds.beads.present, base.feeds.beads.have = true, true
	base.feeds.beads.beads = beads
	base.active, base.pane = tabLocal, paneBranches
	// resolveNameCmd asks git whether the typed name is a legal ref, in the
	// checkout; seededModel's /repo does not exist, so give it a real directory.
	base.checkout = t.TempDir()

	open := func() Model {
		updated, _ := base.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
		return updated.(Model)
	}

	// Criterion 1: literal text matching no bead still resolves the literal text.
	t.Run("literal text with no match resolves the literal", func(t *testing.T) {
		m := typeIntoNamePrompt(open(), "totally-unmatched-branch-name")
		if m.nameSel != -1 {
			t.Fatalf("nameSel = %d, want -1 (nothing should match)", m.nameSel)
		}
		if got, want := resolvedBranch(t, m), "totally-unmatched-branch-name"; got != want {
			t.Errorf("branch = %q, want %q", got, want)
		}
	})

	// Criterion 2: an exact id match auto-highlights, and enter takes it with no ↓.
	t.Run("exact id match auto-highlights and enter takes it", func(t *testing.T) {
		m := typeIntoNamePrompt(open(), "bd-4f2")
		if m.nameSel != 0 {
			t.Fatalf("nameSel = %d, want 0 (exact id match)", m.nameSel)
		}
		if len(m.nameCands) != 1 || m.nameCands[0].ID != "bd-4f2" {
			t.Fatalf("nameCands = %v, want just bd-4f2", m.nameCands)
		}
		if got, want := resolvedBranch(t, m), "bd-4f2"; got != want {
			t.Errorf("branch = %q, want %q", got, want)
		}
	})

	// Criterion 3: a title word plus ↓ then enter takes the highlighted bead.
	t.Run("title word plus down then enter takes the bead", func(t *testing.T) {
		m := typeIntoNamePrompt(open(), "reset")
		if m.nameSel != -1 {
			t.Fatalf("nameSel = %d, want -1 before any ↓ (title match must not auto-highlight)", m.nameSel)
		}
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(Model)
		if m.nameSel != 0 {
			t.Fatalf("nameSel = %d after ↓, want 0", m.nameSel)
		}
		if got, want := resolvedBranch(t, m), "bd-4f2"; got != want {
			t.Errorf("branch = %q, want %q", got, want)
		}
	})

	// Criterion 4: the same title word with enter alone still takes the literal
	// text — the load-bearing rule that a candidate list changes nothing on its
	// own.
	t.Run("title word with enter alone resolves the literal", func(t *testing.T) {
		m := typeIntoNamePrompt(open(), "reset")
		if got, want := resolvedBranch(t, m), "reset"; got != want {
			t.Errorf("branch = %q, want %q", got, want)
		}
	})

	// Criterion 5: editing the buffer after a manual ↓ drops the highlight.
	t.Run("a rune after down drops the highlight", func(t *testing.T) {
		m := typeIntoNamePrompt(open(), "bd-4f") // prefix match, not exact: no auto-highlight
		if m.nameSel != -1 {
			t.Fatalf("nameSel = %d, want -1 (prefix match must not auto-highlight)", m.nameSel)
		}
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(Model)
		if m.nameSel != 0 {
			t.Fatalf("nameSel = %d after ↓, want 0", m.nameSel)
		}
		m = typeIntoNamePrompt(m, "z") // now matches nothing
		if m.nameSel != -1 {
			t.Errorf("nameSel = %d after editing, want -1 (edit must drop the highlight)", m.nameSel)
		}
	})

	// Criterion 6: the list caps at five even when more beads match.
	t.Run("more than five matches yields five", func(t *testing.T) {
		m := typeIntoNamePrompt(open(), "widget")
		if len(m.nameCands) != nameCandMax {
			t.Fatalf("got %d candidates, want %d", len(m.nameCands), nameCandMax)
		}
	})

	// Criterion 7: esc clears the buffer, the candidate list and the highlight
	// together.
	t.Run("esc clears buffer, candidates and highlight", func(t *testing.T) {
		m := typeIntoNamePrompt(open(), "bd-4f2")
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = updated.(Model)
		if m.naming {
			t.Error("esc left the prompt open")
		}
		if m.nameBuf != "" {
			t.Errorf("nameBuf = %q after esc, want empty", m.nameBuf)
		}
		if m.nameCands != nil {
			t.Errorf("nameCands = %v after esc, want nil", m.nameCands)
		}
		if m.nameSel != -1 {
			t.Errorf("nameSel = %d after esc, want -1", m.nameSel)
		}
	})
}

// TestNamePromptFooterUnchangedWithoutBeads pins criterion 9: a repo with no
// beads loaded draws the naming footer byte-identical to what it drew before
// this prompt could search anything — the candidate search must add nothing
// when there is nothing to search.
func TestNamePromptFooterUnchangedWithoutBeads(t *testing.T) {
	m := seededModel()
	m.naming = true
	m.nameBuf = "some-branch-name"
	const width = 100

	got := m.footer(width)
	want := renderSegs([]seg{
		{t: " "}, {t: "new worktree: ", fg: cDim}, {t: "some-branch-name", fg: cFg},
		{t: "▌", fg: cBlue},
		{t: "   " + "(name, #number or URL · enter to create · esc to cancel)", fg: cFaint},
	}, nil)
	if got != want {
		t.Errorf("footer with no beads loaded changed:\ngot  %q\nwant %q", got, want)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("footer with no beads loaded grew extra lines: %q", got)
	}
}

// TestNamePromptFrameHeightWithCandidates pins criterion 10: the candidate list
// is drawn as extra footer lines that shrink the body, never as something that
// grows the frame past the terminal's own height.
func TestNamePromptFrameHeightWithCandidates(t *testing.T) {
	beads := make([]bd.Bead, 0, 7)
	for i := 0; i < 7; i++ {
		beads = append(beads, bd.Bead{
			ID: fmt.Sprintf("bd-9a%d", i), Title: "Widget " + strings.Repeat("x", i+1),
			Status: "open", Type: "task",
		})
	}
	m := seededModel()
	m.feeds.beads.present, m.feeds.beads.have = true, true
	m.feeds.beads.beads = beads
	m.width, m.height = 80, 24
	m.naming = true
	m.nameBuf = "widget"
	m.refreshNameCands()
	if len(m.nameCands) != nameCandMax {
		t.Fatalf("got %d candidates, want %d", len(m.nameCands), nameCandMax)
	}

	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) != m.height {
		t.Fatalf("got %d lines with %d candidates showing, want exactly %d", len(lines), len(m.nameCands), m.height)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > m.width {
			t.Errorf("line %d width %d exceeds %d: %q", i, w, m.width, l)
		}
	}
}

// nameLink is the OSC-8 URL carried by the row segment holding name, empty when
// that segment isn't a hyperlink.
func nameLink(segs []seg, name string) string {
	for _, s := range segs {
		if strings.Contains(s.t, name) {
			return s.link
		}
	}
	return ""
}

// TestBranchNamesLinkToGitHub pins each branch name in the Branches pane as an
// OSC-8 hyperlink to that branch's page on GitHub, and pins the two cases that
// must not get one: a branch origin has never seen (its page doesn't exist, so the
// link would open a 404), and a repo whose origin isn't GitHub at all.
func TestBranchNamesLinkToGitHub(t *testing.T) {
	m := seededModel()

	if got, want := nameLink(branchRow(t, m, "feature"), "feature"), "https://github.com/owner/repo/tree/feature"; got != want {
		t.Errorf("branch on origin: link = %q, want %q", got, want)
	}
	if got := nameLink(branchRow(t, m, "wt-branch"), "wt-branch"); got != "" {
		t.Errorf("branch origin has never seen: link = %q, want none", got)
	}

	// The header names the repo and the current branch; both are links too.
	out := m.View()
	for _, want := range []string{
		"\x1b]8;;https://github.com/owner/repo\x1b\\",
		"\x1b]8;;https://github.com/owner/repo/tree/feature\x1b\\",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("header missing hyperlink %q", want)
		}
	}

	// No GitHub remote, no links anywhere.
	m.nwo = ""
	if got := nameLink(branchRow(t, m, "feature"), "feature"); got != "" {
		t.Errorf("origin isn't GitHub: link = %q, want none", got)
	}
	if strings.Contains(m.View(), "\x1b]8;;") {
		t.Errorf("origin isn't GitHub, yet the view carries an OSC-8 hyperlink")
	}
}

// TestStruckLinkStaysOneEscape pins a struck-through linked segment — a ready
// Clean Up row's name — to a single intact OSC-8 wrapper. Strikethrough makes
// lipgloss style rune by rune; styled inside the link, the escape split into
// separately colored characters and the terminal printed the URL as row text.
func TestStruckLinkStaysOneEscape(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	url := "https://github.com/owner/repo/tree/done"
	out := renderSegs([]seg{{t: "done", link: url, strike: true}}, nil)
	if !strings.HasPrefix(out, "\x1b]8;;"+url+"\x1b\\") || !strings.HasSuffix(out, "\x1b]8;;\x1b\\") {
		t.Errorf("struck link is not wrapped in one OSC-8 sequence: %q", out)
	}
	if got := vlen(out); got != len("done") {
		t.Errorf("struck link renders %d cells wide, want %d: %q", got, len("done"), out)
	}
}

// TestRowNamesLinkToGitHub pins the rest of the clickable surface: a commit's sha
// opens the commit, and a PR's or issue's title opens the same page its number
// does, so the click target is the whole "#12  Fix the thing" rather than four
// characters. Every one of them goes plain when origin isn't GitHub.
func TestRowNamesLinkToGitHub(t *testing.T) {
	const cols = 100
	m := seededModel()
	m.width = cols

	sha := m.local.commits[0]
	if got, want := nameLink(m.cmPanelLine(sha, 60), sha.SHA), "https://github.com/owner/repo/commit/abc123"; got != want {
		t.Errorf("commit sha: link = %q, want %q", got, want)
	}

	pr := m.remote.PRs[0]
	prLeft, _ := m.openPRRow(pr, m.prColumns(cols), cols)
	if got, want := nameLink(prLeft, pr.Title), "https://github.com/owner/repo/pull/10"; got != want {
		t.Errorf("PR title: link = %q, want %q", got, want)
	}

	it := m.remote.Issues[0]
	issueLeft, _ := m.issuesTab.issueRow(it, false, cols, 6, 0, 0)
	if got, want := nameLink(issueLeft, it.Title), "https://github.com/owner/repo/issues/5"; got != want {
		t.Errorf("issue title: link = %q, want %q", got, want)
	}

	// No GitHub remote, no links on any of the three.
	m.nwo, m.issuesTab.nwo = "", ""
	if got := nameLink(m.cmPanelLine(sha, 60), sha.SHA); got != "" {
		t.Errorf("origin isn't GitHub: commit sha link = %q, want none", got)
	}
	prLeft, _ = m.openPRRow(pr, m.prColumns(cols), cols)
	if got := nameLink(prLeft, pr.Title); got != "" {
		t.Errorf("origin isn't GitHub: PR title link = %q, want none", got)
	}
	issueLeft, _ = m.issuesTab.issueRow(it, false, cols, 6, 0, 0)
	if got := nameLink(issueLeft, it.Title); got != "" {
		t.Errorf("origin isn't GitHub: issue title link = %q, want none", got)
	}
}

// Focus is only worth a keypress where a row key can act. The strip shows one
// of Commits and Stashes at a time, so ⇥ steps over the hidden one, and over the
// shown one when it has no rows.
func TestTabSkipsPanesWithNoRows(t *testing.T) {
	m := seededModel()

	// Commits is showing: ⇥ alternates Branches and Commits, never Stashes.
	if got := m.nextVisiblePane(paneBranches, 1); got != paneCommits {
		t.Errorf("⇥ from Branches = %v, want paneCommits", got)
	}
	if got := m.nextVisiblePane(paneCommits, 1); got != paneBranches {
		t.Errorf("⇥ from Commits = %v, want paneBranches (Stashes is hidden)", got)
	}
	if got := m.nextVisiblePane(paneBranches, -1); got != paneCommits {
		t.Errorf("⇧⇥ from Branches = %v, want paneCommits", got)
	}

	// Switch the strip and the roles swap.
	m.historyStashes = true
	if got := m.nextVisiblePane(paneBranches, 1); got != paneStashes {
		t.Errorf("strip on Stashes: ⇥ from Branches = %v, want paneStashes", got)
	}
	if got := m.nextVisiblePane(paneStashes, 1); got != paneBranches {
		t.Errorf("strip on Stashes: ⇥ from Stashes = %v, want paneBranches", got)
	}

	// The shown list is empty: nothing in the strip to focus.
	m.local.stashes = nil
	if got := m.nextVisiblePane(paneBranches, 1); got != paneBranches {
		t.Errorf("empty Stashes shown: ⇥ from Branches = %v, want paneBranches", got)
	}
	if got := m.nextVisiblePane(paneBranches, -1); got != paneBranches {
		t.Errorf("empty Stashes shown: ⇧⇥ from Branches = %v, want paneBranches", got)
	}
}

// `c` switches the strip between Commits and Stashes, and carries focus with it
// when the cursor was in the strip.
func TestCSwitchesTheHistoryStrip(t *testing.T) {
	m := seededModel()
	m.active = tabLocal
	m.pane = paneCommits
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = next.(Model)
	if !m.historyStashes || m.pane != paneStashes {
		t.Fatalf("after c: historyStashes=%v pane=%v, want Stashes shown and focused", m.historyStashes, m.pane)
	}
	if out := stripANSI(m.View()); !strings.Contains(out, "stash@{0}") || !strings.Contains(out, "[Stashes ") {
		t.Errorf("after c the strip does not show the stashes\n%s", out)
	}

	// With focus on Branches, c switches the list and leaves focus alone.
	m.pane = paneBranches
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = next.(Model)
	if m.historyStashes || m.pane != paneBranches {
		t.Errorf("c from Branches: historyStashes=%v pane=%v, want Commits shown, focus unmoved", m.historyStashes, m.pane)
	}
}

// The strip's placeholder line for an empty list is not a row: clicking it must
// not focus the empty pane, where no row key could act.
func TestClickOnEmptyStripPlaceholderIsNoOp(t *testing.T) {
	m := seededModel()
	m.active = tabLocal
	m.local.stashes = nil
	m.historyStashes = true
	lines, hits := m.frame()
	y := -1
	for i, l := range lines {
		if strings.Contains(l, "no stashes") {
			y = i
		}
	}
	if y == -1 {
		t.Fatal("placeholder line not found in the rendered frame")
	}
	if hits[y].row != -1 {
		t.Fatalf("hit on the placeholder = %+v, want row -1", hits[y])
	}
	before := m.pane
	next, _ := m.Update(tea.MouseMsg{Y: y, X: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(Model)
	if m.pane != before {
		t.Errorf("clicking the placeholder moved focus from %v to %v", before, m.pane)
	}
}

// Switching onto an empty list from the strip cannot leave focus on it.
func TestToggleOntoEmptyListHandsFocusToBranches(t *testing.T) {
	m := seededModel()
	m.active = tabLocal
	m.local.stashes = nil
	m.pane = paneCommits
	m.toggleHistory()
	if !m.historyStashes || m.pane != paneBranches {
		t.Errorf("after toggling onto empty Stashes: historyStashes=%v pane=%v, want Stashes shown and focus on Branches", m.historyStashes, m.pane)
	}
}

// historyStripH bounds: a third of the body, never under 3 rows, never over
// historyStripMaxH, and none at all only where the body is too short for any box.
func TestHistoryStripHeight(t *testing.T) {
	for _, c := range []struct{ body, want int }{
		{3, 0}, {4, 0}, {5, 0}, {6, 3}, {8, 3}, {12, 4}, {27, 9}, {25, 8}, {60, historyStripMaxH},
	} {
		if got := historyStripH(c.body); got != c.want {
			t.Errorf("historyStripH(%d) = %d, want %d", c.body, got, c.want)
		}
	}
}

func TestFilterEmptyingTheFocusedPaneMovesFocus(t *testing.T) {
	m := seededModel()
	m.active = tabLocal

	// A filter that empties the focused Recent commits pane moves focus off
	// it, onto a pane that still has rows — here Branches, whose "feature"
	// row still matches while no commit or stash does.
	m.pane = paneCommits
	m.filter = "feature"
	m.clampCursor()
	if m.pane == paneCommits {
		t.Fatalf("filter emptied Commits but focus stayed on it")
	}
	if m.paneCount(m.pane) == 0 {
		t.Fatalf("focus landed on pane %v, which has no rows", m.pane)
	}

	// Clearing the filter restores Commits' rows but does not fling focus
	// back to it.
	moved := m.pane
	m.filter = ""
	m.clampCursor()
	if m.pane != moved {
		t.Errorf("clearing the filter moved focus from %v to %v, want unchanged", moved, m.pane)
	}

	// A filter matching nothing anywhere leaves focus where it is rather
	// than looping.
	m.pane = paneBranches
	m.local.commits = nil
	m.local.stashes = nil
	m.filter = "zzz-no-match-anywhere"
	m.clampCursor()
	if m.pane != paneBranches {
		t.Errorf("filter matching nothing anywhere moved focus to %v, want paneBranches", m.pane)
	}
}

// TestBlurredDashboardSpendsNoSyncOnThePoll pins that a poll tick that fires
// while the terminal is unfocused starts no sync yet still re-arms, and that a
// model which never received a focus event polls as before.
func TestBlurredDashboardSpendsNoSyncOnThePoll(t *testing.T) {
	m := seededModel()
	m.syncing, m.synced = false, true
	m.remote.PRsFetchedAt = time.Now().Add(-2 * data.TTL)

	blurred, _ := m.Update(tea.BlurMsg{})
	updated, cmd := blurred.Update(syncTickMsg{})
	if updated.(Model).syncing {
		t.Error("a blurred dashboard started a sync on the poll tick")
	}
	if cmd == nil {
		t.Error("the poll did not re-arm while blurred")
	}

	updated, _ = m.Update(syncTickMsg{})
	if !updated.(Model).syncing {
		t.Error("a dashboard that never saw a focus event stopped polling")
	}
}

// TestFocusRunsOneSyncOnlyWhenCacheIsStale pins the refocus path: a stale
// cache syncs once at once, a fresh one spends nothing.
func TestFocusRunsOneSyncOnlyWhenCacheIsStale(t *testing.T) {
	m := seededModel()
	m.syncing, m.synced = false, true
	m.remote.PRsFetchedAt = time.Now().Add(-2 * data.TTL)
	b, _ := m.Update(tea.BlurMsg{})
	f, cmd := b.Update(tea.FocusMsg{})
	if !f.(Model).syncing || cmd == nil {
		t.Error("refocusing with a stale cache did not start a sync")
	}
	if f.(Model).blurred {
		t.Error("FocusMsg left the model blurred")
	}

	now := time.Now()
	fresh := seededModel()
	fresh.syncing, fresh.synced = false, true
	fresh.remote.PRsFetchedAt, fresh.remote.IssuesFetchedAt, fresh.remote.MergedFetchedAt = now, now, now
	fb, _ := fresh.Update(tea.BlurMsg{})
	ff, _ := fb.Update(tea.FocusMsg{})
	if ff.(Model).syncing {
		t.Error("refocusing with a fresh cache started a sync")
	}
}

// TestFocusBeforeLaunchSyncStartsNothing pins that a focus-in arriving before the
// launch sync has been considered leaves the first sync to launchSyncCmd, which
// waits for local git.
func TestFocusBeforeLaunchSyncStartsNothing(t *testing.T) {
	m := seededModel()
	m.syncing, m.synced = false, false
	m.remote.PRsFetchedAt = time.Now().Add(-2 * data.TTL)
	f, _ := m.Update(tea.FocusMsg{})
	if f.(Model).syncing {
		t.Error("a focus-in before the launch sync started a sync")
	}
}

// TestInputResumesAPausedPoll pins the fallback for a lost focus-in: a key press
// on a blurred dashboard un-pauses the poll.
func TestInputResumesAPausedPoll(t *testing.T) {
	m := seededModel()
	m.syncing, m.synced = false, true
	m.remote.PRsFetchedAt = time.Now().Add(-2 * data.TTL)
	b, _ := m.Update(tea.BlurMsg{})
	k, _ := b.Update(tea.KeyMsg{Type: tea.KeyDown})
	if k.(Model).blurred {
		t.Error("a key press left the dashboard blurred")
	}
	if !k.(Model).syncing {
		t.Error("a key press on a blurred dashboard with a stale cache did not sync")
	}
}
