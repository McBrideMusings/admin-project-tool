package viewstate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitT runs a git command in dir, failing the test on error. Mirrors
// internal/git's own helper — this package can't import it (test-only,
// unexported, different package).
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

func writeTOML(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// useEmptyGlobal points GlobalPath at a path with nothing written to it, so
// level 3 always falls through to defaults — the shape almost every test
// below wants unless it's specifically exercising level 3.
func useEmptyGlobal(t *testing.T) {
	t.Helper()
	restore := UseGlobalPath(filepath.Join(t.TempDir(), "unused-view.toml"))
	t.Cleanup(restore)
}

func TestLoadLevel1BeatsLevel3PerKey(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, filepath.Join(dir, ".admin", Filename), `
[work]
collapsed = true
`)
	global := filepath.Join(t.TempDir(), "view.toml")
	writeTOML(t, global, `
[work]
collapsed = false

[tracker]
sort = "priority"

[branches]
mine_only = true
`)
	restore := UseGlobalPath(global)
	defer restore()

	got := Load(context.Background(), dir)

	if !got.WorkCollapsed {
		t.Errorf("WorkCollapsed = false, want level 1's true to beat level 3's false")
	}
	// work.collapsed is absent from... no, it IS present at level 1 above.
	// tracker.sort and branches.mine_only are absent at level 1 (a non-git
	// dir, so level 2 never applies) and present only at level 3 — a key
	// missing from level 1 must still fall through to level 3 while
	// work.collapsed, present at level 1, does not.
	if got.TrackSort != "priority" {
		t.Errorf("TrackSort = %q, want level 3's %q since level 1 doesn't set tracker.sort", got.TrackSort, "priority")
	}
	if !got.MinesOnly {
		t.Error("MinesOnly = false, want level 3's true since level 1 doesn't set branches.mine_only")
	}
	if got.BeadTree != "" {
		t.Errorf("BeadTree = %q, want \"\" — no level sets beads.tree", got.BeadTree)
	}
}

func TestLoadNonGitProjectDirSkipsLevel2(t *testing.T) {
	dir := t.TempDir() // never git-initialized
	useEmptyGlobal(t)

	got := Load(context.Background(), dir)

	if got != (State{}) {
		t.Errorf("Load in a non-git dir = %+v, want the zero State (built-in defaults)", got)
	}
}

func TestLoadLinkedWorktreeReadsMainCheckoutFile(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "primary")
	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, primary, "init", "-q")
	gitT(t, primary, "commit", "--allow-empty", "-qm", "init")
	writeTOML(t, filepath.Join(primary, ".admin", Filename), `
[work]
collapsed = true

[tracker]
sort = "priority"
`)

	wt := filepath.Join(base, "wt")
	gitT(t, primary, "worktree", "add", "-q", "--detach", wt, "HEAD")
	// The linked worktree's own project dir has no view.toml at all — the
	// main checkout's file is all there is to find.
	useEmptyGlobal(t)

	got := Load(context.Background(), wt)

	if !got.WorkCollapsed {
		t.Error("WorkCollapsed = false, want true from the main checkout's view.toml")
	}
	if got.TrackSort != "priority" {
		t.Errorf("TrackSort = %q, want %q from the main checkout's view.toml", got.TrackSort, "priority")
	}
}

func TestLoadLevel1BeatsLevel2WhenBothSetTheSameKey(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "primary")
	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, primary, "init", "-q")
	gitT(t, primary, "commit", "--allow-empty", "-qm", "init")
	writeTOML(t, filepath.Join(primary, ".admin", Filename), `
[work]
collapsed = false

[tracker]
sort = "priority"
`)

	wt := filepath.Join(base, "wt")
	gitT(t, primary, "worktree", "add", "-q", "--detach", wt, "HEAD")
	// The worktree sets its OWN value for both keys — level 1 must win over
	// level 2 (the main checkout's file above) for each, not just supply
	// whatever level 1 leaves unset.
	writeTOML(t, filepath.Join(wt, ".admin", Filename), `
[work]
collapsed = true

[tracker]
sort = "oldest-first"
`)
	useEmptyGlobal(t)

	got := Load(context.Background(), wt)

	if !got.WorkCollapsed {
		t.Error("WorkCollapsed = false, want the worktree's own true, not the main checkout's false")
	}
	if got.TrackSort != "oldest-first" {
		t.Errorf("TrackSort = %q, want the worktree's own %q, not the main checkout's %q", got.TrackSort, "oldest-first", "priority")
	}
}

func TestLoadBadValueFallsThrough(t *testing.T) {
	dir := t.TempDir() // non-git: only levels 1 and 3 apply
	writeTOML(t, filepath.Join(dir, ".admin", Filename), `
[work]
collapsed = "yes"

[beads]
tree = "grid"
`)
	global := filepath.Join(t.TempDir(), "view.toml")
	writeTOML(t, global, `
[work]
collapsed = true

[beads]
tree = "family"
`)
	restore := UseGlobalPath(global)
	defer restore()

	got := Load(context.Background(), dir)

	if !got.WorkCollapsed {
		t.Error("WorkCollapsed = false, want level 3's true — level 1's \"yes\" is the wrong TOML type for a bool")
	}
	if got.BeadTree != "family" {
		t.Errorf("BeadTree = %q, want level 3's %q — level 1's \"grid\" isn't a recognized value", got.BeadTree, "family")
	}
}

func TestSaveWritesOnlyProjectLevelFile(t *testing.T) {
	dir := t.TempDir() // non-git, so no main-checkout level to confuse with
	writeTOML(t, filepath.Join(dir, ".admin", Filename), `
[tracker]
sort = "priority"
`)
	global := filepath.Join(t.TempDir(), "view.toml")
	writeTOML(t, global, `
[branches]
mine_only = true
`)
	restore := UseGlobalPath(global)
	defer restore()

	if err := SaveWorkCollapsed(dir, true); err != nil {
		t.Fatalf("SaveWorkCollapsed: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".admin", Filename))
	if err != nil {
		t.Fatalf("reading the saved project-level file: %v", err)
	}
	content := string(raw)
	if !strings.Contains(content, "priority") {
		t.Errorf("save dropped the pre-existing tracker.sort key it should have preserved: %s", content)
	}
	if strings.Contains(content, "mine_only") {
		t.Errorf("save copied branches.mine_only down from the global level into the project file: %s", content)
	}

	// The global file itself must be untouched — Save only ever writes level 1.
	globalRaw, err := os.ReadFile(global)
	if err != nil {
		t.Fatalf("reading the global file: %v", err)
	}
	if !strings.Contains(string(globalRaw), "mine_only") {
		t.Error("the global view.toml was modified by a project-level save")
	}

	got := Load(context.Background(), dir)
	if !got.WorkCollapsed {
		t.Error("Load after save: WorkCollapsed = false, want true")
	}
	if got.TrackSort != "priority" {
		t.Errorf("Load after save: TrackSort = %q, want the preserved %q", got.TrackSort, "priority")
	}
	if !got.MinesOnly {
		t.Error("Load after save: MinesOnly = false, want true (still inherited from the global level)")
	}
}
