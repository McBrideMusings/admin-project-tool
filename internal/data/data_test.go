package data

import (
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
)

// TestStaleTakesTheOldestSection pins that the cache is judged stale as soon as
// any one section is past TTL. One sync refreshes all three together, so a cache
// whose merged set is an hour old is a cache worth re-fetching, however recently
// the PR list was patched by a row-scoped refresh.
func TestStaleTakesTheOldestSection(t *testing.T) {
	now := time.Now()
	fresh := Remote{PRsFetchedAt: now, IssuesFetchedAt: now, MergedFetchedAt: now}
	if fresh.Stale(true) {
		t.Error("a just-synced cache reported stale")
	}

	oneOld := fresh
	oneOld.MergedFetchedAt = now.Add(-2 * TTL)
	if !oneOld.Stale(true) {
		t.Error("a cache with one section past TTL should be stale — the sync refreshes all three, so the oldest decides")
	}

	var never Remote
	if !never.Stale(true) {
		t.Error("a never-fetched cache should be stale")
	}
}

// TestStaleIgnoresIssuesInABeadsRepo pins that a repo tracking its work in beads
// isn't dragged into a sync by the issue section it never fetches. That section's
// stamp stays zero forever there, so counting it would make every launch spend a
// round of `gh` calls the cache already had answers for.
func TestStaleIgnoresIssuesInABeadsRepo(t *testing.T) {
	now := time.Now()
	beadsRepo := Remote{PRsFetchedAt: now, MergedFetchedAt: now} // issues never fetched
	if beadsRepo.Stale(false) {
		t.Error("a beads-backed repo's fresh cache reported stale over the issue section it doesn't use")
	}
	if !beadsRepo.Stale(true) {
		t.Error("the same cache in a GitHub-issues repo should be stale — its issue section was never fetched")
	}
}

// TestNotAttemptedIssueFetchLeavesTheCacheAlone pins the other half: in a beads
// repo the sync returns an issue fragment with neither a stamp nor an error, and
// applying it must not blank whatever issues the cache still holds from before.
func TestNotAttemptedIssueFetchLeavesTheCacheAlone(t *testing.T) {
	cached := Remote{
		Issues:          []gh.IssueItem{{Number: 7, Title: "still here"}},
		IssuesFetchedAt: time.Now(),
	}
	before := cached.IssuesFetchedAt
	var notAttempted Remote
	notAttempted.applyIssuesTo(&cached)
	if len(cached.Issues) != 1 {
		t.Errorf("an issue fetch that never ran wiped the cached issues: got %d, want 1", len(cached.Issues))
	}
	if !cached.IssuesFetchedAt.Equal(before) {
		t.Error("an issue fetch that never ran reset the cached section's timestamp")
	}
}

// TestSectionFragmentsComposeWithoutClobbering pins how FetchAll assembles its
// three concurrent fetches. Each returns a Remote carrying only its own section,
// so composing them with the whole-result ApplyTo silently writes the issue
// fragment's zero PR timestamp over the PR fetch that just succeeded — the PR and
// Issue lists then render as never-fetched (a bare "cached" in the header, no age)
// while the merged set, applied last, looks fine.
func TestSectionFragmentsComposeWithoutClobbering(t *testing.T) {
	prs := Remote{PRsFetchedAt: time.Unix(100, 0)}
	issues := Remote{IssuesFetchedAt: time.Unix(200, 0), ViewerLogin: "me"}
	merged := Remote{MergedHeads: map[string]string{"x": "sha-x"}, MergedFetchedAt: time.Unix(300, 0)}

	var r Remote
	prs.applyPRsTo(&r)
	issues.applyIssuesTo(&r)
	merged.applyMergedTo(&r)

	if !r.PRsFetchedAt.Equal(time.Unix(100, 0)) {
		t.Errorf("the PR section's stamp was lost composing the fragments: %v", r.PRsFetchedAt)
	}
	if !r.IssuesFetchedAt.Equal(time.Unix(200, 0)) || r.ViewerLogin != "me" {
		t.Errorf("the issue section did not survive composition: %v / %q", r.IssuesFetchedAt, r.ViewerLogin)
	}
	if !r.MergedFetchedAt.Equal(time.Unix(300, 0)) || r.MergedHeads["x"] == "" {
		t.Errorf("the merged section did not survive composition: %v / %v", r.MergedFetchedAt, r.MergedHeads)
	}
}

