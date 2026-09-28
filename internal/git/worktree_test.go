package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitFails runs a git command expected to fail, returning its combined output.
func gitFails(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("git %v unexpectedly succeeded:\n%s", args, out)
	}
	return string(out)
}

// TestWorktreeHoldsBranchDuringRebase pins the attachment a paused rebase creates.
// `git worktree list --porcelain` prints `detached` and no `branch` line for such a
// worktree, but git still refuses to delete the branch — so reading porcelain alone
// makes the branch look like a bare deletable ref and a cleanup delete fails.
func TestWorktreeHoldsBranchDuringRebase(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "base")

	// A branch that conflicts with main, checked out in a worktree.
	gitT(t, dir, "branch", "topic")
	writeFile(t, filepath.Join(dir, "f.txt"), "main-side\n")
	gitT(t, dir, "commit", "-qam", "main edit")

	wtPath := filepath.Join(dir, "wt")
	gitT(t, dir, "worktree", "add", "-q", wtPath, "topic")
	writeFile(t, filepath.Join(wtPath, "f.txt"), "topic-side\n")
	gitT(t, wtPath, "commit", "-qam", "topic edit")

	// Rebase onto main, which stops on the conflict and detaches HEAD.
	gitFails(t, wtPath, "rebase", "main")

	// Precondition: git itself now reports the worktree as detached with no branch.
	raw, err := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := "branch refs/heads/topic"; strings.Contains(string(raw), want) {
		t.Fatalf("precondition failed: porcelain still reports %q, so this test proves nothing\n%s", want, raw)
	}

	// Precondition: git refuses to delete the branch, force or not.
	if out := gitFails(t, dir, "branch", "-D", "topic"); !strings.Contains(out, "worktree") {
		t.Fatalf("expected git to refuse the delete on worktree grounds, got:\n%s", out)
	}

	wts, err := Worktrees(context.Background(), dir)
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	var found *Worktree
	for i := range wts {
		if wts[i].Path == resolve(t, wtPath) {
			found = &wts[i]
		}
	}
	if found == nil {
		t.Fatalf("worktree %s missing from %+v", wtPath, wts)
	}
	if found.Branch != "topic" {
		t.Errorf("rebasing worktree Branch = %q, want %q — its branch would be offered as a bare deletable ref", found.Branch, "topic")
	}
	if found.Op != "rebase" {
		t.Errorf("rebasing worktree Op = %q, want %q", found.Op, "rebase")
	}
	if !found.Detached {
		t.Errorf("rebasing worktree should still report Detached")
	}
}

// TestWorktreeDetachedAtCommitHoldsNoBranch checks a plain detached checkout — no
// operation in flight — is left alone: it genuinely holds no branch.
func TestWorktreeDetachedAtCommitHoldsNoBranch(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "base")

	wtPath := filepath.Join(dir, "wt")
	gitT(t, dir, "worktree", "add", "-q", "--detach", wtPath, "HEAD")

	wts, err := Worktrees(context.Background(), dir)
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	for _, w := range wts {
		if w.Path == resolve(t, wtPath) && (w.Branch != "" || w.Op != "") {
			t.Errorf("plain detached worktree got Branch=%q Op=%q, want both empty", w.Branch, w.Op)
		}
	}
}

