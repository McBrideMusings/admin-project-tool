package git

import (
	"context"
	"path/filepath"
	"testing"
)

// setupOriginRepo creates a bare origin plus a clone with one commit on main,
// pushed with an upstream, and returns the clone's dir.
func setupOriginRepo(t *testing.T) string {
	t.Helper()
	origin := t.TempDir()
	gitT(t, origin, "init", "-q", "--bare", "-b", "main")

	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "init")
	gitT(t, dir, "remote", "add", "origin", origin)
	gitT(t, dir, "push", "-q", "-u", "origin", "main")
	return dir
}

func TestClassifyDeadFullyPushedIsDead(t *testing.T) {
	dir := setupOriginRepo(t)
	commitOn(t, dir, "on-origin")
	gitT(t, dir, "push", "-q", "-u", "origin", "on-origin")
	gitT(t, dir, "switch", "-q", "main")
	t.Chdir(dir)
	ctx := context.Background()

	branches, err := LocalBranches(ctx, "main", nil)
	if err != nil {
		t.Fatalf("LocalBranches: %v", err)
	}
	var cand []BranchInfo
	for _, b := range branches {
		if b.Name == "on-origin" {
			cand = append(cand, b)
		}
	}
	if len(cand) != 1 {
		t.Fatalf("expected exactly one candidate, got %d", len(cand))
	}

	dead, survivors := ClassifyDead(ctx, cand, "main", nil)
	if len(dead) != 1 || dead[0].Reason != "on origin" {
		t.Fatalf("dead = %+v, want one branch reasoned \"on origin\"", dead)
	}
	if len(survivors) != 0 {
		t.Fatalf("survivors = %+v, want none", survivors)
	}
}

func TestClassifyDeadUnpushedCommitsSurvive(t *testing.T) {
	dir := setupOriginRepo(t)
	commitOn(t, dir, "never-pushed")
	gitT(t, dir, "switch", "-q", "main")
	t.Chdir(dir)
	ctx := context.Background()

	branches, err := LocalBranches(ctx, "main", nil)
	if err != nil {
		t.Fatalf("LocalBranches: %v", err)
	}
	var cand []BranchInfo
	for _, b := range branches {
		if b.Name == "never-pushed" {
			cand = append(cand, b)
		}
	}

	dead, survivors := ClassifyDead(ctx, cand, "main", nil)
	if len(dead) != 0 {
		t.Fatalf("dead = %+v, want none — a branch with unpushed commits must never be offered", dead)
	}
	if len(survivors) != 1 {
		t.Fatalf("survivors = %+v, want the one unpushed branch", survivors)
	}
}

func TestClassifyDeadMergedPRWithNoFurtherCommitsIsDead(t *testing.T) {
	// The squash/rebase case: gh says the PR merged, git's own --merged check
	// can't see it (no non-squashed commit is reachable from main), and the
	// branch carries no work beyond what merged.
	dir := setupOriginRepo(t)
	commitOn(t, dir, "squash-merged")
	gitT(t, dir, "push", "-q", "-u", "origin", "squash-merged")
	gitT(t, dir, "switch", "-q", "main")
	t.Chdir(dir)
	ctx := context.Background()

	branches, err := LocalBranches(ctx, "main", nil)
	if err != nil {
		t.Fatalf("LocalBranches: %v", err)
	}
	var cand []BranchInfo
	for _, b := range branches {
		if b.Name == "squash-merged" {
			cand = append(cand, b)
		}
	}

	dead, _ := ClassifyDead(ctx, cand, "main", map[string]bool{"squash-merged": true})
	if len(dead) != 1 {
		t.Fatalf("dead = %+v, want the PR-merged branch", dead)
	}
	// "on origin" would also be a true statement here (fully pushed, not ahead),
	// but ClassifyDead must reach the PR-merged case first per its documented
	// precedence, not fall through to a later, weaker reason.
	if dead[0].Reason != "PR merged" {
		t.Fatalf("reason = %q, want %q", dead[0].Reason, "PR merged")
	}
}

