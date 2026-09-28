package git

import (
	"context"
	"path/filepath"
	"testing"
)

// TestRepoRootSubmoduleWorktree covers a worktree cut from a submodule. Its
// common git dir is <super>/.git/modules/<name>, so taking the parent — right
// for every ordinary repo — lands on <super>/.git/modules, a directory holding
// no working tree. Every populate pattern then globbed against nothing and
// brought no local skills, no .env and no CLAUDE.local.md into the worktree.
func TestRepoRootSubmoduleWorktree(t *testing.T) {
	base := t.TempDir()

	subSrc := filepath.Join(base, "subsrc")
	mkdirT(t, subSrc)
	gitT(t, subSrc, "init", "-q")
	writeFile(t, filepath.Join(subSrc, "a.txt"), "a\n")
	gitT(t, subSrc, "add", "a.txt")
	gitT(t, subSrc, "commit", "-qm", "init")

	super := filepath.Join(base, "super")
	mkdirT(t, super)
	gitT(t, super, "init", "-q")
	writeFile(t, filepath.Join(super, "b.txt"), "b\n")
	gitT(t, super, "add", "b.txt")
	gitT(t, super, "commit", "-qm", "init")
	// git refuses a file-transport submodule by default since 2.38.
	gitT(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
	gitT(t, super, "commit", "-qm", "add sub")

	sub := filepath.Join(super, "sub")
	wt := filepath.Join(base, "subwt")
	gitT(t, sub, "worktree", "add", "-q", "--detach", wt, "HEAD")

	got, err := RepoRoot(context.Background(), wt)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	if want := realPath(t, sub); got != want {
		t.Errorf("RepoRoot = %q, want the submodule's own checkout %q", got, want)
	}
}

// TestRepoRootOrdinaryWorktree pins the case the core.worktree lookup must not
// disturb: a plain repo's linked worktree still resolves to the primary.
func TestRepoRootOrdinaryWorktree(t *testing.T) {
	base := t.TempDir()

	primary := filepath.Join(base, "primary")
	mkdirT(t, primary)
	gitT(t, primary, "init", "-q")
	writeFile(t, filepath.Join(primary, "a.txt"), "a\n")
	gitT(t, primary, "add", "a.txt")
	gitT(t, primary, "commit", "-qm", "init")

	wt := filepath.Join(base, "wt")
	gitT(t, primary, "worktree", "add", "-q", "--detach", wt, "HEAD")

	got, err := RepoRoot(context.Background(), wt)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	if want := realPath(t, primary); got != want {
		t.Errorf("RepoRoot = %q, want %q", got, want)
	}
}
