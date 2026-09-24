package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/worktree"
)

// initGitRepo creates a fresh git repo at dir with one commit, so it (and
// any archetype directory placed inside it) is a real, independently
// resolvable repo — not a worktree of anything else.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	gitE2E(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitE2E(t, dir, "add", "-A")
	gitE2E(t, dir, "commit", "-qm", "base")
}

// TestWorktreeFlagArchetypeMergeUsesTargetRepo proves archetype merge is
// keyed off the -w target's own repo, not the invoking shell's cwd. Without
// this, `admin -w <target>` run from repo A against a target living in repo
// B would resolve archetypes/manifest.go's repo-local search-path slot
// (internal/archetype.SearchPathFor -> <repoDir>/.admin/archetypes) against
// A instead of B — invisible whenever the two repos happen to be the same,
// which every other test in this file arranges for simplicity, so this one
// deliberately does not.
func TestWorktreeFlagArchetypeMergeUsesTargetRepo(t *testing.T) {
	t.Cleanup(func() { worktreeTarget, worktreePrimaryRoot = nil, "" })
	installed := t.TempDir()
	defer archetype.UseInstalledDir(installed)()

	repoA := realPathE2E(t, t.TempDir())
	initGitRepo(t, repoA)

	repoB := realPathE2E(t, t.TempDir())
	initGitRepo(t, repoB)

	// Only repoB carries a local archetype override.
	archDir := filepath.Join(repoB, ".admin", "archetypes", "probe")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatal(err)
	}
	archToml := "[commands.build]\ndesc = \"from-repoB\"\nsteps = [\"x\"]\n"
	if err := os.WriteFile(filepath.Join(archDir, archetype.Filename), []byte(archToml), 0o644); err != nil {
		t.Fatal(err)
	}

	adminToml := `project_name = "b"
archetypes   = ["probe"]

[commands.build]
steps = ["x"]

[actions.x]
kind = "shell"
run  = "true"
`
	if err := os.WriteFile(filepath.Join(repoB, "admin.toml"), []byte(adminToml), 0o644); err != nil {
		t.Fatal(err)
	}

	// The invoking shell is in repoA; -w targets a directory in repoB.
	t.Chdir(repoA)
	worktreeTarget = &worktree.Target{Path: repoB}
	worktreePrimaryRoot = repoA

	m, ok, err := resolveProjectManifestFromDisk(context.Background(), repoB)
	if err != nil || !ok {
		t.Fatalf("resolveProjectManifestFromDisk(repoB) = ok=%v err=%v, want ok=true err=nil", ok, err)
	}
	if got := m.Overrides["build"].Desc; got != "from-repoB" {
		t.Fatalf("Overrides[build].Desc = %q, want %q — archetype merge must be keyed off the -w target's repo (repoB), not the invoking shell's cwd (repoA)", got, "from-repoB")
	}
}

// TestWorktreeFlagFallbackBorrowsPrimaryManifestButRunsInTarget covers the
// -w fallback path: a target worktree with no admin.toml of its own (the
// common case, since admin.toml is never committed) borrows the primary
// checkout's manifest content but still executes with the target as Root —
// resolveProjectManifestFromDisk's execRoot override. Previously only
// exercised manually against a real sibling repo (see the comment this test
// replaces in TestWorktreeFlagRunsInsideTargetNotInvokingCwd), never
// asserted in `go test`.
func TestWorktreeFlagFallbackBorrowsPrimaryManifestButRunsInTarget(t *testing.T) {
	t.Cleanup(func() { worktreeTarget, worktreePrimaryRoot = nil, "" })
	installed := t.TempDir()
	defer archetype.UseInstalledDir(installed)()

	primary := realPathE2E(t, t.TempDir())
	initGitRepo(t, primary)

	adminToml := `project_name = "primary"

[commands.build]
steps = ["x"]

[actions.x]
kind = "shell"
run  = "true"
`
	if err := os.WriteFile(filepath.Join(primary, "admin.toml"), []byte(adminToml), 0o644); err != nil {
		t.Fatal(err)
	}

	targetPath := filepath.Join(primary, "..", "fallback-target")
	gitE2E(t, primary, "worktree", "add", "-q", "-b", "feat/fallback-target", targetPath)
	targetPath = realPathE2E(t, targetPath)
	// Deliberately no admin.toml written into targetPath — the fallback
	// case: a fresh worktree with none of its own.

	t.Chdir(primary)
	worktreeTarget = &worktree.Target{Path: targetPath, Branch: "feat/fallback-target"}
	worktreePrimaryRoot = primary

	m, ok, err := resolveProjectManifestFromDisk(context.Background(), targetPath)
	if err != nil || !ok {
		t.Fatalf("resolveProjectManifestFromDisk(targetPath) = ok=%v err=%v, want ok=true err=nil", ok, err)
	}
	if m.Root != targetPath {
		t.Fatalf("Root = %q, want the target worktree %q — the borrowed manifest must still execute inside the target, not the primary checkout it was borrowed from", m.Root, targetPath)
	}
	if _, ok := m.Custom["build"]; !ok {
		if _, ok := m.Overrides["build"]; !ok {
			t.Fatalf("resolved manifest has no build command — the primary checkout's admin.toml was not borrowed")
		}
	}
}

