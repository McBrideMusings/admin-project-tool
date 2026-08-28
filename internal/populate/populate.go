// Package populate brings gitignored local files (.env, etc.) from the primary
// repo into a worktree, per-pattern as a symlink (default) or a copy.
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
	// Skipped is set when a copy-mode entry found a real file already at the
	// destination and left it alone.
	Skipped bool
	// Tracked is set when the match was left alone because git tracks it. See
	// Run for why a tracked path is never populated.
	Tracked bool
	Err     error
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
	// A copy-mode file is the worktree's own to diverge — overwriting it on a
	// re-run would throw away edits (a worktree-local .env pointed at a different
	// port, say) and replace them with the primary repo's version. A symlink is
	// not the worktree's own by definition, so it is always relaid.
	//
	// Lstat, not Stat: only a real file counts as the worktree's copy. A symlink
	// sitting there is left over from when this pattern was symlink-mode, and a
	// broken one would make Stat fail and hide it entirely — either way it is a
	// stale artifact to replace, not user work to protect.
	if mode == config.Copy {
		if info, err := os.Lstat(dst); err == nil && info.Mode()&os.ModeSymlink == 0 {
			r.Skipped = true
			return r
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
	default: // symlink
		r.Err = os.Symlink(src, dst)
	}
	return r
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
