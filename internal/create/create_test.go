package create

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// gitT runs a git command in dir, failing the test on error, and returns stdout.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupRepo builds an origin repo and a clone of it, points WORKTREE_ROOT at a
// scratch dir, cd's into the clone, and returns (cloneDir, originDir). The clone
// has an `origin` remote and an origin/main tracking ref, so every attachment
// path — fork from origin/main, fetch a remote-only branch, check out a local
// branch — runs against real git state.
//
// It also points archetype.InstalledArchetypesDir at a fresh empty temp
// directory for the duration of the test (restored via t.Cleanup), so
// Worktree's config.Load call never merges the machine's real installed
// archetypes (~/.admin/archetypes/, symlinked to a checkout's archetypes/ on
// some machines). A test that means to exercise archetype merging installs
// its own fixture through archetype.UseInstalledDir instead, after
// setupRepo returns.
func setupRepo(t *testing.T) (clone, origin string) {
	t.Helper()
	t.Cleanup(archetype.UseInstalledDir(t.TempDir()))
	base := t.TempDir()
	origin = filepath.Join(base, "origin")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, origin, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(origin, "README.md"), "hi\n")
	gitT(t, origin, "add", "-A")
	gitT(t, origin, "commit", "-qm", "init")

	clone = filepath.Join(base, "clone")
	gitT(t, base, "clone", "-q", origin, clone)

	t.Setenv("WORKTREE_ROOT", filepath.Join(base, "wt"))
	return clone, origin
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func assertWorktreeOnBranch(t *testing.T, path, branch string) {
	t.Helper()
	if path == "" {
		t.Fatal("empty worktree path")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("worktree dir missing: %v", err)
	}
	if got := strings.TrimSpace(gitT(t, path, "rev-parse", "--abbrev-ref", "HEAD")); got != branch {
		t.Errorf("worktree on branch %q, want %q", got, branch)
	}
}

func countWorktreesFor(t *testing.T, ctx context.Context, repoRoot, branch string) int {
	t.Helper()
	wts, err := git.Worktrees(ctx, repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, w := range wts {
		if w.Branch == branch {
			n++
		}
	}
	return n
}

// A brand-new branch is forked from origin/main, and a second call for the same
// branch is idempotent: it returns the same path and never adds a second
// worktree (issue #3 — the idempotent-worktree path).
func TestWorktreeNewBranchIsIdempotent(t *testing.T) {
	clone, _ := setupRepo(t)
	ctx := context.Background()

	res, err := Worktree(ctx, clone, "feat-new")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	assertWorktreeOnBranch(t, res.Path, "feat-new")

	res2, err := Worktree(ctx, clone, "feat-new")
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if resolve(res2.Path) != resolve(res.Path) {
		t.Errorf("idempotent path = %q, want %q", res2.Path, res.Path)
	}
	if n := countWorktreesFor(t, ctx, clone, "feat-new"); n != 1 {
		t.Errorf("worktrees for feat-new = %d, want 1", n)
	}
}

// A branch that exists locally but has no worktree is checked out, not forked
// with `git worktree add -b` (which git rejects — the original exit-255 bug).
func TestWorktreeExistingLocalBranch(t *testing.T) {
	clone, _ := setupRepo(t)
	ctx := context.Background()
	gitT(t, clone, "branch", "local-feat")

	res, err := Worktree(ctx, clone, "local-feat")
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	assertWorktreeOnBranch(t, res.Path, "local-feat")
}

// A branch already checked out in a worktree at some other path is opened where
// it lives — not re-added at the computed path (the original exit-128 bug).
func TestWorktreeBranchAlreadyInAnotherWorktree(t *testing.T) {
	clone, _ := setupRepo(t)
	ctx := context.Background()
	other := filepath.Join(t.TempDir(), "elsewhere")
	gitT(t, clone, "worktree", "add", "-b", "dup-feat", other, "main")

	res, err := Worktree(ctx, clone, "dup-feat")
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if resolve(res.Path) != resolve(other) {
		t.Errorf("path = %q, want existing worktree %q", res.Path, other)
	}
	if n := countWorktreesFor(t, ctx, clone, "dup-feat"); n != 1 {
		t.Errorf("worktrees for dup-feat = %d, want 1", n)
	}
}

// Two clones sharing a directory name under different parents compute the same
// worktree path. When the first repo's worktree already occupies it, the second
// repo's attempt must fail naming the repo that owns the path — not claim the
// path isn't a worktree — and the path itself must be unchanged (no hash
// segment).
func TestWorktreePathTakenByAnotherRepo(t *testing.T) {
	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, origin, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(origin, "README.md"), "hi\n")
	gitT(t, origin, "add", "-A")
	gitT(t, origin, "commit", "-qm", "init")

	// Same basename ("foo"), different parents — the collision from issue #5.
	parentA, parentB := filepath.Join(base, "work"), filepath.Join(base, "personal")
	cloneA, cloneB := filepath.Join(parentA, "foo"), filepath.Join(parentB, "foo")
	for _, p := range []string{parentA, parentB} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, parentA, "clone", "-q", origin, cloneA)
	gitT(t, parentB, "clone", "-q", origin, cloneB)

	root := filepath.Join(base, "wt")
	t.Setenv("WORKTREE_ROOT", root)
	ctx := context.Background()

	res, err := Worktree(ctx, cloneA, "shared-name")
	if err != nil {
		t.Fatalf("repo A create: %v", err)
	}
	want := filepath.Join(root, "foo", "shared-name")
	if resolve(res.Path) != resolve(want) {
		t.Fatalf("repo A path = %q, want %q", res.Path, want)
	}

	if _, err := Worktree(ctx, cloneB, "shared-name"); err == nil {
		t.Fatal("repo B create succeeded, want collision error")
	} else {
		// Resolved, because git records the main worktree by its real path and on
		// macOS the temp dir is reached through a symlink.
		msg, owner := err.Error(), resolve(cloneA)
		if !strings.Contains(msg, owner) {
			t.Errorf("error %q does not name the owning repo %q", msg, owner)
		}
		if strings.Contains(msg, "is not a git worktree") {
			t.Errorf("error %q still reports the misleading not-a-worktree cause", msg)
		}
	}
}

