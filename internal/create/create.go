// Package create is the single worktree-creation path. Every entry point —
// `repo branch` and pressing enter on an issue/PR — calls Worktree. It does the
// bare minimum: git worktree add, then populate. No post-create commands, no
// dependency install, no branch prefixing.
package create

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/populate"
)

// Result reports where the worktree landed and what populate did.
type Result struct {
	Path     string
	Branch   string
	Populate []populate.Result
}

// Worktree creates a worktree for branch under <root>/<repo>/<dir> (root
// defaults to ~/.worktrees; see config.WorktreeRoot) and runs populate from the
// repo config. Returns the new worktree path so the caller can emit the cd
// sentinel.
//
// How the branch attaches is derived from its actual state, never supplied by
// the caller — a caller deriving a branch name (an issue slug) can't know
// whether it already exists. Branch already checked out in a worktree anywhere:
// open that worktree. Exists locally: check it out. Exists only on origin: fetch
// then check it out. Exists nowhere: fork a new branch from origin/<default>
// (never from whatever the main working tree is on).
func Worktree(ctx context.Context, repoRoot, branch string) (Result, error) {
	dir := sanitizeDir(branch)
	if dir == "" {
		return Result{}, fmt.Errorf("invalid branch name %q", branch)
	}
	// Load config once for both the worktree root and populate. A parse error
	// yields a zero Config, which forces the env-or-default root and skips
	// populate — the same fallback both paths used before.
	cfg, cfgErr := config.Load(repoRoot)
	if cfgErr != nil {
		cfg = config.Config{}
	}
	root := config.WorktreeRoot(cfg)
	path := filepath.Join(root, filepath.Base(repoRoot), dir)
	// git allows only one worktree per branch. If this branch is already checked
	// out in a worktree ANYWHERE — including one at a path we don't compute (an
	// older location, or one Claude Code / plain git made under the repo's own
	// .worktrees/) — opening that worktree is the whole job. Keying idempotency on
	// the branch, not our computed path, is what stops "add another worktree for a
	// branch that already has one" from hard-failing (git exit 128).
	if wts, err := git.Worktrees(ctx); err == nil {
		for _, w := range wts {
			if w.Branch == branch {
				return Result{Path: w.Path, Branch: branch}, nil
			}
		}
	}
	if _, err := os.Stat(path); err == nil {
		// Dir already there. If it's a registered git worktree, creation is a
		// no-op — return its path so the caller just cd's in (populate already
		// ran when it was first made). Only a dir that ISN'T a worktree is a
		// real problem worth failing on.
		if isRegisteredWorktree(ctx, path) {
			return Result{Path: path, Branch: branch}, nil
		}
		// Worktree paths are keyed on the repo's basename, so two checkouts with
		// the same directory name under different parents (~/work/foo and
		// ~/personal/foo) compute the same path. The dir sitting there is then a
		// real worktree — just another repo's — and isRegisteredWorktree, scoped
		// to this repo, says no. Name that case instead of claiming the path
		// isn't a worktree at all.
		if owner := worktreeOwner(ctx, path); owner != "" {
			return Result{}, fmt.Errorf("path %s belongs to another repo at %s", path, owner)
		}
		return Result{}, fmt.Errorf("path exists but is not a git worktree: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{}, err
	}
	// Attach by the branch's actual state, not a caller guess. A caller deriving a
	// branch name (enter-on-issue slugs one from the title) can't know whether it
	// already exists, so trusting a "new" hint lets it collide with a real local
	// branch and hard-fail git worktree add -b (255), or fork a divergent branch
	// over one that already exists on origin. Checking existence here makes every
	// such case a plain checkout instead.
	switch {
	case git.BranchExistsLocal(ctx, branch):
		if err := git.AddExisting(ctx, path, branch); err != nil {
			return Result{}, err
		}
	case git.BranchExistsRemote(ctx, branch):
		// On origin but not local yet — fetch it, then attach.
		if err := git.FetchBranch(ctx, branch); err != nil {
			return Result{}, fmt.Errorf("failed to fetch branch %q from origin: %w", branch, err)
		}
		if err := git.AddExisting(ctx, path, branch); err != nil {
			return Result{}, err
		}
	default:
		if err := git.AddNewBranch(ctx, path, branch, git.OriginBase(ctx)); err != nil {
			return Result{}, err
		}
	}

	res := Result{Path: path, Branch: branch}
	if len(cfg.Populate) > 0 {
		res.Populate = populate.Run(repoRoot, path, cfg.Populate)
	}
	return res, nil
}

// isRegisteredWorktree reports whether path is a worktree git already knows
// about. Compares resolved paths so a symlinked repo root (e.g. /Users vs
// /private on macOS) still matches.
func isRegisteredWorktree(ctx context.Context, path string) bool {
	wts, err := git.Worktrees(ctx)
	if err != nil {
		return false
	}
	target := git.ResolvePath(path)
	for _, w := range wts {
		if git.ResolvePath(w.Path) == target {
			return true
		}
	}
	return false
}

// worktreeOwner returns the main repo directory of the checkout at path, or ""
// if path is not itself the top of a git worktree.
//
// The top-level check is what keeps this honest: `git rev-parse` walks up from
// its working dir, so a plain directory under a worktree root that itself sits
// inside some repo would otherwise report that ancestor. Only a path that is its
// own top-level is a worktree; anything else is just a directory.
//
// This talks to git directly rather than through internal/git because every
// helper there runs in the process's cwd, and the whole question here is about a
// checkout somewhere else.
func worktreeOwner(ctx context.Context, path string) string {
	top, err := gitIn(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil || top == "" || !git.SamePath(top, path) {
		return ""
	}
	// `worktree list` always lists the main worktree first, so its first entry is
	// the repo the checkout belongs to — including when path is that main
	// worktree itself.
	out, err := gitIn(ctx, path, "worktree", "list", "--porcelain")
	if err != nil {
		return top
	}
	for line := range strings.SplitSeq(out, "\n") {
		if owner, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree "); ok {
			return owner
		}
	}
	return top
}

// gitIn runs a read-only git command with dir as its working directory and
// returns trimmed stdout.
func gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func sanitizeDir(branch string) string {
	return strings.ReplaceAll(strings.TrimSpace(branch), "/", "-")
}
