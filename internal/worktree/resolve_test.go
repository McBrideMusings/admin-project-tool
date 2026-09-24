package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// gitT runs a git command in dir, failing the test on error — same shape as
// internal/git's own helpers_test.go, duplicated here because that one is
// unexported to its package.
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

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// realPath resolves symlinks so a path this test built compares equal to the
// one git reports — on macOS /tmp is a symlink to /private/tmp, and git
// prints the resolved form.
func realPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// twoWorktrees builds a temp repo with two linked worktrees matching the
// tracked item's own acceptance-criteria example: "a" on branch
// feat/x-1-foo, "b" on branch fix/x-2-bar. Returns the primary checkout's
// directory and both worktrees' real (symlink-resolved) absolute paths.
func twoWorktrees(t *testing.T) (primary, aPath, bPath string) {
	t.Helper()
	primary = realPath(t, t.TempDir())
	gitT(t, primary, "init", "-q", "-b", "main")
	writeFileT(t, filepath.Join(primary, "f.txt"), "base\n")
	gitT(t, primary, "add", "-A")
	gitT(t, primary, "commit", "-qm", "base")

	wts := filepath.Join(primary, "..", "wts")
	if err := os.MkdirAll(wts, 0o755); err != nil {
		t.Fatal(err)
	}
	aPath = filepath.Join(wts, "a")
	bPath = filepath.Join(wts, "b")
	gitT(t, primary, "worktree", "add", "-q", "-b", "feat/x-1-foo", aPath)
	gitT(t, primary, "worktree", "add", "-q", "-b", "fix/x-2-bar", bPath)
	return primary, realPath(t, aPath), realPath(t, bPath)
}

func TestResolveTier1ExistingDirectory(t *testing.T) {
	primary, aPath, _ := twoWorktrees(t)
	got, err := Resolve(context.Background(), primary, aPath)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Path != aPath {
		t.Errorf("Path = %q, want %q", got.Path, aPath)
	}
	if got.Branch != "feat/x-1-foo" {
		t.Errorf("Branch = %q, want %q", got.Branch, "feat/x-1-foo")
	}
}

func TestResolveTier1DirectoryOutsideRepo(t *testing.T) {
	// Tier 1 resolves a real directory even when it doesn't belong to this
	// repo's worktree list at all — the spec's "no need to match a git
	// worktree entry".
	primary, _, _ := twoWorktrees(t)
	other := realPath(t, t.TempDir())
	got, err := Resolve(context.Background(), primary, other)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Path != other {
		t.Errorf("Path = %q, want %q", got.Path, other)
	}
	if got.Branch != "" {
		t.Errorf("Branch = %q, want empty for an unrelated directory", got.Branch)
	}
}

func TestResolveTier2Basename(t *testing.T) {
	primary, aPath, _ := twoWorktrees(t)
	got, err := Resolve(context.Background(), primary, "a")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Path != aPath {
		t.Errorf("Path = %q, want %q", got.Path, aPath)
	}
}

func TestResolveTier3Branch(t *testing.T) {
	primary, _, bPath := twoWorktrees(t)
	got, err := Resolve(context.Background(), primary, "fix/x-2-bar")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Path != bPath {
		t.Errorf("Path = %q, want %q", got.Path, bPath)
	}
}

func TestResolveTier4Substring(t *testing.T) {
	primary, aPath, _ := twoWorktrees(t)
	got, err := Resolve(context.Background(), primary, "X-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Path != aPath {
		t.Errorf("Path = %q, want %q (case-insensitive substring of the branch)", got.Path, aPath)
	}
}

func TestResolveAmbiguousExitsNonZero(t *testing.T) {
	primary, aPath, bPath := twoWorktrees(t)
	_, err := Resolve(context.Background(), primary, "x")
	var ambiguous *AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("Resolve(%q) = %v, want *AmbiguousError", "x", err)
	}
	if len(ambiguous.Candidates) != 2 {
		t.Fatalf("Candidates = %+v, want exactly 2", ambiguous.Candidates)
	}
	got := map[string]bool{}
	for _, c := range ambiguous.Candidates {
		got[c.Path] = true
	}
	if !got[aPath] || !got[bPath] {
		t.Errorf("Candidates = %+v, want both %q and %q listed", ambiguous.Candidates, aPath, bPath)
	}
	// Every candidate carries a branch, since a caller printing "every
	// candidate" wants path + branch, not just a path.
	for _, c := range ambiguous.Candidates {
		if c.Branch == "" {
			t.Errorf("candidate %q has no branch", c.Path)
		}
	}
}

func TestResolveNoMatchExitsNonZero(t *testing.T) {
	primary, _, _ := twoWorktrees(t)
	_, err := Resolve(context.Background(), primary, "nope")
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("Resolve(%q) = %v, want *NotFoundError", "nope", err)
	}
}

// TestResolveStopsAtFirstMatchingTier pins the "stop at the first tier that
// produces any match" rule: a target that would be ambiguous at tier 4 (a
// substring match) must never even be considered there once tier 2 (an exact
// basename) already resolved it on its own.
func TestResolveStopsAtFirstMatchingTier(t *testing.T) {
	primary, aPath, _ := twoWorktrees(t)
	got, err := Resolve(context.Background(), primary, "a")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Path != aPath {
		t.Errorf("Path = %q, want %q", got.Path, aPath)
	}
}

// TestResolveWithFakeList exercises the same four-tier logic against a
// synthetic worktree list rather than shelling out to git a second time —
// covering the branch-vs-basename precedence within a single tier without
// needing a real repo for every case.
func TestResolveWithFakeList(t *testing.T) {
	fake := []git.Worktree{
		{Path: "/repo", Branch: "main"},
		{Path: "/wts/one", Branch: "topic-one"},
		{Path: "/wts/two", Branch: "topic-two"},
	}
	list := func(context.Context, string) ([]git.Worktree, error) { return fake, nil }

	t.Run("branch exact when no basename matches", func(t *testing.T) {
		got, err := resolveWith(context.Background(), "", "topic-one", list)
		if err != nil {
			t.Fatalf("resolveWith: %v", err)
		}
		if got.Path != "/wts/one" {
			t.Errorf("Path = %q, want /wts/one", got.Path)
		}
	})

	t.Run("no match at any tier", func(t *testing.T) {
		_, err := resolveWith(context.Background(), "", "nothing-like-this", list)
		var notFound *NotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("resolveWith = %v, want *NotFoundError", err)
		}
	})
}
