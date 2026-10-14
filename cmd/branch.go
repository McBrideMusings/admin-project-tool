package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/resolve"
)

var branchCmd = &cobra.Command{
	Use:   "branch [input]",
	Short: "Resolve input to a branch and check it out in the current working tree",
	Long: `Resolve any of the following to a branch and create/check it out right
here — no worktree, no separate directory:

  branch name        my-feature            (new branch, forked from origin/<default>)
                     feat/existing         (checks it out if it already exists)
  issue/PR number    42  or  #42
  PR URL             https://github.com/owner/repo/pull/123
  issue URL          https://github.com/owner/repo/issues/45

A branch that already has an on-disk worktree elsewhere still just gets
checked out here; it does not move or duplicate that worktree.

With no input, opens the dashboard so you can pick one instead.

--prune deletes local branches that are provably safe to drop (merged into
the default branch, PR-merged on GitHub, remote-gone, or fully pushed) —
origin is never touched, and a branch holding commits that live nowhere else
is never offered.

Contrast 'admin worktree <input>', which resolves the same input set but
creates a SEPARATE worktree for the branch, leaving the current one alone.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		prune, _ := cmd.Flags().GetBool("prune")
		if prune {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			yes, _ := cmd.Flags().GetBool("yes")
			return runPrune(cmd.Context(), pruneBranches, dryRun, yes)
		}
		if len(args) == 0 {
			return runDashboard(cmd.Context(), "")
		}
		return runBranchCheckout(cmd.Context(), args[0])
	},
}

func init() {
	branchCmd.Flags().BoolP("yes", "y", false, "skip the confirmation prompt (--prune only)")
	branchCmd.Flags().Bool("prune", false, "delete local branches that are provably safe to drop")
	branchCmd.Flags().Bool("dry-run", false, "with --prune, list what would be deleted without deleting it")
}

// runBranchCheckout resolves input and creates/checks out the branch in the
// current working tree — no worktree. Contrast runWorktree (cmd/worktree.go),
// which resolves the same input set into a separate worktree.
func runBranchCheckout(ctx context.Context, input string) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := git.RepoRoot(ctx, dir); err != nil {
		return err
	}
	// A missing or non-GitHub origin is not fatal here. Only an issue/PR number or
	// a GitHub URL needs the repo's name-with-owner; a typed branch name is pure
	// git and works in a repo with no remote at all.
	nwo, _ := git.OriginNWO(ctx, dir)

	plan, err := resolve.BranchInput(ctx, dir, nwo, input)
	if err != nil {
		return err
	}

	// A PR head lives on the contributor's fork when the PR came from one, so it
	// has to be fetched into a local branch before it can be checked out here.
	if plan.PRNum > 0 {
		if err := git.FetchPRHead(ctx, dir, plan.PRNum, plan.Branch); err != nil {
			return err
		}
	}

	action, err := git.CheckoutOrCreate(ctx, dir, plan.Branch)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s branch %s\n", action, plan.Branch)
	return nil
}
