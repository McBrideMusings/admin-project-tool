package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

var me = data.Identity{Login: "pierce", Email: "pierce@example.com"}

// TestOwnershipUsesFirstCommitNotAnyCommit pins the rung that decides "yours" for a
// branch with no PR. Merging the default branch into a long-lived branch writes
// your address into its history, so an any-commit test claims every colleague's
// branch you ever helped rebase — on the repo this was built against, five of six.
// The FIRST commit is who started the work and cannot be acquired by maintenance.
func TestOwnershipUsesFirstCommitNotAnyCommit(t *testing.T) {
	mine := ownerOf(me, "", "pierce@example.com")
	if !mine.mine || mine.who != "you" {
		t.Errorf("a branch I started did not read as mine: %+v", mine)
	}
	theirs := ownerOf(me, "", "chris@example.com")
	if theirs.mine {
		t.Error("a branch chris started read as mine — a merge of main must not transfer ownership")
	}
	if theirs.who != "chris" {
		t.Errorf("owner display = %q, want the person's name from the commit address", theirs.who)
	}
}

// TestPRAuthorOutranksCommitAuthor: GitHub already decided whose PR it is, and a
// branch can carry commits from several people.
func TestPRAuthorOutranksCommitAuthor(t *testing.T) {
	got := ownerOf(me, "chris", "pierce@example.com")
	if got.mine {
		t.Error("chris's PR read as mine because my address appears in its commits")
	}
	got = ownerOf(me, "pierce", "chris@example.com")
	if !got.mine {
		t.Error("my own PR did not read as mine")
	}
}

// TestOwnerNameNeverBorrowsAnUnrelatedCommitAuthor pins the bug a review caught:
// the friendly display name came from the branch's FIRST commit unconditionally,
// even when a different person opened the PR. A branch chris's first commit
// started, whose PR austin opened, must display "austin" — not "chris", which
// would name the wrong person outright.
func TestOwnerNameNeverBorrowsAnUnrelatedCommitAuthor(t *testing.T) {
	got := ownerOf(me, "austin", "chris@example.com")
	if got.who != "austin" {
		t.Errorf("owner = %q, want the PR author's login — chris's commit does not mean chris opened this PR", got.who)
	}
	// When the names plausibly agree, the friendlier commit-derived name still wins.
	got = ownerOf(me, "chris-foreVRGames", "chris@example.com")
	if got.who != "chris" {
		t.Errorf("owner = %q, want the friendly commit-derived name when it plausibly matches the login", got.who)
	}
}

// TestUnknownOwnershipIsNeverGuessed pins the absent third rung. A branch nothing
// identifies has to say so: filed under someone by default, it is a row you have
// to un-learn, and filed under YOU it quietly claims a colleague's work.
func TestUnknownOwnershipIsNeverGuessed(t *testing.T) {
	got := ownerOf(me, "", "")
	if !got.unknown {
		t.Errorf("an unattributable branch resolved to an owner: %+v", got)
	}
	if got.mine {
		t.Error("an unattributable branch defaulted to mine")
	}
	if sec := classifySection(false, got, "", nil, nil, nil); sec != secUnsorted {
		t.Errorf("unknown ownership filed as %v, want secUnsorted", sec)
	}
}

// TestUnattributedButHeldIsYours pins the exact case a real repo hit: a branch
// with no PR and no commit of its own (so ownership never resolves) but 5
// uncommitted files sitting in the worktree — checked out on this machine, right
// now. Nothing attributes it to anyone else, so the uncommitted files are the
// strongest signal there is, and it reads as unfinished work of yours rather than
// Unsorted or, worse, something proven "empty" and safe to sweep.
func TestUnattributedButHeldIsYours(t *testing.T) {
	unknown := ownership{unknown: true}
	held := []string{"5 uncommitted"}
	if sec := classifySection(false, unknown, "empty", nil, nil, held); sec != secContinue {
		t.Errorf("unattributed + held + landed = %v, want secContinue", sec)
	}
	// The same branch with nothing in its worktree has no local evidence at all,
	// and stays genuinely unsorted.
	if sec := classifySection(false, unknown, "", nil, nil, nil); sec != secUnsorted {
		t.Errorf("unattributed + clean = %v, want secUnsorted", sec)
	}
}