// TestWorktreeReportsConflictedMergeOp pins the case a detached-only read misses.
// A merge that stops on a conflict leaves HEAD on its branch, so `git worktree
// list --porcelain` shows an ordinary attached worktree and nothing says the tree
// is stuck half-merged. Without Op set here, the Cleanup box labels it plain
// "uncommitted" and the delete walks into a stash git will refuse.
func TestWorktreeReportsConflictedMergeOp(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "base")
	gitT(t, dir, "branch", "topic")

	wtPath := filepath.Join(dir, "wt")
	gitT(t, dir, "worktree", "add", "-q", wtPath, "topic")
	writeFile(t, filepath.Join(wtPath, "f.txt"), "topic-side\n")
	gitT(t, wtPath, "commit", "-qam", "topic edit")

	writeFile(t, filepath.Join(dir, "f.txt"), "main-side\n")
	gitT(t, dir, "commit", "-qam", "main edit")

	// Merge main into topic inside the worktree; it stops on the conflict.
	gitFails(t, wtPath, "merge", "main")

	// Precondition: git still reports the worktree as attached to its branch, so
	// only the paused-operation marker distinguishes it from an ordinary dirty tree.
	raw, err := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "branch refs/heads/topic") {
		t.Fatalf("precondition failed: conflicted merge detached HEAD, so this test proves nothing\n%s", raw)
	}

	wts, err := Worktrees(context.Background(), dir)
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	var found *Worktree
	for i := range wts {
		if wts[i].Path == resolve(t, wtPath) {
			found = &wts[i]
		}
	}
	if found == nil {
		t.Fatalf("worktree %s missing from %+v", wtPath, wts)
	}
	if found.Op != "merge" {
		t.Errorf("conflicted-merge worktree Op = %q, want %q", found.Op, "merge")
	}
	if found.Branch != "topic" {
		t.Errorf("conflicted-merge worktree Branch = %q, want %q", found.Branch, "topic")
	}

	st, err := WorkingStatus(context.Background(), wtPath)
	if err != nil {
		t.Fatalf("WorkingStatus: %v", err)
	}
	if st.Conflicted != 1 {
		t.Errorf("Conflicted = %d, want 1 — the count is what decides the delete refuses", st.Conflicted)
	}
}

// TestWorkingStatusCountsOnlyUnmergedAsConflicted keeps ordinary edits out of the
// conflict count: an inflated count would refuse deletes that stash perfectly well.
func TestWorkingStatusCountsOnlyUnmergedAsConflicted(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "base")

	writeFile(t, filepath.Join(dir, "f.txt"), "edited\n")   // unstaged
	writeFile(t, filepath.Join(dir, "new.txt"), "staged\n") // staged
	gitT(t, dir, "add", "new.txt")
	writeFile(t, filepath.Join(dir, "extra.txt"), "untracked\n")

	st, err := WorkingStatus(context.Background(), dir)
	if err != nil {
		t.Fatalf("WorkingStatus: %v", err)
	}
	if st.Conflicted != 0 {
		t.Errorf("Conflicted = %d on a tree with no merge in flight, want 0", st.Conflicted)
	}
	if st.Clean() {
		t.Error("tree with staged, unstaged, and untracked files reported Clean")
	}
}

func TestUnmergedRecognizesEveryConflictCode(t *testing.T) {
	for _, code := range []string{"DD", "AU", "UD", "UA", "DU", "AA", "UU"} {
		if !unmerged(code[0], code[1]) {
			t.Errorf("unmerged(%q) = false, want true", code)
		}
	}
	for _, code := range []string{" M", "M ", "MM", "A ", " D", "D ", "R ", "??", "AM", "AD"} {
		if unmerged(code[0], code[1]) {
			t.Errorf("unmerged(%q) = true, want false", code)
		}
	}
}

// TestWorktreeGitDirFailsClosed pins the three ways the layout read comes up
// empty. Every one of them makes `Worktrees` skip paused-operation detection for
// that worktree, which is the safe answer — the unsafe one would be guessing a
// git dir. There is deliberately no `git rev-parse` fallback behind this: when a
// worktree directory survives but its `.git` does not, rev-parse walks up and
// answers with the *parent* repo's git dir, which would report the main repo's
// paused rebase as this worktree's and hand back the main repo's branch.
func TestWorktreeGitDirFailsClosed(t *testing.T) {
	dir := t.TempDir()

	if got := worktreeGitDir(filepath.Join(dir, "no-such-worktree")); got != "" {
		t.Errorf("missing worktree = %q, want \"\"", got)
	}

	empty := filepath.Join(dir, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(empty, ".git"), "")
	if got := worktreeGitDir(empty); got != "" {
		t.Errorf("empty .git file = %q, want \"\"", got)
	}

	junk := filepath.Join(dir, "junk")
	if err := os.MkdirAll(junk, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(junk, ".git"), "not a gitdir line\n")
	if got := worktreeGitDir(junk); got != "" {
		t.Errorf("malformed .git file = %q, want \"\"", got)
	}

	// A relative gitdir resolves against the worktree, not the process cwd.
	rel := filepath.Join(dir, "rel")
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(rel, ".git"), "gitdir: ../elsewhere/.git/worktrees/rel\n")
	want := filepath.Join(dir, "elsewhere", ".git", "worktrees", "rel")
	if got := worktreeGitDir(rel); got != want {
		t.Errorf("relative gitdir = %q, want %q", got, want)
	}

	// The main worktree's .git is the git dir itself.
	main := filepath.Join(dir, "main")
	gitDir := filepath.Join(main, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := worktreeGitDir(main); got != gitDir {
		t.Errorf("main worktree = %q, want %q", got, gitDir)
	}
}

