package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBranchActivitySeparatesEmptyFromMerged pins the distinction the commit graph
// cannot make: a branch created off the default branch and never committed to is
// listed by `git branch --merged origin/main` exactly like a branch whose work
// really did land. Only the reflog says which is which.
func TestBranchActivitySeparatesEmptyFromMerged(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "init")

	// Created and left alone — the case a mass cleanup swept out from under an
	// agent that was still working in it.
	gitT(t, dir, "switch", "-q", "-c", "fresh-empty")

	// Created, committed to, then merged back: genuinely leftovers.
	gitT(t, dir, "switch", "-q", "main")
	commitOn(t, dir, "did-work")
	gitT(t, dir, "switch", "-q", "main")
	gitT(t, dir, "merge", "-q", "--no-ff", "-m", "merge", "did-work")

	t.Chdir(dir)
	branches, err := LocalBranches(context.Background(), "main", nil)
	if err != nil {
		t.Fatalf("LocalBranches: %v", err)
	}

	// Both are equally "merged" to git, which is the whole problem.
	merged, err := MergedIntoDefault(context.Background(), "main")
	if err != nil {
		t.Fatalf("MergedIntoDefault: %v", err)
	}
	if !merged["fresh-empty"] || !merged["did-work"] {
		t.Fatalf("precondition: expected both branches merged into main, got %v", merged)
	}

	act := BranchActivity(context.Background(), branches)
	if !act["fresh-empty"].NeverCommitted {
		t.Error("a branch created and never committed to did not report NeverCommitted — it would read as merged work")
	}
	if act["did-work"].NeverCommitted {
		t.Error("a branch that held a commit reported NeverCommitted")
	}
	if age := time.Since(act["fresh-empty"].LastTouched); age > time.Minute || age < 0 {
		t.Errorf("fresh branch LastTouched is %v old, want ~now", age)
	}
}

// TestCheckingOutAnEmptyBranchLeavesItsReflogAlone documents why the reflog cannot
// be the only freshness probe: checking a branch out updates HEAD's reflog, not the
// branch's own, so working in a branch you never commit to leaves its ref
// timestamp frozen at creation. The worktree probes exist to cover that gap.
func TestCheckingOutAnEmptyBranchLeavesItsReflogAlone(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "init")
	gitT(t, dir, "switch", "-q", "-c", "empty")
	gitT(t, dir, "switch", "-q", "main")
	gitT(t, dir, "switch", "-q", "empty")

	t.Chdir(dir)
	branches, err := LocalBranches(context.Background(), "empty", nil)
	if err != nil {
		t.Fatalf("LocalBranches: %v", err)
	}
	if !BranchActivity(context.Background(), branches)["empty"].NeverCommitted {
		t.Error("checking a branch out counted as work on it; NeverCommitted must survive checkouts")
	}
}

// TestReusedBranchNameReadsEmpty pins the case a leftover reflog used to poison: a
// deleted branch's log entries outlive the ref, so the branch created next under
// that name appends its creation entry to a file that already has lines in it.
// Only the entries after the *last* creation line describe the branch that exists
// now, and there are none.
//
// The stale entries are written here rather than produced by `git branch -D`
// because git (2.50) deletes a branch's reflog along with its ref — a repo reaches
// this state through whatever deleted the ref without its log, which is any number
// of tools. The file shape is the thing under test either way.
func TestReusedBranchNameReadsEmpty(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "init")
	gitT(t, dir, "switch", "-q", "-c", "reused")

	// A previous life of this name: created, committed to twice, then deleted —
	// prepended to the log the live branch just created.
	log := filepath.Join(dir, ".git", "logs", "refs", "heads", "reused")
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read reflog: %v", err)
	}
	const zero = "0000000000000000000000000000000000000000"
	stale := zero + " " + zero + " prior <prior@example.com> 1700000000 -0500\tbranch: Created from HEAD\n" +
		zero + " " + zero + " prior <prior@example.com> 1700000100 -0500\tcommit: work that has since been deleted\n"
	writeFile(t, log, stale+string(raw))

	t.Chdir(dir)
	branches, err := LocalBranches(context.Background(), "reused", nil)
	if err != nil {
		t.Fatalf("LocalBranches: %v", err)
	}
	act := BranchActivity(context.Background(), branches)
	if !act["reused"].NeverCommitted {
		t.Error("a branch created under a name whose old reflog entries survived did not report NeverCommitted — the stale segment disqualified the name")
	}
	if age := time.Since(act["reused"].LastTouched); age > time.Minute || age < 0 {
		t.Errorf("reused branch LastTouched is %v old, want ~now — it must come from the live segment, not the stale one", age)
	}
}

// TestBranchStartedAtExistingWorkIsNotEmpty covers what the reflog cannot see: a
// branch created at a commit that is not on the default line has a reflog holding
// nothing but its creation entry, yet it carries commits of its own. The commit
// count against the base is the check that catches it.
func TestBranchStartedAtExistingWorkIsNotEmpty(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "init")

	// Work that never reaches main, left somewhere a new branch can point at.
	commitOn(t, dir, "work")
	gitT(t, dir, "switch", "-q", "main")
	gitT(t, dir, "switch", "-q", "-c", "forked-at-work", "work")

	t.Chdir(dir)
	branches, err := LocalBranches(context.Background(), "forked-at-work", nil)
	if err != nil {
		t.Fatalf("LocalBranches: %v", err)
	}
	if BranchActivity(context.Background(), branches)["forked-at-work"].NeverCommitted {
		t.Error("a branch created at an unmerged commit reported NeverCommitted — its reflog holds only a creation entry, so the commit count is what must catch it")
	}
}

// TestWorktreeTouchedSeesGitActivity checks the worktree probe moves when a git
// command runs in the tree — the signal that catches an agent working in a branch
// it has not committed to yet.
func TestWorktreeTouchedSeesGitActivity(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "init")

	wt := filepath.Join(t.TempDir(), "wt")
	gitT(t, dir, "worktree", "add", "-q", "-b", "wt-branch", wt)
	t.Chdir(dir)

	touched := WorktreeTouched(context.Background(), wt)
	if touched.IsZero() {
		t.Fatal("WorktreeTouched reported nothing for a live worktree")
	}
	if age := time.Since(touched); age > time.Minute || age < 0 {
		t.Errorf("worktree touched %v ago, want ~now", age)
	}
}