func TestClassifyDeadMergedPRButStillAheadSurvives(t *testing.T) {
	// gh says the PR merged, but the branch kept moving afterward — the name is
	// carrying new local-only work, not the landed commit, so it must survive.
	dir := setupOriginRepo(t)
	commitOn(t, dir, "reused-name")
	gitT(t, dir, "push", "-q", "-u", "origin", "reused-name")
	writeFile(t, filepath.Join(dir, "more.txt"), "more\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "more work after the PR merged")
	gitT(t, dir, "switch", "-q", "main")
	t.Chdir(dir)
	ctx := context.Background()

	branches, err := LocalBranches(ctx, "main", nil)
	if err != nil {
		t.Fatalf("LocalBranches: %v", err)
	}
	var cand []BranchInfo
	for _, b := range branches {
		if b.Name == "reused-name" {
			cand = append(cand, b)
		}
	}

	dead, survivors := ClassifyDead(ctx, cand, "main", map[string]bool{"reused-name": true})
	if len(dead) != 0 {
		t.Fatalf("dead = %+v, want none — the branch is ahead of the PR that merged", dead)
	}
	if len(survivors) != 1 {
		t.Fatalf("survivors = %+v, want the ahead branch", survivors)
	}
}

// TestClassifyDeadRemoteGoneWithExtraLocalWorkSurvives is the hard backstop
// actually earning its keep: the same shape as
// TestClassifyDeadRemoteGoneIsDead (merged into main, pushed, upstream then
// deleted) — except one more commit lands on the branch afterward that never
// reaches origin at all. "remote gone" alone would say dead, since the
// branch's upstream really is gone; the backstop must still hold because that
// last commit lives nowhere else.
func TestClassifyDeadRemoteGoneWithExtraLocalWorkSurvives(t *testing.T) {
	dir := setupOriginRepo(t)
	commitOn(t, dir, "deleted-upstream-plus")
	gitT(t, dir, "push", "-q", "-u", "origin", "deleted-upstream-plus")
	gitT(t, dir, "switch", "-q", "main")
	gitT(t, dir, "merge", "-q", "--ff-only", "deleted-upstream-plus")
	gitT(t, dir, "push", "-q", "origin", "main")
	gitT(t, dir, "push", "-q", "origin", "--delete", "deleted-upstream-plus")
	gitT(t, dir, "fetch", "-q", "--prune", "origin")
	gitT(t, dir, "switch", "-q", "deleted-upstream-plus")
	writeFile(t, filepath.Join(dir, "more.txt"), "more\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "local work after the remote was deleted")
	gitT(t, dir, "switch", "-q", "main")
	t.Chdir(dir)
	ctx := context.Background()

	branches, err := LocalBranches(ctx, "main", nil)
	if err != nil {
		t.Fatalf("LocalBranches: %v", err)
	}
	var cand []BranchInfo
	for _, b := range branches {
		if b.Name == "deleted-upstream-plus" {
			cand = append(cand, b)
		}
	}
	if len(cand) != 1 {
		t.Fatalf("expected exactly one candidate, got %d", len(cand))
	}

	dead, survivors := ClassifyDead(ctx, cand, "main", nil)
	if len(dead) != 0 {
		t.Fatalf("dead = %+v, want none — a commit past the deleted upstream lives nowhere else", dead)
	}
	if len(survivors) != 1 {
		t.Fatalf("survivors = %+v, want the one branch", survivors)
	}
}

func TestClassifyDeadRemoteGoneIsDead(t *testing.T) {
	// "remote gone" fires only once the has-local-commits backstop clears: the
	// branch's commit has to be reachable via SOME origin ref, or the branch
	// would (rightly) survive as unlanded work with nowhere else it lives. So
	// the branch is fast-forward merged into main and pushed before its own
	// upstream is deleted — the same shape as GitHub's "delete branch" button
	// after a merge.
	dir := setupOriginRepo(t)
	commitOn(t, dir, "deleted-upstream")
	gitT(t, dir, "push", "-q", "-u", "origin", "deleted-upstream")
	gitT(t, dir, "switch", "-q", "main")
	gitT(t, dir, "merge", "-q", "--ff-only", "deleted-upstream")
	gitT(t, dir, "push", "-q", "origin", "main")
	gitT(t, dir, "push", "-q", "origin", "--delete", "deleted-upstream")
	gitT(t, dir, "fetch", "-q", "--prune", "origin")
	t.Chdir(dir)
	ctx := context.Background()

	branches, err := LocalBranches(ctx, "main", nil)
	if err != nil {
		t.Fatalf("LocalBranches: %v", err)
	}
	var cand []BranchInfo
	for _, b := range branches {
		if b.Name == "deleted-upstream" {
			cand = append(cand, b)
		}
	}

	dead, _ := ClassifyDead(ctx, cand, "main", nil)
	if len(dead) != 1 || dead[0].Reason != "remote gone" {
		t.Fatalf("dead = %+v, want one branch reasoned \"remote gone\"", dead)
	}
}

func TestHasCommitsNotOnOriginFalseWhenFullyPushed(t *testing.T) {
	dir := setupOriginRepo(t)
	commitOn(t, dir, "pushed")
	gitT(t, dir, "push", "-q", "-u", "origin", "pushed")
	t.Chdir(dir)

	if HasCommitsNotOnOrigin(context.Background(), "pushed") {
		t.Fatal("HasCommitsNotOnOrigin = true for a fully pushed branch, want false")
	}
}

func TestHasCommitsNotOnOriginTrueWhenNeverPushed(t *testing.T) {
	dir := setupOriginRepo(t)
	commitOn(t, dir, "local-only")
	t.Chdir(dir)

	if !HasCommitsNotOnOrigin(context.Background(), "local-only") {
		t.Fatal("HasCommitsNotOnOrigin = false for a never-pushed branch, want true")
	}
}