// TestPausedOpPrefersRebaseOverItsOwnCherryPick pins the marker precedence. A pick
// that conflicts mid-rebase writes CHERRY_PICK_HEAD alongside the rebase state, and
// the rebase is the thing to go deal with — reporting "cherry-picking" there would
// point at the step instead of the operation running it.
func TestPausedOpPrefersRebaseOverItsOwnCherryPick(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "CHERRY_PICK_HEAD"), "deadbeef\n")
	if got := pausedOp(dir); got != OpCherryPick {
		t.Fatalf("pausedOp with only CHERRY_PICK_HEAD = %q, want %q", got, OpCherryPick)
	}
	if err := os.MkdirAll(filepath.Join(dir, "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := pausedOp(dir); got != OpRebase {
		t.Errorf("pausedOp with both markers = %q, want %q", got, OpRebase)
	}
	if got := pausedOp(t.TempDir()); got != "" {
		t.Errorf("pausedOp with no markers = %q, want \"\"", got)
	}
}

func TestReadRefRejectsRawCommit(t *testing.T) {
	dir := t.TempDir()
	sha := filepath.Join(dir, "sha")
	writeFile(t, sha, "0123456789abcdef0123456789abcdef01234567\n")
	if got := readRef(sha); got != "" {
		t.Errorf("readRef on a raw commit = %q, want \"\" (a bisect from detached HEAD names no branch)", got)
	}
	name := filepath.Join(dir, "name")
	writeFile(t, name, "refs/heads/feature/x\n")
	if got := readRef(name); got != "feature/x" {
		t.Errorf("readRef = %q, want %q", got, "feature/x")
	}
	if got := readRef(filepath.Join(dir, "missing")); got != "" {
		t.Errorf("readRef on a missing file = %q, want \"\"", got)
	}
}

// gitConfigGet reads a git config value, returning "" if it is unset.
func gitConfigGet(t *testing.T, dir, key string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestFetchPRHeadSameRepoDefersToDWIM pins the fix for #36: when origin already
// has the PR's branch, FetchPRHead must not fetch it into a local branch first —
// doing so pre-empts AddExisting's DWIM, which is what wires the upstream. Left
// alone, the DWIM creates the local branch from origin/<branch> and sets its
// upstream itself.
func TestFetchPRHeadSameRepoDefersToDWIM(t *testing.T) {
	origin := t.TempDir()
	gitT(t, origin, "init", "-q", "--bare", "-b", "main")

	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "init")
	gitT(t, dir, "remote", "add", "origin", origin)
	gitT(t, dir, "push", "-q", "-u", "origin", "main")

	// Simulate a same-repo PR: push a branch to origin, then drop the local copy
	// so only the remote-tracking ref remains, as if opened from a fresh clone.
	gitT(t, dir, "checkout", "-qb", "pr-branch")
	writeFile(t, filepath.Join(dir, "f.txt"), "pr\n")
	gitT(t, dir, "commit", "-qam", "pr work")
	gitT(t, dir, "push", "-q", "origin", "pr-branch")
	gitT(t, dir, "checkout", "-q", "main")
	gitT(t, dir, "branch", "-D", "pr-branch")

	ctx := context.Background()
	if BranchExistsLocal(ctx, dir, "pr-branch") {
		t.Fatal("precondition failed: pr-branch already local")
	}
	if !BranchExistsRemote(ctx, dir, "pr-branch") {
		t.Fatal("precondition failed: origin/pr-branch missing")
	}

	if err := FetchPRHead(ctx, dir, 99, "pr-branch"); err != nil {
		t.Fatalf("FetchPRHead: %v", err)
	}
	if BranchExistsLocal(ctx, dir, "pr-branch") {
		t.Fatal("FetchPRHead created the local branch itself, pre-empting AddExisting's DWIM")
	}

	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddExisting(ctx, dir, wt, "pr-branch"); err != nil {
		t.Fatalf("AddExisting: %v", err)
	}

	if got := gitConfigGet(t, dir, "branch.pr-branch.remote"); got != "origin" {
		t.Errorf("branch.pr-branch.remote = %q, want %q", got, "origin")
	}
	if got := gitConfigGet(t, dir, "branch.pr-branch.merge"); got != "refs/heads/pr-branch" {
		t.Errorf("branch.pr-branch.merge = %q, want %q", got, "refs/heads/pr-branch")
	}
}

