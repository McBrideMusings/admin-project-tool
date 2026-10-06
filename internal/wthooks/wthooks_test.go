package wthooks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// repoWithWorktree builds a primary checkout declaring preRemove and one
// linked worktree on branch "feat", and returns both paths.
func repoWithWorktree(t *testing.T, preRemove string) (repo, wt string) {
	t.Helper()
	t.Cleanup(archetype.UseInstalledDir(t.TempDir()))
	base := t.TempDir()
	repo = filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	manifest := "project_name = \"t\"\n[worktrees.hooks]\npre_remove = '" + preRemove + "'\n"
	if err := os.WriteFile(filepath.Join(repo, "admin.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	wt = filepath.Join(base, "wt")
	gitT(t, repo, "worktree", "add", "-q", "-b", "feat", wt)
	return repo, wt
}

// pre_remove reads the PRIMARY checkout's manifest, runs inside the worktree
// with the ADR-0024 variables, and a non-zero exit vetoes with its output.
func TestPreRemoveVetoes(t *testing.T) {
	repo, wt := repoWithWorktree(t, `echo "busy $ADMIN_WT_BRANCH $(basename "$PWD")"; exit 4`)
	ctx := git.WithQuiet(context.Background())

	err := PreRemove(ctx, repo, wt, false)
	var he *Error
	if !errors.As(err, &he) {
		t.Fatalf("PreRemove = %v, want a *Error veto", err)
	}
	if he.Exit != 4 || !strings.Contains(err.Error(), "busy feat wt") {
		t.Errorf("veto = exit %d, %q; want exit 4 carrying the hook's output", he.Exit, err)
	}
}

// With force the hook still runs, and its failure no longer vetoes.
func TestPreRemoveForceStillRuns(t *testing.T) {
	repo, wt := repoWithWorktree(t, `touch "$ADMIN_REPO_ROOT/ran"; exit 1`)
	if err := PreRemove(git.WithQuiet(context.Background()), repo, wt, true); err != nil {
		t.Fatalf("forced PreRemove = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "ran")); err != nil {
		t.Error("forced removal skipped the pre_remove hook")
	}
}

// A worktree whose directory is already gone runs no hook.
func TestPreRemoveSkipsMissingDir(t *testing.T) {
	repo, wt := repoWithWorktree(t, "exit 1")
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if err := PreRemove(git.WithQuiet(context.Background()), repo, wt, false); err != nil {
		t.Fatalf("PreRemove on a missing dir = %v, want nil", err)
	}
}
