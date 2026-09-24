package populate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
)

// TestCopyOverwritesAndReportsDiff pins that re-running populate over a
// copy-mode entry that differs brings the primary's version in, and hands back
// the diff of what it replaced so the worktree's edit is not lost unseen.
func TestCopyOverwritesAndReportsDiff(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("PORT=1111\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries := []config.Entry{{Pattern: ".env", Mode: config.Copy}}

	if res := Run(src, dst, entries); len(res) != 1 || res[0].Err != nil {
		t.Fatalf("first run: %+v", res)
	}
	if err := os.WriteFile(filepath.Join(dst, ".env"), []byte("PORT=9999\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := Run(src, dst, entries)
	if len(res) != 1 || res[0].Err != nil || res[0].Unchanged {
		t.Fatalf("second run over a divergent copy = %+v, want it replaced", res)
	}
	if !strings.Contains(res[0].Diff, "-PORT=9999") || !strings.Contains(res[0].Diff, "+PORT=1111") {
		t.Errorf("diff = %q, want the worktree's PORT=9999 replaced by the primary's PORT=1111", res[0].Diff)
	}
	got, err := os.ReadFile(filepath.Join(dst, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "PORT=1111\n" {
		t.Errorf(".env = %q after re-run, want the primary's %q", got, "PORT=1111\n")
	}

	res = Run(src, dst, entries)
	if len(res) != 1 || !res[0].Unchanged || res[0].Diff != "" {
		t.Errorf("third run over an identical copy = %+v, want Unchanged with no diff", res)
	}
}

// TestCopyReplacesLeftoverSymlink pins that switching a pattern from symlink to
// copy mode actually converts it. The symlink left behind by the old mode is not
// the worktree's own file, so the skip that protects a real copy must not protect
// it — otherwise the worktree would keep writing through to the primary repo
// forever, which is the opposite of what copy mode was asked for.
func TestCopyReplacesLeftoverSymlink(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("PORT=1111\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := Run(src, dst, []config.Entry{{Pattern: ".env", Mode: config.Symlink}}); res[0].Err != nil {
		t.Fatal(res[0].Err)
	}

	res := Run(src, dst, []config.Entry{{Pattern: ".env", Mode: config.Copy}})
	if len(res) != 1 || res[0].Err != nil || res[0].Unchanged {
		t.Fatalf("copy over a symlink = %+v, want it replaced", res)
	}
	info, err := os.Lstat(filepath.Join(dst, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error(".env is still a symlink; copy mode left the worktree writing through to the primary repo")
	}
}

// TestSymlinkIsRelaid pins that a symlink entry is restored on a re-run. This is
// what `repo populate` exists for: a worktree created before a pattern was added
// has no link at all, and running populate again must make one.
func TestSymlinkIsRelaid(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	real := filepath.Join(src, "CLAUDE.local.md")
	if err := os.WriteFile(real, []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries := []config.Entry{{Pattern: "CLAUDE.local.md", Mode: config.Symlink}}

	if res := Run(src, dst, entries); res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	if err := os.Remove(filepath.Join(dst, "CLAUDE.local.md")); err != nil {
		t.Fatal(err)
	}

	if res := Run(src, dst, entries); res[0].Err != nil {
		t.Fatalf("re-run = %+v, want the link remade", res)
	}
	got, err := os.Readlink(filepath.Join(dst, "CLAUDE.local.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got != real {
		t.Errorf("link points at %q, want %q", got, real)
	}
}

// gitT runs a git command in dir and fails the test if it errors.
func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestTrackedPathIsNeverPopulated pins the guard that keeps a broad pattern from
// clobbering a tracked path. `.claude/skills/*` legitimately matches both a
// committed skill and a local one; the committed one belongs to git, and apply
// removes the destination before linking, so populating it would replace a real
// directory with a symlink into the primary repo. A worktree whose branch
// predates that path would then carry it as a NEW untracked entry, and the next
// `git add -A` commits an absolute home-directory path. etv-station shipped six
// of those on 2026-08-20.
func TestTrackedPathIsNeverPopulated(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	gitT(t, src, "init", "-q")

	skills := filepath.Join(src, ".claude", "skills")
	for _, name := range []string{"committed", "local"} {
		if err := os.MkdirAll(filepath.Join(skills, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skills, name, "SKILL.md"), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, src, "add", ".claude/skills/committed")
	gitT(t, src, "commit", "-qm", "add the committed skill")

	res := Run(src, dst, []config.Entry{{Pattern: ".claude/skills/*", Mode: config.Symlink}})
	if len(res) != 2 {
		t.Fatalf("got %d results, want one per match: %+v", len(res), res)
	}

	byRel := map[string]Result{}
	for _, r := range res {
		byRel[filepath.Base(r.Rel)] = r
	}
	if got := byRel["committed"]; !got.Tracked {
		t.Errorf("committed skill = %+v, want Tracked", got)
	}
	if _, err := os.Lstat(filepath.Join(dst, ".claude", "skills", "committed")); !os.IsNotExist(err) {
		t.Errorf("committed skill was written into the worktree; want it left to git")
	}

	if got := byRel["local"]; got.Tracked || got.Err != nil {
		t.Errorf("local skill = %+v, want it populated", got)
	}
	link := filepath.Join(dst, ".claude", "skills", "local")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("local skill not symlinked: %v", err)
	}
	if want := filepath.Join(skills, "local"); target != want {
		t.Errorf("local link -> %q, want %q", target, want)
	}
}
