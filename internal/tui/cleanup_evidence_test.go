package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// staleModel is a repo holding one branch of each kind the buckets have to tell
// apart:
//
//   - old-merged — provably merged, nothing holding it: safe, sweepable.
//   - abandoned — never pushed, five weeks idle, 900 behind. This is the shape the
//     evidence tier was built for, and the realistic version of it: a branch that
//     was never pushed and has commits necessarily holds unpushed commits, so it
//     is ALSO quarantined. Held and likely are independent axes and this row is
//     both.
//   - declined — its PR was closed unmerged, but everything is on origin, so
//     nothing is held. This is what lands in the likely bucket un-held.
//   - main — protected.
func staleModel() Model {
	m := seededModel()
	m.local.branches = []git.BranchInfo{
		{Name: "main", HasRemote: true, LastCommit: "1 hour ago"},
		{Name: "old-merged", HasRemote: true, LastCommit: "3 days ago"},
		{Name: "abandoned", LastCommit: "5 weeks ago"},
		{Name: "declined", HasRemote: true, LastCommit: "3 weeks ago"},
	}
	m.local.wts = nil
	m.local.signals = map[string]gitSignals{
		"old-merged": {fullyPushed: true},
		"abandoned":  {neverPushed: true, idle: 5 * week, behind: 900},
		// Deliberately NOT fullyPushed: that is a proof ("on origin") and would
		// short-circuit before the evidence tier ever ran. This is a branch origin
		// carries but which holds commits origin has not seen.
		"declined": {idle: 3 * week, behind: 400},
	}
	// A never-pushed branch with commits has them nowhere else — this is what
	// quarantines it, and leaving it out of the fixture would test a branch that
	// cannot exist.
	m.local.branchUnsafe = map[string]bool{"abandoned": true}
	m.local.touched = map[string]time.Time{}
	m.remote = data.Remote{
		MergedHeads: map[string]string{"old-merged": "abc123"},
		// No matching local tip is set for "declined" (m.local.tips is nil here), so
		// closedTipMatch stays false and this PR's closure stays circumstantial —
		// exactly the "declined" fixture this test wants: on origin, PR closed
		// unmerged, everything backed up, nothing held.
		ClosedHeads: map[string]string{"declined": "zzz999"},
	}
	return m
}

// TestLikelyStaleIsSurfacedNotSwept is the whole point of the evidence tier, in
// one test. The abandoned branch must appear in Cleanup — before this it produced
// no signal at all and was simply absent — and a batch sweep must still refuse to
// take it, because nothing proved it dead.
func TestLikelyStaleIsSurfacedNotSwept(t *testing.T) {
	m := staleModel()
	set := m.repo().cleanupTargets()

	// The never-pushed orphan appears at all — before the evidence tier it emitted
	// no signal, so it was not a cleanup target and never rendered anywhere. It
	// lands in quarantine rather than likely, because its commits exist nowhere
	// else; both facts about it are true at once.
	abandoned, ok := set.byBranch()["abandoned"]
	if !ok {
		t.Fatal("the never-pushed orphan is not a cleanup target at all — the tier did nothing")
	}
	if abandoned.conf != confLikely {
		t.Errorf("conf = %v, want confLikely", abandoned.conf)
	}
	if !abandoned.quarantined {
		t.Error("a branch whose commits are nowhere else must be held, not offered as safe")
	}
	if abandoned.sweepable() {
		t.Error("a likely-stale target reported itself sweepable; ⇧X would delete it on a guess")
	}

	// The un-held guess is the one that fills the likely bucket.
	var likely []string
	for _, tgt := range set.likely {
		likely = append(likely, tgt.branch)
	}
	if len(likely) != 1 || likely[0] != "declined" {
		t.Fatalf("likely bucket = %v, want [declined]", likely)
	}
	if set.likely[0].sweepable() {
		t.Error("an un-held guess is still a guess; a sweep must not take it")
	}

	// The proven-dead one is in the safe bucket and IS sweepable.
	if len(set.mergedBranches) != 1 || set.mergedBranches[0].branch != "old-merged" {
		t.Fatalf("safe bucket = %v, want [old-merged]", set.mergedBranches)
	}
	if !set.mergedBranches[0].sweepable() {
		t.Error("a proven-merged, unheld target is not sweepable; ⇧X would skip real cleanup")
	}

	// And the sweep itself takes only the proven one.
	m.repo().cleanAll()
	got := m
	if len(got.confirmTargets) != 1 || got.confirmTargets[0].branch != "old-merged" {
		var names []string
		for _, tgt := range got.confirmTargets {
			names = append(names, tgt.branch)
		}
		t.Errorf("cleanAll staged %v, want only [old-merged]", names)
	}
}

// TestLikelyStaleRowShowsItsEvidence pins that a guess explains itself. "stale"
// alone is the dashboard asserting a mood — the reader can only judge it from the
// signals that fired, so the row has to carry them.
func TestLikelyStaleRowShowsItsEvidence(t *testing.T) {
	m := staleModel()
	set := m.repo().cleanupTargets()
	if len(set.likely) == 0 {
		t.Fatal("no likely target to inspect")
	}
	detail := cleanupDetail(set.likely[0])
	for _, want := range []string{"PR closed", "idle", "400 behind"} {
		if !strings.Contains(detail, want) {
			t.Errorf("evidence line %q does not mention %q", detail, want)
		}
	}

	// A held row keeps its hold word first — that is the safety fact — but must
	// still carry evidence after it. "unpushed" alone is equally true of this
	// morning's work, so on its own it gives the reader nothing to judge by.
	held := cleanupDetail(set.byBranch()["abandoned"])
	if !strings.HasPrefix(held, "unpushed") {
		t.Errorf("held row %q does not lead with the hold reason", held)
	}
	// It must carry the idle measure, not a restatement of the hold: "unpushed ·
	// never pushed" spends the column saying the same thing twice, and how long the
	// branch has been ignored is the part "unpushed" cannot tell you.
	if !strings.Contains(held, "idle") {
		t.Errorf("held row %q carries no idleness; it reads like active work", held)
	}
	if strings.Contains(held, "never pushed") {
		t.Errorf("held row %q restates its own hold reason", held)
	}

	// A proven-dead row has no evidence line: its reason word is the explanation.
	if d := cleanupDetail(set.mergedBranches[0]); d != "" {
		t.Errorf("a certain verdict carried an evidence line %q", d)
	}
}

// TestProtectedBranchNeverEntersCleanup pins that the default branch stays out of
// every bucket even when it is quiet and fully pushed — the condition that would
// otherwise let the evidence tier condemn it.
func TestProtectedBranchNeverEntersCleanup(t *testing.T) {
	m := staleModel()
	m.local.signals["main"] = gitSignals{fullyPushed: true, mergedLocal: true, idle: 9 * week, behind: 0}
	for _, tgt := range m.repo().cleanupTargets().all() {
		if tgt.branch == "main" {
			t.Fatalf("the default branch entered Cleanup as %q", tgt.reason)
		}
	}
}
