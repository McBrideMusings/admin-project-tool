package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// fakeHome builds a temp directory to use as $HOME for a test — t.TempDir()
// lands under /private/var on macOS, outside any real $HOME, so exercising
// the $HOME-scoped path of checkAbsoluteRunPaths requires pointing $HOME at
// the fixture directly (mirrors admin-relative-path-guard.sh's own test
// suite, which does the same for the same reason).
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// gitInit runs `git init` in dir, failing the test on error.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cmd := exec.Command("git", "init", "-q", ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

// shellActionManifest builds a *manifest.Manifest with one kind="shell"
// action named "build" whose run is the given string.
func shellActionManifest(run string) *manifest.Manifest {
	return &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"build": {"kind": "shell", "run": run},
		},
	}
}

// TestCheckAbsoluteRunPathsFailsOnPathIntoCheckout is the ticket's core
// acceptance criterion: a `run` line naming an absolute path under $HOME
// that resolves inside a git checkout fails, naming the action and the
// checkout root.
func TestCheckAbsoluteRunPathsFailsOnPathIntoCheckout(t *testing.T) {
	home := fakeHome(t)
	repo := filepath.Join(home, "repo")
	gitInit(t, repo)
	script := filepath.Join(repo, "scripts", "build.sh")
	m := shellActionManifest(script)

	err := checkAbsoluteRunPaths(context.Background(), m)
	if err == nil {
		t.Fatal("checkAbsoluteRunPaths = nil, want an error naming the checkout")
	}
	if !strings.Contains(err.Error(), `action "build"`) {
		t.Fatalf("error %q does not name the action", err.Error())
	}
	if !strings.Contains(err.Error(), repo) {
		t.Fatalf("error %q does not name the checkout root %s", err.Error(), repo)
	}
}

// TestCheckAbsoluteRunPathsPassesOnRelativePath proves a repo-relative run
// line — the fix the error message recommends — never fails.
func TestCheckAbsoluteRunPathsPassesOnRelativePath(t *testing.T) {
	fakeHome(t)
	m := shellActionManifest("./scripts/build.sh")

	if err := checkAbsoluteRunPaths(context.Background(), m); err != nil {
		t.Fatalf("checkAbsoluteRunPaths = %v, want nil (relative path)", err)
	}
}

// TestCheckAbsoluteRunPathsPassesOnSystemBinaryOutsideHome proves an
// absolute path to a system binary outside $HOME — /usr/bin/env, the ADR's
// own example of a legitimate absolute path — keeps passing regardless of
// whether it happens to sit inside a checkout somewhere on the machine.
func TestCheckAbsoluteRunPathsPassesOnSystemBinaryOutsideHome(t *testing.T) {
	fakeHome(t)
	m := shellActionManifest("/usr/bin/env node scripts/build.mjs")

	if err := checkAbsoluteRunPaths(context.Background(), m); err != nil {
		t.Fatalf("checkAbsoluteRunPaths = %v, want nil (system binary outside $HOME)", err)
	}
}

// TestCheckAbsoluteRunPathsPassesOnNonCheckoutPathUnderHome proves an
// absolute path under $HOME that does not resolve inside any git checkout
// passes — the check is about paths that name a repo, not about $HOME
// paths in general.
func TestCheckAbsoluteRunPathsPassesOnNonCheckoutPathUnderHome(t *testing.T) {
	home := fakeHome(t)
	scratch := filepath.Join(home, "scratch")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	m := shellActionManifest(filepath.Join(scratch, "build.sh"))

	if err := checkAbsoluteRunPaths(context.Background(), m); err != nil {
		t.Fatalf("checkAbsoluteRunPaths = %v, want nil (no checkout at that path)", err)
	}
}

// TestCheckAbsoluteRunPathsCatchesMultiTargetSubtable proves a
// kind="multi-target" inline subtable's own `run` is examined too, not only
// a flat kind="shell" action — collectRunRefs' ADR-0010 scope.
func TestCheckAbsoluteRunPathsCatchesMultiTargetSubtable(t *testing.T) {
	home := fakeHome(t)
	repo := filepath.Join(home, "repo")
	gitInit(t, repo)
	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"dev": {
				"kind": "multi-target",
				"cf": map[string]any{
					"run": filepath.Join(repo, "scripts", "dev.sh"),
				},
			},
		},
	}

	err := checkAbsoluteRunPaths(context.Background(), m)
	if err == nil {
		t.Fatal("checkAbsoluteRunPaths = nil, want an error naming the checkout")
	}
	if !strings.Contains(err.Error(), `action "dev.cf"`) {
		t.Fatalf("error %q does not name the multi-target subtable", err.Error())
	}
}

// TestCheckAbsoluteRunPathsCatchesFlagValueForm proves a `--flag=/abs/path`
// form (the path is not the leading token) is still caught, not only a bare
// leading path.
func TestCheckAbsoluteRunPathsCatchesFlagValueForm(t *testing.T) {
	home := fakeHome(t)
	repo := filepath.Join(home, "repo")
	gitInit(t, repo)
	m := shellActionManifest("rsync --exclude-from=" + filepath.Join(repo, "exclude.txt") + " src/ dest/")

	if err := checkAbsoluteRunPaths(context.Background(), m); err == nil {
		t.Fatal("checkAbsoluteRunPaths = nil, want an error naming the checkout")
	}
}

// TestCheckAbsoluteRunPathsStopsCandidateAtColon proves a PATH-list-shaped
// value (`PATH=/home/p/bin:$PATH`) reports the real absolute path segment,
// not the whole colon-joined string including the "$PATH" tail — a
// candidate string that never appeared as a literal path in the run line
// and would otherwise get walked ancestor-by-ancestor in
// nearestExistingAncestor.
func TestCheckAbsoluteRunPathsStopsCandidateAtColon(t *testing.T) {
	home := fakeHome(t)
	bin := filepath.Join(home, "repo", "bin")
	gitInit(t, filepath.Join(home, "repo"))
	m := shellActionManifest("PATH=" + bin + ":$PATH bun run dev")

	err := checkAbsoluteRunPaths(context.Background(), m)
	if err == nil {
		t.Fatal("checkAbsoluteRunPaths = nil, want an error naming the checkout")
	}
	if strings.Contains(err.Error(), ":$PATH") {
		t.Fatalf("error %q names the bogus colon-joined candidate instead of the real path", err.Error())
	}
	if !strings.Contains(err.Error(), bin) {
		t.Fatalf("error %q does not name the real absolute path %s", err.Error(), bin)
	}
}
