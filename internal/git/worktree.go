package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// The git operations that can be left half-finished in a worktree, as reported
// by Worktree.Op. Named once here because two packages switch on them — the
// detection in pausedOp and the label the dashboard renders — and a spelling that
// drifted between the two would silently fall through to the raw string instead
// of failing to build.
const (
	OpRebase     = "rebase"
	OpMerge      = "merge"
	OpCherryPick = "cherry-pick"
	OpRevert     = "revert"
	OpBisect     = "bisect"
)

// Worktree is one entry from `git worktree list --porcelain`.
type Worktree struct {
	Path     string
	Head     string
	Branch   string // the branch this worktree holds, including one held mid-operation
	Detached bool
	Bare     bool
	// Op is the git operation left half-finished in this worktree — one of the
	// Op* constants above — or "" when nothing is paused.
	Op string
}

// Worktrees parses `git worktree list --porcelain` into structured entries. This
// reports every worktree regardless of where it lives, so it does not care
// whether the dashboard, Claude Code, or plain git created it.
func Worktrees(ctx context.Context) ([]Worktree, error) {
	return WorktreesFor(ctx, "")
}

// WorktreesFor is Worktrees for a directory other than the process's own cwd —
// dir == "" runs in the process cwd exactly as Worktrees. Needed by the -w/
// --worktree flag's resolver (internal/worktree), which must list the
// worktrees of the repo containing a directory it was merely handed, not the
// one the admin process happens to be running in — same shape as
// RepoRootFor's own doc comment above.
func WorktreesFor(ctx context.Context, dir string) ([]Worktree, error) {
	args := []string{"worktree", "list", "--porcelain"}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := runner(ctx, args...)
	if err != nil {
		return nil, err
	}
	var (
		result []Worktree
		cur    Worktree
		open   bool
	)
	flush := func() {
		if open {
			result = append(result, cur)
		}
		cur = Worktree{}
		open = false
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.Path = strings.TrimPrefix(line, "worktree ")
			open = true
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			cur.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		case line == "detached":
			cur.Detached = true
		case line == "bare":
			cur.Bare = true
		}
	}
	flush()
	for i := range result {
		w := &result[i]
		if w.Bare {
			continue
		}
		// No `git rev-parse --absolute-git-dir` fallback when this comes up empty.
		// The layout read only fails when the worktree's `.git` is gone, and there
		// rev-parse is either useless or actively wrong: if the directory itself is
		// missing, `git -C <path>` exits 128 too; if the directory survives but its
		// `.git` does not, rev-parse walks *up* and answers with the parent repo's
		// git dir — so this worktree would be labelled with the main repo's paused
		// rebase, and detachedBranch would hand back the main repo's branch.
		gitDir := worktreeGitDir(w.Path)
		if gitDir == "" {
			continue
		}
		w.Op = pausedOp(gitDir)
		if w.Detached {
			w.Branch = detachedBranch(gitDir)
		}
	}
	return result, nil
}

// ResolvePath returns path with symlinks resolved, falling back to a lexical
// clean when it can't be resolved (a path that doesn't exist yet). Comparing
// worktree paths needs this because git reports the real path of a checkout
// reached through a symlinked parent — on macOS a repo under /var or /tmp comes
// back as /private/var or /private/tmp, so the string git prints and the string
// a caller computed name the same directory without matching.
func ResolvePath(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return filepath.Clean(path)
}

// SamePath reports whether a and b name the same directory once symlinks are
// resolved.
func SamePath(a, b string) bool {
	return ResolvePath(a) == ResolvePath(b)
}

// worktreeGitDir resolves a worktree's private git dir from its checkout path by
// reading the repository layout directly: a linked worktree's `.git` is a file
// holding `gitdir: <path>`, and the main worktree's `.git` is the git dir itself.
// Going to the filesystem rather than `git rev-parse --absolute-git-dir` keeps
// paused-operation detection free of one subprocess per worktree, which a repo
// holding a dozen of them would otherwise pay on every reload.
func worktreeGitDir(path string) string {
	p := filepath.Join(path, ".git")
	info, err := os.Stat(p)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return p
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: ")
	if !ok {
		return ""
	}
	dir = strings.TrimSpace(dir)
	if dir != "" && !filepath.IsAbs(dir) {
		dir = filepath.Join(path, dir)
	}
	return dir
}

// pausedOp reports the git operation left half-finished in the worktree whose git
// dir is gitDir, or "" when none is. Each of these operations parks a marker in
// the worktree's own git dir and removes it on finish or abort, so the marker's
// presence *is* the paused state — there is no separate flag to consult.
//
// Only a rebase or a bisect detaches HEAD. A conflicted merge, cherry-pick, or
// revert leaves HEAD sitting on its branch, so `git worktree list` shows nothing
// unusual and the worktree reads as an ordinary dirty one — which is why this runs
// for every worktree rather than only the detached ones.
//
// Rebase is checked first because it is the operation that contains the others:
// a pick that conflicts mid-rebase writes CHERRY_PICK_HEAD alongside the rebase
// state, and the rebase is the thing to go deal with.
func pausedOp(gitDir string) string {
	for _, c := range []struct{ marker, op string }{
		// An interactive/merge rebase, then the apply-backend rebase.
		{"rebase-merge", OpRebase},
		{"rebase-apply", OpRebase},
		{"MERGE_HEAD", OpMerge},
		{"CHERRY_PICK_HEAD", OpCherryPick},
		{"REVERT_HEAD", OpRevert},
		{"BISECT_LOG", OpBisect},
	} {
		if _, err := os.Stat(filepath.Join(gitDir, c.marker)); err == nil {
			return c.op
		}
	}
	return ""
}

// detachedBranch recovers the branch a detached worktree still holds because an
// operation is paused in it. `git worktree list --porcelain` prints `detached` and
// no `branch` line for these, but git has not released the branch: it is recorded
// in the worktree's private git dir and `git branch -d/-D` refuses to delete it
// ("used by worktree at …"). Reading porcelain alone therefore makes a rebasing
// worktree look unattached, and its branch look like a bare deletable ref.
//
// Returns "" for a genuinely detached worktree — one checked out at a raw commit,
// which holds no branch at all.
func detachedBranch(gitDir string) string {
	// An interactive/merge rebase, then the apply-backend rebase. Both record the
	// branch being replayed in head-name.
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		if b := readRef(filepath.Join(gitDir, dir, "head-name")); b != "" {
			return b
		}
	}
	// A bisect run records the branch to return to in BISECT_START.
	return readRef(filepath.Join(gitDir, "BISECT_START"))
}

// readRef reads a file holding a single ref and returns it as a short branch
// name, or "" if the file is missing or does not name a branch. head-name writes
// the full `refs/heads/x`; BISECT_START writes a bare branch name, or a raw commit
// when the bisect started from a detached HEAD — which names no branch.
func readRef(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(raw))
	ref = strings.TrimPrefix(ref, "refs/heads/")
	if ref == "" || isHex40(ref) {
		return ""
	}
	return ref
}

// isHex40 reports whether s is a full commit SHA rather than a branch name.
func isHex40(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// ByBranch maps branch name -> worktree path for non-main worktrees.
func ByBranch(list []Worktree, repoRoot string) map[string]string {
	m := map[string]string{}
	for _, w := range list {
		if w.Branch != "" && w.Path != "" && w.Path != repoRoot {
			m[w.Branch] = w.Path
		}
	}
	return m
}
