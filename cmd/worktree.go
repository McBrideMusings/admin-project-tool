package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/create"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/resolve"
)

var worktreeCmd = &cobra.Command{
	Use:   "worktree [input]",
	Short: "Create a separate worktree from a branch name, issue/PR number, or GitHub URL",
	Long: `Resolve any of the following and create a SEPARATE worktree for it — the
current working tree is left exactly where it was:

  branch name        my-feature            (new branch, forked from origin/<default>)
                     feat/existing         (checks it out if it already exists)
  issue/PR number    42  or  #42
  PR URL             https://github.com/owner/repo/pull/123
  issue URL          https://github.com/owner/repo/issues/45
  project board URL  https://github.com/orgs/Org/projects/1/...?issue=Org|repo|45

New branches always fork from origin/<default>, never from whatever the main
working tree is currently checked out on. The new worktree receives the
project's configured populate files, admin.toml among them — admin.toml is
gitignored, so a fresh worktree has no task runner until it is seeded.

With no input, opens the dashboard so you can pick one instead.

Contrast 'admin branch <input>', which resolves the same input set but checks
the branch out in the CURRENT working tree, with no new worktree.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		prune, _ := cmd.Flags().GetBool("prune")
		if prune {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			yes, _ := cmd.Flags().GetBool("yes")
			return runPrune(cmd.Context(), pruneWorktrees, dryRun, yes)
		}
		if len(args) == 0 {
			return runDashboard(cmd.Context())
		}
		yes, _ := cmd.Flags().GetBool("yes")
		return runWorktree(cmd.Context(), args[0], yes)
	},
}

func init() {
	worktreeCmd.Flags().BoolP("yes", "y", false, "skip the confirmation prompt")
	worktreeCmd.Flags().Bool("prune", false, "remove worktrees whose branch is dead, and delete that branch")
	worktreeCmd.Flags().Bool("dry-run", false, "with --prune, list what would be removed without removing it")
}

func runWorktree(ctx context.Context, input string, skipConfirm bool) error {
	repoRoot, err := cwdRepoRoot(ctx)
	if err != nil {
		return err
	}
	// A missing or non-GitHub origin is not fatal here. Only an issue/PR number or
	// a GitHub URL needs the repo's name-with-owner; a typed branch name is pure
	// git and works in a repo with no remote at all. Resolution reports the
	// GitHub-shaped inputs as unsupported when nwo is empty, so failing this early
	// would refuse to make a worktree in a local repo over a lookup it never needs.
	nwo, _ := git.OriginNWO(ctx, repoRoot)

	plan, err := resolve.BranchInput(ctx, repoRoot, nwo, input)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  Detected: %s\n", plan.Summary)
	fmt.Fprintf(os.Stderr, "  Branch:   %s\n", plan.Branch)
	if plan.Existing {
		fmt.Fprintln(os.Stderr, "  Action:   check out existing branch")
	} else if def := git.DefaultBranch(ctx, repoRoot); def != "" {
		// DefaultBranch falls back to a local main/master when there is no
		// origin/HEAD, so the name alone doesn't say the fork point is on a remote.
		// Printing "origin/main" in a repo with no origin names a ref that doesn't
		// exist, and the branch would in fact fork from the local one.
		from := def
		if git.BranchExistsRemote(ctx, repoRoot, def) {
			from = "origin/" + def
		}
		fmt.Fprintf(os.Stderr, "  From:     %s\n", from)
	}
	fmt.Fprintln(os.Stderr)

	if !skipConfirm {
		if !confirm("Continue? (enter/y: yes · esc/n: cancel) ") {
			fmt.Fprintln(os.Stderr, "Cancelled.")
			return nil
		}
	}

	// A PR head lives on the contributor's fork when the PR came from one, so it
	// has to be fetched into a local branch before the worktree can attach to it.
	if plan.PRNum > 0 {
		if err := git.FetchPRHead(ctx, repoRoot, plan.PRNum, plan.Branch); err != nil {
			return err
		}
	}

	res, err := create.Worktree(ctx, repoRoot, plan.Branch)
	if err != nil {
		return err
	}
	reportPopulate(res)
	emitCD(repoRoot, res.Path)
	return nil
}
