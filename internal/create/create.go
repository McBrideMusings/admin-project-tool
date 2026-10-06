// Package create is the single worktree-creation path. Every entry point —
// `repo branch` and pressing enter on an issue/PR — calls Worktree. It runs git
// worktree add, then populate, then the project's [worktrees.hooks]
// post_create and post_start (ADR-0024, internal/wthooks). No branch
// prefixing.
package create

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/populate"
	"github.com/McBrideMusings/admin-project-tool/internal/wthooks"
)

// Result reports where the worktree landed, what populate did, and where a
// detached post_start hook writes its output ("" when none started).
type Result struct {
	Path         string
	Branch       string
	Populate     []populate.Result
	PostStartLog string
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
	// Load resolves repoRoot's admin.toml (merged with its declared
	// archetypes) once for both the worktree root and populate. A manifest
	// that fails to load stops the call: a zero Config would silently drop
	// WORKTREE_ROOT and populate.
	cfg, err := config.Load(repoRoot)
	if err != nil {
		return Result{}, fmt.Errorf("loading config: %w", err)
	}
	root := config.WorktreeRoot(cfg)
	path := filepath.Join(root, filepath.Base(repoRoot), dir)
	// git allows only one worktree per branch. If this branch is already checked
	// out in a worktree ANYWHERE — including one at a path we don't compute (an
	// older location, or one Claude Code / plain git made under the repo's own
	// .worktrees/) — opening that worktree is the whole job. Keying idempotency on
	// the branch, not our computed path, is what stops "add another worktree for a
	// branch that already has one" from hard-failing (git exit 128).
	if wts, err := git.Worktrees(ctx, repoRoot); err == nil {
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
		if IsRegisteredWorktree(ctx, repoRoot, path) {
			return Result{Path: path, Branch: branch}, nil
		}
		// Worktree paths are keyed on the repo's basename, so two checkouts with
		// the same directory name under different parents (~/work/foo and
		// ~/personal/foo) compute the same path. The dir sitting there is then a
		// real worktree — just another repo's — and IsRegisteredWorktree, scoped
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
	case git.BranchExistsLocal(ctx, repoRoot, branch):
		if err := git.AddExisting(ctx, repoRoot, path, branch); err != nil {
			return Result{}, err
		}
	case git.BranchExistsRemote(ctx, repoRoot, branch):
		// On origin but not local yet — fetch it, then attach.
		if err := git.FetchBranch(ctx, repoRoot, branch); err != nil {
			return Result{}, fmt.Errorf("failed to fetch branch %q from origin: %w", branch, err)
		}
		if err := git.AddExisting(ctx, repoRoot, path, branch); err != nil {
			return Result{}, err
		}
	default:
		if err := git.AddNewBranch(ctx, repoRoot, path, branch, git.OriginBase(ctx, repoRoot)); err != nil {
			return Result{}, err
		}
	}

	res := Result{Path: path, Branch: branch}
	if len(cfg.Populate) > 0 {
		res.Populate = populate.Run(repoRoot, path, cfg.Populate)
	}
	return res, runCreateHooks(ctx, cfg, repoRoot, &res)
}

// runCreateHooks runs ADR-0024's creation hooks on a worktree Worktree just
// made: post_create, waited on, then post_start, detached. A failing
// post_create keeps the worktree, skips post_start and returns the hook's
// *wthooks.Error — res still names the worktree, so a caller can report where
// it is.
func runCreateHooks(ctx context.Context, cfg config.Config, repoRoot string, res *Result) error {
	hooks := cfg.Worktrees.Hooks
	if hooks.PostCreate != "" {
		if err := wthooks.Run(ctx, wthooks.HookPostCreate, hooks.PostCreate, repoRoot, res.Path, res.Branch); err != nil {
			return fmt.Errorf("worktree kept at %s: %w", res.Path, err)
		}
	}
	if hooks.PostStart != "" {
		log, err := wthooks.Start(wthooks.HookPostStart, hooks.PostStart, repoRoot, res.Path, res.Branch, cfg.LogDir)
		res.PostStartLog = log
		if err != nil {
			return fmt.Errorf("worktree kept at %s: %w", res.Path, err)
		}
	}
	return nil
}

// IsRegisteredWorktree reports whether path is a worktree of the repo at
// repoRoot. Compares resolved paths so a symlinked repo root (e.g. /Users vs
// /private on macOS) still matches.
//
// Exported for admin-3rgp.25's dispatch machinery: a worktree this package
// created can vanish before a queued target under a concurrency ceiling
// actually starts (a background sweeper, or a person cleaning up by hand),
// and the dispatcher must re-check this exact fact — "is this still a real
// worktree" — immediately before starting, rather than trusting the path it
// got back from Worktree minutes or hours earlier.
func IsRegisteredWorktree(ctx context.Context, repoRoot, path string) bool {
	wts, err := git.Worktrees(ctx, repoRoot)
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
func worktreeOwner(ctx context.Context, path string) string {
	top, err := git.TopLevel(ctx, path)
	if err != nil || top == "" || !git.SamePath(top, path) {
		return ""
	}
	// `worktree list` always lists the main worktree first, so its first entry is
	// the repo the checkout belongs to — including when path is that main
	// worktree itself.
	wts, err := git.Worktrees(ctx, path)
	if err != nil || len(wts) == 0 {
		return top
	}
	return wts[0].Path
}

func sanitizeDir(branch string) string {
	return strings.ReplaceAll(strings.TrimSpace(branch), "/", "-")
}
