// Package populate brings gitignored local files (.env, etc.) from the primary
// repo into a worktree, per-pattern as a symlink (default), a copy, or a
// copy-on-write clone.
package populate

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
)

// Result records what happened to one matched path.
type Result struct {
	Rel  string
	Mode config.Mode
	// Unchanged is set when a copy-mode entry found the destination already
	// byte-identical to the source and left it alone.
	Unchanged bool
	// Diff is the unified diff from the worktree's old copy to the primary's
	// file, set when a copy-mode entry overwrote a file that differed.
	Diff string
	// Tracked is set when the match was left alone because git tracks it. See
	// Run for why a tracked path is never populated.
	Tracked bool
	// Kept is set when a clone-mode entry found a real directory already at the
	// destination and left it alone: a cloned build cache is the worktree's own
	// once it builds into it.
	Kept bool
	// FullCopy holds why a clone-mode entry could not share blocks with the
	// primary and made a full copy instead, so a slow populate is explained.
	FullCopy string
	Err      error
}

// Run applies every populate entry from src (the primary repo root) into dst (a
// worktree root). Patterns are globbed relative to src. Returns one Result per
// matched path. Does nothing when entries is empty.
//
// A path git tracks is never populated, whatever the pattern says. Populate
// exists for gitignored local files; a tracked path is git's to fill in, and
// every worktree already has it from its own checkout. Writing there is not a
// no-op but a corruption: apply removes the destination before linking, so a
// broad pattern like `.claude/skills/*` would replace a real, tracked directory
// with a symlink into the primary repo. In a worktree whose branch predates that
// path the symlink is a brand new untracked entry, and the next `git add -A`
// commits an absolute path into one machine's home directory. That is not
// hypothetical — it is how etv-station came to carry six symlinks pointing into
// a different project (2026-08-20).
//
// Checked per match rather than per pattern: `.claude/skills/*` legitimately
// mixes tracked skills with local ones, and the local ones are the whole reason
// to write that pattern.
func Run(srcRoot, dstRoot string, entries []config.Entry) []Result {
	var results []Result
	for _, e := range entries {
		matches, err := filepath.Glob(filepath.Join(srcRoot, e.Pattern))
		if err != nil {
			results = append(results, Result{Rel: e.Pattern, Mode: e.Mode, Err: err})
			continue
		}
		for _, src := range matches {
			rel, err := filepath.Rel(srcRoot, src)
			if err != nil {
				results = append(results, Result{Rel: src, Mode: e.Mode, Err: err})
				continue
			}
			if tracked(srcRoot, rel) {
				results = append(results, Result{Rel: rel, Mode: e.Mode, Tracked: true})
				continue
			}
			dst := filepath.Join(dstRoot, rel)
			results = append(results, apply(src, dst, rel, e.Mode))
		}
	}
	return results
}

// tracked reports whether git tracks rel in the repo at root. A directory counts
// as tracked when any file beneath it is, which is what makes the check work for
// a pattern that matches directories (a skill folder) as well as files.
//
// A git failure — not a repo, git missing — reads as "not tracked" so populate
// keeps working outside a repository, the same way the rest of this package
// degrades rather than refusing.
func tracked(root, rel string) bool {
	out, err := exec.Command("git", "-C", root, "ls-files", "--", rel).Output()
	if err != nil {
		return false
	}
	return len(bytes.TrimSpace(out)) > 0
}

func apply(src, dst, rel string, mode config.Mode) Result {
	r := Result{Rel: rel, Mode: mode}
	// The primary repo's file is the truth, so a copy-mode file that differs is
	// overwritten. The worktree's old content is not thrown away unseen: the
	// diff from it to the primary's version goes back in the Result for the
	// caller to print, so an edit made in the worktree can be re-applied.
	//
	// Lstat, not Stat: only a real file is diffed. A symlink sitting there is
	// left over from when this pattern was symlink-mode, and a broken one would
	// make Stat fail and hide it entirely — either way it is a stale artifact to
	// replace, with no content of the worktree's own to report.
	//
	// A clone-mode directory is the exception: it is a build cache, a diff of it
	// is unreadable, and replacing it would discard the worktree's builds.
	if mode == config.Copy || mode == config.Clone {
		if info, err := os.Lstat(dst); err == nil && info.Mode()&os.ModeSymlink == 0 {
			if info.IsDir() && mode == config.Clone && isDir(src) {
				r.Kept = true
				return r
			}
			diff, err := unifiedDiff(dst, src, rel)
			if err != nil {
				r.Err = err
				return r
			}
			if diff == "" {
				r.Unchanged = true
				return r
			}
			r.Diff = diff
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		r.Err = err
		return r
	}
	// Replace any existing file/link at dst so re-running is idempotent.
	_ = os.Remove(dst)
	switch mode {
	case config.Copy:
		r.Err = copyPath(src, dst)
	case config.Clone:
		r.FullCopy, r.Err = clonePath(src, dst)
	default: // symlink
		r.Err = os.Symlink(src, dst)
	}
	return r
}

func isDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// unifiedDiff returns the unified diff from old to new, or "" when the two are
// identical. `git diff --no-index` works on any two paths outside a repository
// and exits 1 when they differ, so exit 1 is the diff and only a higher code is
// an error. git's own header names both absolute paths; it is replaced with
// worktree/<rel> and primary/<rel>, keeping the hunks from the first "@@" on.
func unifiedDiff(old, new, rel string) (string, error) {
	out, err := exec.Command("git", "diff", "--no-index", "--no-color", "--", old, new).Output()
	if err == nil {
		return "", nil
	}
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 1 {
		return "", fmt.Errorf("diff %s: %w", rel, err)
	}
	hunks := out
	if i := bytes.Index(out, []byte("\n@@")); i >= 0 {
		hunks = out[i+1:]
	}
	return fmt.Sprintf("--- worktree/%s\n+++ primary/%s\n%s", rel, rel, hunks), nil
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("copy of directories not supported: %s", src)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