// TestWorktreePopulatesFromPrimaryManifest is the ticket's central acceptance
// criterion: `admin populate` (via create.Worktree, which every worktree-
// creation path calls) seeds a fresh worktree from the PRIMARY checkout's
// admin.toml — the ONLY place populate patterns can live now that
// internal/config no longer reads any dotfile. The freshly created worktree
// itself has no manifest of its own; it must still receive both the
// admin.toml the pattern names and an unrelated untracked file the manifest
// declares.
func TestWorktreePopulatesFromPrimaryManifest(t *testing.T) {
	clone, _ := setupRepo(t)
	ctx := context.Background()

	writeFile(t, filepath.Join(clone, "admin.toml"), `project_name = "t"

[[populate]]
pattern = "admin.toml"
mode = "copy"

[[populate]]
pattern = "CLAUDE.local.md"
mode = "copy"
`)
	writeFile(t, filepath.Join(clone, "CLAUDE.local.md"), "local notes\n")

	res, err := Worktree(ctx, clone, "feat-populate")
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	assertWorktreeOnBranch(t, res.Path, "feat-populate")

	admin, err := os.ReadFile(filepath.Join(res.Path, "admin.toml"))
	if err != nil {
		t.Fatalf("worktree missing admin.toml — populate did not read the primary checkout's manifest: %v", err)
	}
	if !strings.Contains(string(admin), "project_name") {
		t.Errorf("worktree admin.toml content = %q, want the primary checkout's own manifest", admin)
	}
	local, err := os.ReadFile(filepath.Join(res.Path, "CLAUDE.local.md"))
	if err != nil {
		t.Fatalf("worktree missing CLAUDE.local.md: %v", err)
	}
	if string(local) != "local notes\n" {
		t.Errorf("CLAUDE.local.md content = %q, want the primary checkout's content", local)
	}
}

// TestWorktreePopulatesFromBaseArchetype is admin-mb5q round 2's acceptance
// 15: a manifest that declares NO [[populate]] entries of its own is still
// seeded from the implicit base archetype (internal/archetype.BaseName) —
// the replacement for the old global config tier's populate patterns.
// archetype.InstalledArchetypesDir is pointed at a scratch dir carrying its
// own base/ fixture so this never touches the real ~/.admin deployment on
// the machine running the test — config.Load resolves the installed slot
// through that seam rather than os.UserHomeDir/$HOME.
func TestWorktreePopulatesFromBaseArchetype(t *testing.T) {
	clone, _ := setupRepo(t)
	ctx := context.Background()

	installed := t.TempDir()
	t.Cleanup(archetype.UseInstalledDir(installed))
	baseDir := filepath.Join(installed, "base")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(baseDir, "archetype.toml"), `name = "base"

[[populate]]
pattern = "CLAUDE.local.md"
mode = "copy"
`)

	// The project's own manifest declares no [[populate]] of its own — every
	// pattern that reaches the worktree must have come from base.
	writeFile(t, filepath.Join(clone, "admin.toml"), `project_name = "t"`)
	writeFile(t, filepath.Join(clone, "CLAUDE.local.md"), "local notes\n")

	res, err := Worktree(ctx, clone, "feat-populate-base")
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	assertWorktreeOnBranch(t, res.Path, "feat-populate-base")

	local, err := os.ReadFile(filepath.Join(res.Path, "CLAUDE.local.md"))
	if err != nil {
		t.Fatalf("worktree missing CLAUDE.local.md — base's populate entry did not apply: %v", err)
	}
	if string(local) != "local notes\n" {
		t.Errorf("CLAUDE.local.md content = %q, want the primary checkout's content", local)
	}
}

// A branch that exists only on origin (tracking ref present, no local branch) is
// fetched and checked out — not forked into a divergent new branch.
func TestWorktreeRemoteOnlyBranch(t *testing.T) {
	clone, origin := setupRepo(t)
	ctx := context.Background()
	gitT(t, origin, "branch", "remote-feat", "main")
	gitT(t, clone, "fetch", "-q", "origin")

	if git.BranchExistsLocal(ctx, clone, "remote-feat") {
		t.Fatal("precondition: remote-feat should not be a local branch")
	}
	if !git.BranchExistsRemote(ctx, clone, "remote-feat") {
		t.Fatal("precondition: remote-feat should be a remote-tracking branch")
	}

	res, err := Worktree(ctx, clone, "remote-feat")
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	assertWorktreeOnBranch(t, res.Path, "remote-feat")
}