// TestFetchPRHeadForkSetsUpstreamToPullRef pins the fork-PR branch of #36: when
// origin has no remote-tracking branch of the same name, FetchPRHead must fetch
// the PR's synthetic pull ref into a local branch and point that branch's
// upstream at the pull ref itself — the only ref a fork PR has — so `git pull`
// keeps following the PR instead of freezing at whatever commit this fetch caught.
func TestFetchPRHeadForkSetsUpstreamToPullRef(t *testing.T) {
	origin := t.TempDir()
	gitT(t, origin, "init", "-q", "--bare", "-b", "main")

	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "init")
	gitT(t, dir, "remote", "add", "origin", origin)
	gitT(t, dir, "push", "-q", "-u", "origin", "main")

	// Simulate a fork PR: the head commit lands only on GitHub's synthetic
	// refs/pull/<N>/head, never on a branch under origin's refs/heads/*.
	fork := t.TempDir()
	gitT(t, fork, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(fork, "f.txt"), "fork\n")
	gitT(t, fork, "add", "-A")
	gitT(t, fork, "commit", "-qm", "fork work")
	gitT(t, fork, "remote", "add", "origin", origin)
	gitT(t, fork, "push", "-q", "origin", "HEAD:refs/pull/42/head")

	ctx := context.Background()
	if BranchExistsRemote(ctx, dir, "pr-fork") {
		t.Fatal("precondition failed: origin/pr-fork should not exist")
	}

	if err := FetchPRHead(ctx, dir, 42, "pr-fork"); err != nil {
		t.Fatalf("FetchPRHead: %v", err)
	}
	if !BranchExistsLocal(ctx, dir, "pr-fork") {
		t.Fatal("FetchPRHead did not create the local branch")
	}

	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddExisting(ctx, dir, wt, "pr-fork"); err != nil {
		t.Fatalf("AddExisting: %v", err)
	}

	if got := gitConfigGet(t, dir, "branch.pr-fork.remote"); got != "origin" {
		t.Errorf("branch.pr-fork.remote = %q, want %q", got, "origin")
	}
	if got := gitConfigGet(t, dir, "branch.pr-fork.merge"); got != "refs/pull/42/head" {
		t.Errorf("branch.pr-fork.merge = %q, want %q", got, "refs/pull/42/head")
	}
}

// resolve returns path with symlinks resolved, since macOS temp dirs live under a
// symlinked /var and git reports the resolved form.
func resolve(t *testing.T, path string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return p
}

// TestAddNewBranchLeavesNoUpstream pins that a branch forked from origin/<default>
// does not end up tracking that ref. New worktrees fork from a remote-tracking
// branch, and git's default there is to set it as the new branch's upstream — so
// without --no-track, `feat-x` would track `origin/main`, a bare `git push` on it
// would target main, and every ahead/behind reading would compare it to main
// rather than to its own remote branch. The correct state for a never-pushed
// branch is no upstream at all.
func TestAddNewBranchLeavesNoUpstream(t *testing.T) {
	origin := t.TempDir()
	gitT(t, origin, "init", "-q", "--bare", "-b", "main")

	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "init")
	gitT(t, dir, "remote", "add", "origin", origin)
	gitT(t, dir, "push", "-q", "-u", "origin", "main")

	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddNewBranch(context.Background(), dir, wt, "feat-x", "origin/main"); err != nil {
		t.Fatalf("AddNewBranch: %v", err)
	}

	out, err := exec.Command("git", "-C", dir, "config", "--get", "branch.feat-x.merge").Output()
	if err == nil {
		t.Errorf("feat-x has upstream %q; a new branch forked from origin/main must have none",
			strings.TrimSpace(string(out)))
	}
}
