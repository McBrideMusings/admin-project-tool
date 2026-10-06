package cmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/shim"
)

// rmFixture is a repo pushed to a local bare origin, with one worktree on
// branch "feat" whose commit is pushed with an upstream — so ClassifyDead
// calls the branch dead "on origin". preRemove, when set, is the primary
// checkout's [worktrees.hooks] pre_remove.
func rmFixture(t *testing.T, preRemove string) (repo, wt string) {
	t.Helper()
	t.Setenv("HERDR_ENV", "")
	t.Setenv("ADMIN_TARGET", "")
	origin := t.TempDir()
	gitCT(t, origin, "init", "-q", "--bare", "-b", "main")
	repo = t.TempDir()
	gitCT(t, repo, "init", "-q", "-b", "main")
	writeFileCT(t, filepath.Join(repo, "f.txt"), "base\n")
	gitCT(t, repo, "add", "-A")
	gitCT(t, repo, "commit", "-qm", "init")
	gitCT(t, repo, "remote", "add", "origin", origin)
	gitCT(t, repo, "push", "-q", "-u", "origin", "main")
	manifest := "project_name = \"t\"\n"
	if preRemove != "" {
		manifest += "[worktrees.hooks]\npre_remove = '" + preRemove + "'\n"
	}
	writeFileCT(t, filepath.Join(repo, "admin.toml"), manifest)

	wt = filepath.Join(t.TempDir(), "feat")
	gitCT(t, repo, "worktree", "add", "-q", "-b", "feat", wt)
	writeFileCT(t, filepath.Join(wt, "g.txt"), "g\n")
	gitCT(t, wt, "add", "-A")
	gitCT(t, wt, "commit", "-qm", "work")
	gitCT(t, wt, "push", "-q", "-u", "origin", "feat")
	return repo, wt
}

func branchExists(t *testing.T, repo, branch string) bool {
	t.Helper()
	return exec.Command("git", "-C", repo, "rev-parse", "--verify", "-q", "refs/heads/"+branch).Run() == nil
}

func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestWorktreeRmRemovesWorktreeAndDeadBranch(t *testing.T) {
	repo, wt := rmFixture(t, "")
	if err := runWorktreeRm(context.Background(), repo, "feat", false, true); err != nil {
		t.Fatalf("runWorktreeRm: %v", err)
	}
	if dirExists(wt) || branchExists(t, repo, "feat") {
		t.Fatalf("worktree exists=%v, branch exists=%v; want both gone", dirExists(wt), branchExists(t, repo, "feat"))
	}
}

func TestWorktreeRmByPath(t *testing.T) {
	repo, wt := rmFixture(t, "")
	if err := runWorktreeRm(context.Background(), repo, wt, false, true); err != nil {
		t.Fatalf("runWorktreeRm: %v", err)
	}
	if dirExists(wt) {
		t.Fatal("worktree named by path still exists")
	}
}

func TestWorktreeRmRefusesDirtyUnlessForced(t *testing.T) {
	repo, wt := rmFixture(t, "")
	writeFileCT(t, filepath.Join(wt, "scratch.txt"), "unsaved\n")
	err := runWorktreeRm(context.Background(), repo, "feat", false, true)
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("err = %v, want an uncommitted-changes refusal", err)
	}
	if !dirExists(wt) {
		t.Fatal("refused removal deleted the worktree")
	}
	if err := runWorktreeRm(context.Background(), repo, "feat", true, true); err != nil {
		t.Fatalf("forced runWorktreeRm: %v", err)
	}
	if dirExists(wt) {
		t.Fatal("forced removal left the worktree")
	}
	out, _ := exec.Command("git", "-C", repo, "stash", "list").Output()
	if !bytes.Contains(out, []byte("auto-stash before worktree removal")) {
		t.Fatalf("forced removal did not stash the uncommitted file; stash list:\n%s", out)
	}
}

func TestWorktreeRmRefusesUnpushedCommits(t *testing.T) {
	repo, wt := rmFixture(t, "")
	writeFileCT(t, filepath.Join(wt, "h.txt"), "h\n")
	gitCT(t, wt, "add", "-A")
	gitCT(t, wt, "commit", "-qm", "local only")
	err := runWorktreeRm(context.Background(), repo, "feat", false, true)
	if err == nil || !strings.Contains(err.Error(), "live on no origin ref") {
		t.Fatalf("err = %v, want an unpushed-commits refusal", err)
	}
	if !dirExists(wt) {
		t.Fatal("refused removal deleted the worktree")
	}
}

func TestWorktreeRmRefusesPausedOperation(t *testing.T) {
	repo, wt := rmFixture(t, "")
	// A conflicting cherry-pick from main leaves one paused in the worktree.
	writeFileCT(t, filepath.Join(repo, "g.txt"), "main side\n")
	gitCT(t, repo, "add", "g.txt")
	gitCT(t, repo, "commit", "-qm", "conflict")
	cmd := exec.Command("git", "-C", wt, "cherry-pick", "main")
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if cmd.Run() == nil {
		t.Fatal("cherry-pick applied cleanly; the fixture needs a conflict")
	}
	err := runWorktreeRm(context.Background(), repo, "feat", false, true)
	if err == nil || !strings.Contains(err.Error(), "a paused cherry-pick") {
		t.Fatalf("err = %v, want a paused-operation refusal", err)
	}
	if !dirExists(wt) {
		t.Fatal("refused removal deleted the worktree")
	}
}

