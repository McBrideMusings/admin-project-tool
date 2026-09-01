package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/populate"
)

var populateCmd = &cobra.Command{
	Use:   "populate",
	Short: "Re-run populate for this worktree, or for every worktree with --all",
	Long: `Bring the gitignored files named in the populate config from the primary repo
into a worktree that already exists.

Creating a worktree populates it once, so a worktree made before a pattern was
added never receives it. This applies the current config to a worktree already
on disk.

Patterns come from the PRIMARY checkout's admin.toml [[populate]] table,
merged with its declared archetypes. A symlink entry is always relaid. A copy
entry is skipped when a real file is already there, so a worktree-local edit
is never overwritten.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")
		return runPopulate(cmd.Context(), all)
	},
}

func init() {
	populateCmd.Flags().BoolP("all", "a", false, "populate every worktree of this repo instead of just this one")
}

func runPopulate(ctx context.Context, all bool) error {
	repoRoot, err := git.RepoRoot(ctx)
	if err != nil {
		return err
	}
	// Unlike worktree creation — which falls back to a zero Config so a broken
	// file can't block making a worktree — a parse error is fatal here. Applying
	// the config is the entire job, so carrying on would silently do nothing.
	cfg, err := config.Load(repoRoot)
	if err != nil {
		return err
	}
	if len(cfg.Populate) == 0 {
		fmt.Fprintf(os.Stderr, "No populate patterns configured. Add some to %s.\n",
			filepath.Join(repoRoot, manifest.Filename))
		return nil
	}

	targets, err := populateTargets(ctx, repoRoot, all)
	if err != nil {
		return err
	}

	for _, dst := range targets {
		if len(targets) > 1 {
			fmt.Fprintf(os.Stderr, "\n%s\n", dst)
		}
		reportPopulateResults(populate.Run(repoRoot, dst, cfg.Populate))
	}
	return nil
}

// populateTargets lists the worktrees to populate. The primary repo is never a
// target: it is where the files come from, so populating it would point a file
// at itself.
func populateTargets(ctx context.Context, repoRoot string, all bool) ([]string, error) {
	if !all {
		top, err := git.TopLevel(ctx)
		if err != nil {
			return nil, err
		}
		if git.SamePath(top, repoRoot) {
			return nil, fmt.Errorf("this is the primary repo, not a worktree — cd into a worktree, or pass --all to populate every worktree of %s", filepath.Base(repoRoot))
		}
		return []string{top}, nil
	}

	wts, err := git.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	var targets []string
	for _, w := range wts {
		if w.Bare || git.SamePath(w.Path, repoRoot) {
			continue
		}
		targets = append(targets, w.Path)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("%s has no worktrees to populate", filepath.Base(repoRoot))
	}
	return targets, nil
}

func reportPopulateResults(results []populate.Result) {
	for _, r := range results {
		switch {
		case r.Err != nil:
			fmt.Fprintf(os.Stderr, "populate %s: %v\n", r.Rel, r.Err)
		case r.Tracked:
			fmt.Fprintf(os.Stderr, "skipped  %s (git tracks it)\n", r.Rel)
		case r.Skipped:
			fmt.Fprintf(os.Stderr, "skipped  %s (already there)\n", r.Rel)
		default:
			fmt.Fprintf(os.Stderr, "populate %s (%s)\n", r.Rel, r.Mode)
		}
	}
}