// TestIdentityIsRequiredForOwnership: with no configured identity nothing can be
// yours, and the pane must say Unsorted rather than quietly calling it someone
// else's.
func TestIdentityIsRequiredForOwnership(t *testing.T) {
	got := ownerOf(data.Identity{}, "pierce", "pierce@example.com")
	if got.mine {
		t.Error("ownership resolved with no identity configured")
	}
}

// TestSectionLadderIsExclusive walks the ladder in order and checks each rule fires
// only when the ones above it did not — a branch must be in exactly one section.
func TestSectionLadderIsExclusive(t *testing.T) {
	mineOwn := ownership{who: "you", mine: true}
	theirOwn := ownership{who: "chris"}
	draft := &gh.OpenPR{IsDraft: true}
	open := &gh.OpenPR{}
	approved := &gh.OpenPR{Review: gh.ReviewApproved}

	for _, tc := range []struct {
		name      string
		protected bool
		own       ownership
		landed    string
		pr        *gh.OpenPR
		broken    []string
		holds     []string
		want      section
	}{
		{"default branch outranks everything", true, mineOwn, "merged", approved, []string{"conflicts"}, nil, secMain},
		{"landed outranks ownership", false, theirOwn, "merged", nil, nil, nil, secCleanup},
		{"unknown outranks theirs", false, ownership{unknown: true}, "", nil, nil, nil, secUnsorted},
		{"theirs outranks every mine-only rule", false, theirOwn, "", approved, []string{"conflicts"}, nil, secTheirs},
		{"broken outranks approved", false, mineOwn, "", approved, []string{"checks failing"}, nil, secFixes},
		{"approved and clean is ready to merge", false, mineOwn, "", approved, nil, nil, secMerge},
		{"open and clean is waiting", false, mineOwn, "", open, nil, nil, secWaiting},
		{"a draft is still being worked on", false, mineOwn, "", draft, nil, nil, secContinue},
		{"no PR at all is being worked on", false, mineOwn, "", nil, nil, nil, secContinue},
		{"landed but held with real uncommitted work of mine is unfinished, not cleanup",
			false, mineOwn, "empty", nil, nil, []string{"5 uncommitted"}, secContinue},
		{"landed and held but NOT mine stays in Clean Up — someone else's unsaved work is still theirs to lose",
			false, theirOwn, "empty", nil, nil, []string{"5 uncommitted"}, secCleanup},
		{"landed and mine but holds is empty stays in Clean Up — nothing to finish",
			false, mineOwn, "merged", nil, nil, nil, secCleanup},
	} {
		if got := classifySection(tc.protected, tc.own, tc.landed, tc.pr, tc.broken, tc.holds); got != tc.want {
			t.Errorf("%s: got section %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestOpenPRVetoesTheDeleteProof: whatever the commit graph says, work under review
// is not leftovers, and deleting it takes away the branch the PR points at.
func TestOpenPRVetoesTheDeleteProof(t *testing.T) {
	m := seededModel()
	m.remote.PRs = append(m.remote.PRs, gh.OpenPR{Number: 9, HeadBranch: "old-merged", Author: "pierce"})
	if got := m.landedProof("old-merged", true); got != "" {
		t.Errorf("a branch with an open PR claimed a delete proof: %q", got)
	}
}

// TestOpenPRMatchesABranchPushedUnderAnotherName pins the three ways a local
// branch finds its PR when its name is not the PR's head — a repo that keeps
// tracker ids out of pushed names has every PR in this shape — and that a PR whose
// head IS another local branch stays on that branch alone.
func TestOpenPRMatchesABranchPushedUnderAnotherName(t *testing.T) {
	m := seededModel()
	m.local.branches = append(m.local.branches,
		git.BranchInfo{Name: "feat/x-9ee.1-seed"},
		git.BranchInfo{Name: "fix/x-9ee.2-rating"},
		git.BranchInfo{Name: "spike-tracks-feature"},
	)
	m.local.tips = map[string]string{"feat/x-9ee.1-seed": "4e9dff59", "spike-tracks-feature": "aaaa"}
	m.local.signals["fix/x-9ee.2-rating"] = gitSignals{upstream: "fix/rating"}
	m.local.signals["spike-tracks-feature"] = gitSignals{upstream: "feature"}
	m.local.signals["feat/x-9ee.1-seed"] = gitSignals{upstream: "main"}
	m.remote.PRs = append(m.remote.PRs,
		gh.OpenPR{Number: 287, HeadBranch: "feat/dev-seed", HeadOID: "4e9dff59"},
		gh.OpenPR{Number: 286, HeadBranch: "fix/rating", HeadOID: "d811c7a9"},
	)
	for branch, want := range map[string]int{
		"feat/x-9ee.1-seed":    287, // by tip, though its upstream is origin/main
		"fix/x-9ee.2-rating":   286, // by upstream
		"spike-tracks-feature": 0,   // tracks `feature`, which is a local branch holding PR #10
		"feature":              10,  // by name
	} {
		got := 0
		if pr := m.openPRFor(branch); pr != nil {
			got = pr.Number
		}
		if got != want {
			t.Errorf("openPRFor(%q) = #%d, want #%d", branch, got, want)
		}
		if m.hasOpenPR(branch) != (want != 0) {
			t.Errorf("hasOpenPR(%q) disagrees with openPRFor", branch)
		}
	}
}

// TestStackedBaseTakesTheNearestPROnlyWithoutItsOwn pins stacked PRs: a branch
// whose commits all sit inside an open PR's head — the base of a stack — shows
// the nearest such PR when it has none of its own, and keeps its own when it
// does, so two branches meant to be reviewed separately show separate PRs.
func TestStackedBaseTakesTheNearestPROnlyWithoutItsOwn(t *testing.T) {
	m := seededModel()
	m.local.branches = append(m.local.branches,
		git.BranchInfo{Name: "mid-tier"}, git.BranchInfo{Name: "top"}, git.BranchInfo{Name: "base-with-pr"})
	m.local.containedIn = map[string]map[string]int{
		"mid-tier":     {"top": 34, "top-of-top": 50},
		"base-with-pr": {"top": 3},
	}
	m.remote.PRs = append(m.remote.PRs,
		gh.OpenPR{Number: 278, HeadBranch: "top"},
		gh.OpenPR{Number: 300, HeadBranch: "top-of-top"},
		gh.OpenPR{Number: 277, HeadBranch: "base-with-pr"},
	)
	for branch, want := range map[string]int{
		"mid-tier":     278, // contained in two PRs; #278 is 34 commits above it, #300 is 50
		"base-with-pr": 277, // its own PR, never the one stacked on it
		"top":          278,
	} {
		got := 0
		if pr := m.openPRFor(branch); pr != nil {
			got = pr.Number
		}
		if got != want {
			t.Errorf("openPRFor(%q) = #%d, want #%d", branch, got, want)
		}
	}
}

// TestBlockersAreMeasuredIndependently pins the collapse behind "Fixes Needed":
// four different measurements, all reported, because they co-occur and hiding one
// behind another sends you back for a second look.
func TestBlockersAreMeasuredIndependently(t *testing.T) {
	pr := &gh.OpenPR{Review: gh.ReviewChangesRequested, Checks: gh.ChecksFailing, Mergeable: "CONFLICTING"}
	got := blockers(pr, "", 0)
	for _, want := range []string{"changes", "checks failing", "conflicts"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("blockers %v is missing %q", got, want)
		}
	}
	if paused := blockers(nil, "rebase", 0); len(paused) != 1 || paused[0] != "rebasing" {
		t.Errorf("a paused rebase is not reported as a blocker: %v", paused)
	}
}

// TestReadinessIgnoresTheClock: nothing becomes deletable by sitting still. The
// only clock left withholds (see the "active" hold), it never authorises. The
// verdict is not drawn; ⇧X sweeps the Clean Up rows it calls "ready".
func TestReadinessIgnoresTheClock(t *testing.T) {
	if readyWord("", nil) != "keep" {
		t.Error("a branch with no proof was not kept")
	}
	if readyWord("merged", nil) != "ready" {
		t.Error("a proven-merged branch with nothing held was not ready")
	}
	if readyWord("merged", []string{"3 uncommitted"}) != "held" {
		t.Error("uncommitted files did not hold a merged branch")
	}
}

// TestPurposeLadderPrefersIntentOverDescription. A branch note is somebody saying
// what this is for; a PR title describes a change; a commit subject is only the
// last thing that happened. They must not be interchangeable.
func TestPurposeLadderPrefersIntentOverDescription(t *testing.T) {
	m := seededModel()
	m.local.branchNote = map[string]string{"feature": "spike the wallet read API"}
	m.local.lastSubject = map[string]string{"feature": "wip"}

	if got := m.purposeOf("feature", "feat: something else", 0); got.src != "note" {
		t.Errorf("a branch note lost to a PR title: %+v", got)
	}
	delete(m.local.branchNote, "feature")
	if got := m.purposeOf("feature", "feat: something else", 0); got.src != "pr" {
		t.Errorf("a PR title lost to a commit subject: %+v", got)
	}
	if got := m.purposeOf("feature", "", 0); got.src != "commit" || got.text != "wip" {
		t.Errorf("the commit subject fallback did not fire: %+v", got)
	}
	m.local.lastSubject = map[string]string{}
	if got := m.purposeOf("feature", "", 0); got.text != "" {
		t.Errorf("purpose invented a sentence from nothing: %+v", got)
	}
}

// TestSweepCountMatchesSweepBatch pins the bug a real repo hit: the footer's "N
// clean up" promised more than ⇧X actually swept, because the footer counted
// every row in the Clean Up SECTION (which includes `held` rows) while the sweep
// itself filtered by a completely separate, older classifier. The two must be the
// same count, built from the same rows, every time.
func TestSweepCountMatchesSweepBatch(t *testing.T) {
	m := seededModel()
	m.remote.MergedHeads["old-merged"] = "abc123"
	// A branch fully pushed to origin (a proof under the old classifier) but with an
	// OPEN PR attached — the case that diverged. It must not be swept.
	m.local.branches = append(m.local.branches, git.BranchInfo{
		Name: "under-review", HasRemote: true, Ahead: 0, LastCommit: "1 hour ago",
	})
	m.remote.PRs = append(m.remote.PRs, gh.OpenPR{
		Number: 99, HeadBranch: "under-review", Author: "me", Bucket: gh.BucketMine,
	})

	m.active, m.pane = tabLocal, paneBranches
	before, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	updated := before.(Model)
	if !updated.confirming {
		t.Fatal("⇧X did not stage a batch")
	}
	swept := len(updated.confirmTargets)

	// The footer's count is computed the same way inside renderLocalPanels; recompute
	// it here the way a reader of the rendered pane would, and it must equal the batch.
	want := len(m.pruneTargets())
	for _, r := range m.branchRows() {
		if r.sec == secCleanup && r.ready == "ready" {
			want++
		}
	}
	if swept != want {
		t.Errorf("⇧X swept %d, footer's own count says %d — they must always agree", swept, want)
	}
	for _, tgt := range updated.confirmTargets {
		if tgt.branch == "under-review" {
			t.Error("a branch with an open PR was swept by ⇧X")
		}
	}
}

// TestReviewingNameColorIsDistinctFromDeadGray pins the color the last round
// changed: a co-contributor's branch under review must not read the same gray as
// a branch already proven safe to delete — those are opposite facts. Only rows
// classifySection actually filed under Reviewing (secTheirs) get the distinct
// color; an unattributed-but-held branch that landed in Continue Working must not
// pick it up just because ownership couldn't resolve a name.
func TestReviewingNameColorIsDistinctFromDeadGray(t *testing.T) {
	m := seededModel()
	m.remote.PRs = append(m.remote.PRs, gh.OpenPR{
		Number: 50, HeadBranch: "old-merged", Author: "chris", Bucket: gh.BucketAssigned,
	})
	m.remote.MergedHeads = map[string]string{}

	rows := m.branchRows()
	var reviewing paneRow
	found := false
	for _, r := range rows {
		if r.name == "old-merged" {
			reviewing, found = r, true
		}
	}
	if !found || reviewing.sec != secTheirs {
		t.Fatalf("fixture setup: expected old-merged in Reviewing, got sec=%v found=%v", reviewing.sec, found)
	}
	row := m.brPanelLine(reviewing, m.branchInfo("old-merged"), 90, columnPlan{})
	name := row[2] // glyph, " ", name — see brPanelLine's `left` slice (gutterSegs + nameSeg)
	if name.fg != cSlate {
		t.Errorf("a Reviewing row's name is not cSlate: %v", name.fg)
	}
	if name.fg == cFaint {
		t.Error("a Reviewing row's name is the same gray as a finished Clean Up row")
	}
}

// TestMultiSelectDeletesAMixedBatch pins the capability the redesign lost and this
// pass restored: picking two branches from DIFFERENT sections and confidence
// tiers, then deleting them together in one confirm — something neither a single
// `x` (one row) nor `⇧X` (every proven-ready row, unconditionally) can express.
func TestMultiSelectDeletesAMixedBatch(t *testing.T) {
	m := seededModel()
	m.remote.MergedHeads["old-merged"] = "abc123"
	m.local.branches = append(m.local.branches,
		git.BranchInfo{Name: "eyeballed", HasRemote: true, Ahead: 2, LastCommit: "40 days ago"})
	m.local.branchUnsafe["eyeballed"] = true
	m.active, m.pane = tabLocal, paneBranches

	toggle := func(name string) Model {
		found := false
		for i, r := range m.branchRows() {
			if r.name == name {
				m.paneCursor[paneBranches], found = i, true
			}
		}
		if !found {
			t.Fatalf("branch %q is not in the pane's rows", name)
		}
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
		return updated.(Model)
	}
	m = toggle("old-merged")
	m = toggle("eyeballed")
	if !m.selected["old-merged"] || !m.selected["eyeballed"] {
		t.Fatalf("selection did not record both branches: %+v", m.selected)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(Model)
	if !m.confirming || len(m.confirmTargets) != 2 {
		t.Fatalf("expected a 2-item batch, got confirming=%v targets=%+v", m.confirming, m.confirmTargets)
	}
	got := map[string]bool{}
	for _, tg := range m.confirmTargets {
		got[tg.branch] = true
	}
	if !got["old-merged"] || !got["eyeballed"] {
		t.Errorf("batch is missing a selected branch: %+v", m.confirmTargets)
	}
}

// TestSelectionRefusesCurrentAndDefault mirrors the single-row refusal: `space`
// on the branch you're standing on, or the default branch, must not enter the
// selection at all — and must leave select mode exactly as it found it, so the
// pane never latches into a widened gutter with nothing selected.
func TestSelectionRefusesCurrentAndDefault(t *testing.T) {
	for _, name := range []string{"feature", "main"} { // current branch, then default
		m := seededModel()
		m.local.branches = append(m.local.branches,
			git.BranchInfo{Name: "main", HasRemote: true, LastCommit: "2 hours ago"})
		m.active, m.pane = tabLocal, paneBranches
		found := false
		for i, r := range m.branchRows() {
			if r.name == name {
				m.paneCursor[paneBranches], found = i, true
			}
		}
		if !found {
			t.Fatalf("branch %q is not in the pane's rows", name)
		}
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
		m = updated.(Model)
		if m.selected[name] {
			t.Errorf("%s entered the selection", name)
		}
		if m.selectMode() {
			t.Errorf("space on %s entered select mode with %d rows selected", name, len(m.selected))
		}
	}
}

// TestSelectModeLeavesWhenTheLastRowIsDeselected is the other half of #25: with
// select mode being the selection itself, `space` on the one selected row drops
// straight back out of the mode.
func TestSelectModeLeavesWhenTheLastRowIsDeselected(t *testing.T) {
	m := seededModel()
	m.remote.MergedHeads["old-merged"] = "abc123"
	m.active, m.pane = tabLocal, paneBranches
	for i, r := range m.branchRows() {
		if r.name == "old-merged" {
			m.paneCursor[paneBranches] = i
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m = updated.(Model)
	if !m.selectMode() || !m.selected["old-merged"] {
		t.Fatalf("space on a selectable row did not select it: %+v", m.selected)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m = updated.(Model)
	if m.selectMode() {
		t.Errorf("deselecting the last row left select mode on: %+v", m.selected)
	}
}

// TestPurposeSublineStopsAtThePRColumn pins the width cap on the row below each
// branch: it stops no later than where the PR cell's right edge falls on the row
// above, whatever width the pane renders at, so it never runs on under the git and
// age cells and reads as part of them.
func TestPurposeSublineStopsAtThePRColumn(t *testing.T) {
	m := seededModel()
	m.local.branchNote = map[string]string{
		"feature": "a purpose sentence long enough to reach the far right edge of a wide pane if nothing capped it",
	}
	for w := 60; w <= 160; w += 11 {
		rows := m.branchRows()
		// The same pane-wide plan branchPaneLines draws with.
		plan := planColumns(rows)
		for _, r := range rows {
			// branchPaneLines draws the subline only when the purpose is not inline.
			if m.branchRowLayout(r, w, plan).purposeW > 0 {
				continue
			}
			sub := m.brSubLine(r, w, plan)
			if sub == nil {
				continue
			}
			end := m.prColumnEnd(r, w, plan)
			if got := len([]rune(segsText(sub))); got > end {
				t.Errorf("width %d: %s subline is %d cells, past the PR cell's edge at %d",
					w, r.name, got, end)
			}
		}
	}
}

// TestOwnerOfGHDerivedIdentity pins ownership over an identity as `gh api user`
// reports it: the PR-author rung matches the login, and the first-commit rung
// matches both the public email and the noreply address built from id and login.
func TestOwnerOfGHDerivedIdentity(t *testing.T) {
	id := data.Identity{Login: "Octocat", Email: "pierce@example.com", ID: 4242}
	noreply := "4242+octocat@users.noreply.github.com"

	for _, first := range []string{"pierce@example.com", noreply} {
		if got := ownerOf(id, "", first); !got.mine || got.via != "first commit" {
			t.Errorf("ownerOf(first=%q) = %+v, want mine via first commit", first, got)
		}
	}
	if got := ownerOf(id, "octocat", "chris@example.com"); !got.mine || got.via != "pr author" {
		t.Errorf("ownerOf(pr author = my login) = %+v, want mine via pr author", got)
	}
	if got := ownerOf(id, "", "99+chris@users.noreply.github.com"); got.mine || got.unknown {
		t.Errorf("ownerOf(someone else's noreply) = %+v, want theirs", got)
	}
	if got := ownerOf(data.Identity{}, "pierce", "pierce@example.com"); got.mine {
		t.Errorf("ownerOf with no identity claimed a branch: %+v", got)
	}
}

// TestIdentityMsgReplacesAndClearsIdentity pins that the fetch result is applied
// as-is: a later failure (the zero identity) returns ownership to unknown instead
// of keeping the previous account.
func TestIdentityMsgReplacesAndClearsIdentity(t *testing.T) {
	m := seededModel()
	got, _ := m.Update(identityMsg{id: data.Identity{Login: "pierce", ID: 1}})
	if !got.(Model).identity.Known() {
		t.Fatal("a fetched identity was not applied")
	}
	got, _ = got.(Model).Update(identityMsg{})
	if got.(Model).identity.Known() {
		t.Error("a failed fetch left the old identity in force")
	}
}

// TestRenderLocalPanelsFillsBodyHeight pins that the Repo tab draws exactly as many
// lines, and as many hit targets, as the body is tall, with either history list
// showing. A body a line too tall or short shifts the footer or leaves a click
// resolving to nothing.
func TestRenderLocalPanelsFillsBodyHeight(t *testing.T) {
	for _, stashes := range []bool{false, true} {
		for height := 1; height <= 12; height++ {
			m := seededModel()
			m.historyStashes = stashes
			lines, hits := m.renderLocalPanels(80, height)
			if len(lines) != height {
				t.Errorf("stashes=%v height=%d: rendered %d lines", stashes, height, len(lines))
			}
			if len(hits) != height {
				t.Errorf("stashes=%v height=%d: built %d hit targets", stashes, height, len(hits))
			}
		}
	}
}

// TestPRMarkMostUrgentWins pins the one-mark rule: each case adds the next more
// urgent state to a PR that already carries every less urgent one, so the mark
// must change at every step.
func TestPRMarkMostUrgentWins(t *testing.T) {
	pr := gh.OpenPR{Review: gh.ReviewInProgress}
	steps := []struct {
		apply func()
		want  string
	}{
		{func() {}, "○"},
		{func() { pr.Review = gh.ReviewApproved }, "●"},
		{func() { pr.IsDraft = true }, "◌"},
		{func() { pr.Checks = gh.ChecksPending }, "◷"},
		{func() { pr.Review = gh.ReviewNeedsResponse }, "◐"},
		{func() { pr.Review = gh.ReviewChangesRequested }, "⊗"},
		{func() { pr.Checks = gh.ChecksFailing }, "✗"},
		{func() { pr.Mergeable = "CONFLICTING" }, "≠"},
	}
	for _, s := range steps {
		s.apply()
		if got := prMark(&pr).t; got != s.want {
			t.Errorf("prMark(%+v) = %q, want %q", pr, got, s.want)
		}
	}
}