// TestApplyToKeepsCachedRowsWhenASectionFails pins the rule that makes one sync
// safe: a section whose gh call errored contributes its error and nothing else.
// Without this, one timed-out call would empty the list on screen — every open PR
// gone because the network hiccuped — and stamp the empty result as current.
func TestApplyToKeepsCachedRowsWhenASectionFails(t *testing.T) {
	cached := Remote{
		MergedHeads:     map[string]string{"squash-merged": "sha-sq"},
		MergedFetchedAt: time.Unix(100, 0),
		IssuesFetchedAt: time.Unix(100, 0),
		PRsFetchedAt:    time.Unix(100, 0),
	}

	// A sync where merged failed and the others came back clean.
	src := Remote{
		MergedFetchError: "gh: connection reset",
		PRsFetchedAt:     time.Unix(200, 0),
		IssuesFetchedAt:  time.Unix(200, 0),
	}
	got := cached
	src.ApplyTo(&got)

	if got.MergedHeads["squash-merged"] == "" {
		t.Error("a failed merged fetch blanked the cached set; every squash-merged branch would read as live")
	}
	if !got.MergedFetchedAt.Equal(time.Unix(100, 0)) {
		t.Errorf("a failed section moved its clock forward: %v", got.MergedFetchedAt)
	}
	if got.MergedFetchError == "" {
		t.Error("the failure was not recorded, so the header can't report it")
	}
	if !got.PRsFetchedAt.Equal(time.Unix(200, 0)) {
		t.Error("a clean section did not take the new stamp")
	}

	// The next clean sync adds to the set and clears the error. It accumulates
	// rather than replaces: each fetch reads a fixed window of the most recent
	// merged PRs, so replacing would drop a branch that merged before the window
	// and hand back the exact "squash-merged branch reads as live" bug this set
	// exists to prevent — just delayed by a couple hundred PRs instead of by a
	// failed fetch.
	next := Remote{
		MergedHeads:     map[string]string{"other": "sha-other"},
		MergedFetchedAt: time.Unix(300, 0),
		PRsFetchedAt:    time.Unix(300, 0),
		IssuesFetchedAt: time.Unix(300, 0),
	}
	next.ApplyTo(&got)
	if got.MergedHeads["squash-merged"] == "" {
		t.Errorf("a clean fetch dropped a head that fell out of the window: %v", got.MergedHeads)
	}
	if got.MergedHeads["other"] == "" {
		t.Errorf("a clean fetch did not add the new head: %v", got.MergedHeads)
	}
	if got.MergedFetchError != "" {
		t.Error("a clean fetch left the old error behind; the header would keep saying sync failed")
	}
}

// TestClosedHeadsAccumulate pins that the closed-unmerged set rides the same
// accumulate-don't-replace rule as MergedHeads, and for the same reason: a PR
// closed three hundred PRs ago is still closed, and the abandoned branch it names
// must not read as live again once it slides out of the fetch window.
func TestClosedHeadsAccumulate(t *testing.T) {
	got := Remote{ClosedHeads: map[string]string{"declined-long-ago": "aaa111"}}
	next := Remote{
		ClosedHeads:     map[string]string{"declined-today": "bbb222"},
		MergedFetchedAt: time.Unix(300, 0),
	}
	next.ApplyTo(&got)
	if got.ClosedHeads["declined-long-ago"] != "aaa111" || got.ClosedHeads["declined-today"] != "bbb222" {
		t.Errorf("closed heads did not accumulate: %v", got.ClosedHeads)
	}
}

// TestIndexMergedKeepsNewestMergePerHead pins that when two merged PRs share a head
// name, the newer merge (listed first) keeps its OID and author.
func TestIndexMergedKeepsNewestMergePerHead(t *testing.T) {
	heads, authors, _ := indexMerged([]gh.MergedPR{
		{HeadBranch: "refactor/x", HeadOID: "dfaf2b6f", Author: "new"},
		{HeadBranch: "refactor/x", HeadOID: "4352055d", Author: "old"},
	})
	if heads["refactor/x"] != "dfaf2b6f" || authors["refactor/x"] != "new" {
		t.Errorf("got oid %q author %q, want dfaf2b6f / new", heads["refactor/x"], authors["refactor/x"])
	}
}