// gitE2E runs a git command in dir, failing the test on error — same shape
// as internal/git's own helpers_test.go and internal/worktree's own
// resolve_test.go, duplicated here because those are unexported to their
// packages.
func gitE2E(t *testing.T, dir string, args ...string) {
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

func realPathE2E(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// TestWorktreeFlagRunsInsideTargetNotInvokingCwd is the tracked item's own
// end-to-end acceptance criterion: `admin -w <name> <cmd>` — run from the
// primary checkout — actually executes the command's step with the TARGET
// worktree as its working directory, never the shell's own cwd it was typed
// from. It builds the real `admin` binary (the same one install.sh ships,
// not a compiled-artifact standalone) via `go build`, so this proves the
// -w flag as a person would actually invoke it, not just the resolver
// function in isolation (see internal/worktree's own unit tests for the
// four-tier resolution and its ambiguity/no-match cases).
func TestWorktreeFlagRunsInsideTargetNotInvokingCwd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the real admin binary via `go build`; skipped with -short")
	}

	repoRoot := repoRootForTest(t)
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "admin-e2e")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	primary := realPathE2E(t, t.TempDir())
	gitE2E(t, primary, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(primary, "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitE2E(t, primary, "add", "-A")
	gitE2E(t, primary, "commit", "-qm", "base")

	// The target worktree carries its OWN admin.toml — admin.toml is never
	// committed, so a real fresh worktree usually lacks one (the fallback-
	// to-primary path is what the live acceptance check against term-redux
	// drives instead); this fixture takes the direct path to keep the
	// end-to-end proof about -w's cwd retargeting, not the fallback.
	targetPath := filepath.Join(primary, "..", "e2e-target")
	gitE2E(t, primary, "worktree", "add", "-q", "-b", "feat/e2e-target", targetPath)
	targetPath = realPathE2E(t, targetPath)

	toml := `project_name = "e2e-target"

[commands.custom.pwd]
desc  = "print pwd"
steps = ["pwd"]

[actions.pwd]
kind = "shell"
run  = "pwd"
`
	if err := os.WriteFile(filepath.Join(targetPath, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	// Invoked from the PRIMARY checkout — the "invoking shell's cwd" the
	// acceptance criterion contrasts the target against — with a target
	// name that only matches at tier 4 (a substring of the branch), so this
	// also proves the flag drives real resolution, not just an exact path.
	run := exec.Command(binPath, "-w", "e2e-target", "pwd")
	run.Dir = primary
	run.Env = append([]string{"PATH=/usr/bin:/bin"}, "HOME="+os.Getenv("HOME"))
	var stdout, stderr bytes.Buffer
	run.Stdout = &stdout
	run.Stderr = &stderr
	if err := run.Run(); err != nil {
		t.Fatalf("admin -w e2e-target pwd failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	// The shell step's own output is wrapped in the framed action's status
	// lines (a colored "running:"/"complete" pair, a timestamp column) —
	// substring-checked rather than trimmed-and-compared for exactly that
	// reason.
	got := stdout.String()
	if !strings.Contains(got, targetPath) {
		t.Fatalf("printed output does not contain the target worktree %q:\n%s", targetPath, got)
	}
	if strings.Contains(got, primary) {
		t.Fatalf("printed output contains the invoking shell's cwd %q — -w had no effect:\n%s", primary, got)
	}
}

// TestStripWorktreeFlag covers every form -w/--worktree can be typed in,
// proving it's removed from argv before dispatchArgs or cobra's own parser
// ever see it (see stripWorktreeFlag's doc comment for why that matters).
func TestStripWorktreeFlag(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantOut   []string
		wantValue string
		wantFound bool
	}{
		{"absent", []string{"dev"}, []string{"dev"}, "", false},
		{"short with space", []string{"-w", "redux-c38.5", "sessions"}, []string{"sessions"}, "redux-c38.5", true},
		{"long with space", []string{"--worktree", "redux-c38.5", "sessions"}, []string{"sessions"}, "redux-c38.5", true},
		{"short equals", []string{"-w=redux-c38.5", "sessions"}, []string{"sessions"}, "redux-c38.5", true},
		{"long equals", []string{"--worktree=redux-c38.5", "sessions"}, []string{"sessions"}, "redux-c38.5", true},
		{"bare, no args after", []string{"-w", "redux-c38.5"}, []string{}, "redux-c38.5", true},
		{"missing value left in place", []string{"-w"}, []string{"-w"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, value, found := stripWorktreeFlag(tc.args)
			if !reflect.DeepEqual(out, tc.wantOut) {
				t.Errorf("out = %v, want %v", out, tc.wantOut)
			}
			if value != tc.wantValue || found != tc.wantFound {
				t.Errorf("(value, found) = (%q, %v), want (%q, %v)", value, found, tc.wantValue, tc.wantFound)
			}
		})
	}
}

// TestResolveWorktreeFlagAmbiguousAndNoMatch drives the tracked item's own
// acceptance-criteria example verbatim: a temp repo with worktree a on
// branch feat/x-1-foo and worktree b on branch fix/x-2-bar. "-w x-1" resolves
// to a's path; "-w x" is ambiguous and names both; "-w nope" matches nothing.
// All three are errors a caller prints to stderr and exits non-zero on,
// except the first — see main.go's own `fmt.Fprintln(os.Stderr,
// err); os.Exit(1)` and internal/worktree's own AmbiguousError/NotFoundError
// tests for the resolver in isolation.
func TestResolveWorktreeFlagAmbiguousAndNoMatch(t *testing.T) {
	t.Cleanup(func() { worktreeTarget, worktreePrimaryRoot = nil, "" })

	primary := realPathE2E(t, t.TempDir())
	gitE2E(t, primary, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(primary, "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitE2E(t, primary, "add", "-A")
	gitE2E(t, primary, "commit", "-qm", "base")

	aPath := filepath.Join(primary, "..", "a")
	bPath := filepath.Join(primary, "..", "b")
	gitE2E(t, primary, "worktree", "add", "-q", "-b", "feat/x-1-foo", aPath)
	gitE2E(t, primary, "worktree", "add", "-q", "-b", "fix/x-2-bar", bPath)
	aPath = realPathE2E(t, aPath)
	bPath = realPathE2E(t, bPath)

	t.Chdir(primary)

	t.Run("x-1 resolves to a's path", func(t *testing.T) {
		t.Cleanup(func() { worktreeTarget, worktreePrimaryRoot = nil, "" })
		if err := resolveWorktreeFlag(context.Background(), "x-1"); err != nil {
			t.Fatalf("resolveWorktreeFlag = %v, want nil", err)
		}
		if worktreeTarget == nil || worktreeTarget.Path != aPath {
			t.Fatalf("worktreeTarget = %+v, want Path %q", worktreeTarget, aPath)
		}
	})

	t.Run("x is ambiguous and names both", func(t *testing.T) {
		t.Cleanup(func() { worktreeTarget, worktreePrimaryRoot = nil, "" })
		err := resolveWorktreeFlag(context.Background(), "x")
		var ambiguous *worktree.AmbiguousError
		if !errors.As(err, &ambiguous) {
			t.Fatalf("resolveWorktreeFlag(%q) = %v, want *worktree.AmbiguousError", "x", err)
		}
		msg := err.Error()
		if !strings.Contains(msg, aPath) || !strings.Contains(msg, bPath) {
			t.Fatalf("error %q does not name both candidates (%q, %q)", msg, aPath, bPath)
		}
		if worktreeTarget != nil {
			t.Fatalf("worktreeTarget = %+v, want nil on a resolution error", worktreeTarget)
		}
	})

	t.Run("nope matches nothing", func(t *testing.T) {
		t.Cleanup(func() { worktreeTarget, worktreePrimaryRoot = nil, "" })
		err := resolveWorktreeFlag(context.Background(), "nope")
		var notFound *worktree.NotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("resolveWorktreeFlag(%q) = %v, want *worktree.NotFoundError", "nope", err)
		}
	})
}