func TestWorktreeRmRefusesDetachedHead(t *testing.T) {
	repo, wt := rmFixture(t, "")
	gitCT(t, wt, "switch", "-q", "--detach")
	err := runWorktreeRm(context.Background(), repo, wt, false, true)
	if err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("err = %v, want a detached-HEAD refusal", err)
	}
	if !dirExists(wt) {
		t.Fatal("refused removal deleted the worktree")
	}
}

func TestWorktreeRmRejectsUnregisteredDirectory(t *testing.T) {
	repo, wt := rmFixture(t, "")
	sub := filepath.Join(wt, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	err := runWorktreeRm(context.Background(), repo, sub, false, true)
	if err == nil || !strings.Contains(err.Error(), "not a registered worktree") {
		t.Fatalf("err = %v, want the not-a-worktree error", err)
	}
}

func TestWorktreeRmKeepsSurvivorBranch(t *testing.T) {
	repo, wt := rmFixture(t, "")
	// Every commit lives on origin, but under another name with no counterpart
	// for feat2 and nothing merged: ClassifyDead's survivor.
	gitCT(t, wt, "switch", "-q", "-c", "feat2")
	writeFileCT(t, filepath.Join(wt, "h.txt"), "h\n")
	gitCT(t, wt, "add", "-A")
	gitCT(t, wt, "commit", "-qm", "more")
	gitCT(t, wt, "push", "-q", "origin", "feat2:elsewhere")
	if err := runWorktreeRm(context.Background(), repo, "feat2", false, true); err != nil {
		t.Fatalf("runWorktreeRm: %v", err)
	}
	if dirExists(wt) || !branchExists(t, repo, "feat2") {
		t.Fatalf("worktree exists=%v, branch exists=%v; want worktree gone, branch kept", dirExists(wt), branchExists(t, repo, "feat2"))
	}
}

func TestWorktreeRmPreRemoveVetoKeepsEverything(t *testing.T) {
	repo, wt := rmFixture(t, "echo not yet; exit 3")
	err := runWorktreeRm(context.Background(), repo, "feat", false, true)
	if err == nil || !strings.Contains(err.Error(), "pre_remove hook failed (exit 3)") {
		t.Fatalf("err = %v, want the pre_remove veto", err)
	}
	if !dirExists(wt) || !branchExists(t, repo, "feat") {
		t.Fatal("a vetoed removal touched the worktree or its branch")
	}
	if err := runWorktreeRm(context.Background(), repo, "feat", true, true); err != nil {
		t.Fatalf("forced runWorktreeRm over a veto: %v", err)
	}
	if dirExists(wt) {
		t.Fatal("--force did not override the pre_remove veto")
	}
}

func TestWorktreeRmNeverRemovesMainTree(t *testing.T) {
	repo, _ := rmFixture(t, "")
	err := runWorktreeRm(context.Background(), repo, repo, true, true)
	if err == nil || !strings.Contains(err.Error(), "main working tree") {
		t.Fatalf("err = %v, want the main-tree refusal", err)
	}
}

func TestWorktreeRmNoArgRemovesCwdAndCDsToPrimary(t *testing.T) {
	repo, wt := rmFixture(t, "")
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := runWorktreeRm(context.Background(), filepath.Join(wt), "", false, true)
	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	if err != nil {
		t.Fatalf("runWorktreeRm: %v", err)
	}
	if dirExists(wt) {
		t.Fatal("no-arg form left the cwd's worktree")
	}
	root, _ := filepath.EvalSymlinks(repo)
	if !strings.Contains(out.String(), shim.SentinelPrefix) || !strings.Contains(out.String(), filepath.Base(root)) {
		t.Fatalf("stdout = %q, want a cd sentinel to %s", out.String(), root)
	}
}

func TestWorktreeRmRefusesWorkerSelfRemoval(t *testing.T) {
	_, wt := rmFixture(t, "")
	t.Setenv("ADMIN_TARGET", "admin-x1")
	err := runWorktreeRm(context.Background(), wt, "", true, true)
	if err == nil || !strings.Contains(err.Error(), "reaper") {
		t.Fatalf("err = %v, want the worker self-removal refusal", err)
	}
	if !dirExists(wt) {
		t.Fatal("a worker removed its own worktree")
	}
}

func TestWorktreeRmRefusesWorkerNamingItsWorktreeFromElsewhere(t *testing.T) {
	repo, wt := rmFixture(t, "")
	t.Setenv("ADMIN_TARGET", "admin-x1")
	t.Setenv("ADMIN_WORKER_WORKTREE", wt)
	err := runWorktreeRm(context.Background(), repo, "feat", true, true)
	if err == nil || !strings.Contains(err.Error(), "reaper") {
		t.Fatalf("err = %v, want the worker self-removal refusal", err)
	}
	if !dirExists(wt) {
		t.Fatal("a worker removed its own worktree by naming its branch")
	}
}
