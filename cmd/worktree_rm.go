package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/resolve"
	"github.com/McBrideMusings/admin-project-tool/internal/shim"
	"github.com/McBrideMusings/admin-project-tool/internal/wthooks"
)

var worktreeRmCmd = &cobra.Command{
	Use:   "rm [branch|path|issue/PR|bead]",
	Short: "Remove one worktree, then its branch",
	Long: `Remove one registered worktree, then its branch.

The input is a worktree's path, or anything 'admin worktree <input>' accepts —
a branch name, an issue/PR number or URL, a bead id — naming the branch a
worktree holds. With no input, the worktree containing the current directory
is removed and the shell is moved back to the primary checkout.

Refused without --force when the worktree has uncommitted changes, a paused
rebase/merge/cherry-pick/revert/bisect, or commits that live on no origin ref
(a branch whose PR GitHub reports merged is exempt). The main working tree is
never removed, --force or not.

The project's pre_remove hook (ADR-0024) runs first; a non-zero exit keeps the
worktree and its branch and its output is the reason. With --force the hook
still runs, and the removal goes ahead whatever it returns. --force also
stashes uncommitted files (git stash -u) before the removal when git can.

The branch is deleted only when 'admin worktree --prune' would call it dead
(merged, PR merged, remote gone, or fully on origin); otherwise it stays and
the reason is printed. --force deletes it regardless.

A dispatched workflow worker (ADMIN_TARGET set) may not remove its own
worktree — the one it runs in, or the one its dispatch exported as
ADMIN_WORKER_WORKTREE, from any directory: the dashboard's reaper retires
workers, and removing one under it strands its live-work row and concurrency
slot.

To create a worktree for a branch literally named "rm", use
'admin worktree -- rm'.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		force, _ := cmd.Flags().GetBool("force")
		yes, _ := cmd.Flags().GetBool("yes")
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		input := ""
		if len(args) == 1 {
			input = args[0]
		}
		return runWorktreeRm(cmd.Context(), cwd, input, force, yes)
	},
}

func init() {
	worktreeRmCmd.Flags().Bool("force", false, "remove despite uncommitted changes, a paused operation, unpushed commits or a pre_remove veto, and delete the branch")
	worktreeRmCmd.Flags().BoolP("yes", "y", false, "skip the confirmation prompt")
	worktreeCmd.AddCommand(worktreeRmCmd)
}

// errRmRefused marks a refusal that --force would override, so the message
// can say so once.
var errRmRefused = errors.New("refused")

// runWorktreeRm removes one worktree of the repo containing cwd, then its
// branch. Every check runs before anything changes; pre_remove is the first
// step that acts, so a veto leaves nothing half-done.
func runWorktreeRm(ctx context.Context, cwd, input string, force, yes bool) error {
	repoRoot, err := git.RepoRoot(ctx, cwd)
	if err != nil {
		return err
	}
	wt, err := rmTarget(ctx, cwd, repoRoot, input)
	if err != nil {
		return err
	}
	if wt.Bare || git.SamePath(wt.Path, repoRoot) {
		return fmt.Errorf("%s is the main working tree — admin never removes it", wt.Path)
	}
	self := pathWithin(cwd, wt.Path)
	own := os.Getenv("ADMIN_WORKER_WORKTREE")
	if os.Getenv("ADMIN_TARGET") != "" && (self || (own != "" && git.SamePath(own, wt.Path))) {
		return fmt.Errorf("this is dispatched worker %q's own worktree — the dashboard's reaper removes it once the worker is done", os.Getenv("ADMIN_TARGET"))
	}

	// Judge "on origin" and "remote gone" against current origin, as --prune does.
	git.FetchPrune(ctx, repoRoot)
	nwo, _ := git.OriginNWO(ctx, repoRoot)
	merged := mergedPRHeads(ctx, nwo)

	st, stErr := git.WorkingStatus(ctx, wt.Path)
	var reasons []string
	switch {
	case stErr != nil:
		reasons = append(reasons, "its status can't be read: "+stErr.Error())
	case !st.Clean():
		reasons = append(reasons, fmt.Sprintf("uncommitted changes (%d staged, %d unstaged, %d untracked)", st.Staged, st.Unstaged, st.Untracked))
	}
	if wt.Op != "" {
		reasons = append(reasons, "a paused "+wt.Op)
	}
	switch {
	case wt.Branch == "":
		reasons = append(reasons, "a detached HEAD, whose commits no branch keeps")
	case git.HasCommitsNotOnOrigin(ctx, repoRoot, wt.Branch) && !merged[wt.Branch]:
		reasons = append(reasons, "commits on branch "+wt.Branch+" that live on no origin ref")
	}
	if len(reasons) > 0 && !force {
		return fmt.Errorf("%w: %s has %s — pass --force to remove it anyway", errRmRefused, wt.Path, strings.Join(reasons, "; "))
	}

	deleteBranch, keepWhy := false, ""
	if wt.Branch != "" {
		deleteBranch, keepWhy = rmBranchFate(ctx, repoRoot, wt.Branch, merged, force)
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  Worktree: %s\n", wt.Path)
	switch {
	case wt.Branch == "":
		fmt.Fprintln(os.Stderr, "  Branch:   (detached)")
	case deleteBranch:
		fmt.Fprintf(os.Stderr, "  Branch:   %s — delete\n", wt.Branch)
	default:
		fmt.Fprintf(os.Stderr, "  Branch:   %s — keep (%s)\n", wt.Branch, keepWhy)
	}
	for _, r := range reasons {
		fmt.Fprintf(os.Stderr, "  Forced:   %s\n", r)
	}
	fmt.Fprintln(os.Stderr)
	if !yes && !confirm("Remove? (enter/y: yes · esc/n: cancel) ") {
		fmt.Fprintln(os.Stderr, "Cancelled.")
		return nil
	}

	if err := wthooks.PreRemove(ctx, repoRoot, wt.Path, force); err != nil {
		return fmt.Errorf("kept %s: %w", wt.Path, err)
	}
	// Forced over uncommitted files: stash them so the removal is recoverable.
	// A conflicted index or a paused operation can't be stashed, and an
	// unreadable status gives nothing to go on, so those are removed as they are.
	switch {
	case !force || (stErr == nil && st.Clean()):
	case stErr == nil && st.Conflicted == 0 && wt.Op == "":
		if err := git.StashWorktree(ctx, wt.Path); err != nil {
			return fmt.Errorf("kept %s: stashing its uncommitted files: %w", wt.Path, err)
		}
		fmt.Fprintln(os.Stderr, "stashed uncommitted files in stash@{0}")
	default:
		fmt.Fprintln(os.Stderr, "not stashing: a conflicted, paused or unreadable tree can't be stashed — its uncommitted files are discarded")
	}
	// A herdr workspace on the worktree closes with it. When this process runs
	// inside the worktree, that workspace may be the one holding it, so it
	// closes last rather than kill the removal halfway.
	if !self {
		if _, err := shim.CloseWorkspaceAt(wt.Path); err != nil {
			return fmt.Errorf("kept %s: closing the herdr workspace: %w", wt.Path, err)
		}
	}
	if err := git.RemoveWorktree(ctx, repoRoot, wt.Path, force); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "removed %s\n", wt.Path)
	if deleteBranch {
		if err := git.DeleteBranch(ctx, repoRoot, wt.Branch, true); err != nil {
			return fmt.Errorf("worktree removed, but deleting branch %s failed: %w", wt.Branch, err)
		}
		fmt.Fprintf(os.Stderr, "deleted branch %s\n", wt.Branch)
	} else if wt.Branch != "" {
		fmt.Fprintf(os.Stderr, "kept branch %s (%s)\n", wt.Branch, keepWhy)
	}
	if self {
		shim.PrintCD(repoRoot)
		if _, err := shim.CloseWorkspaceAt(wt.Path); err != nil {
			fmt.Fprintf(os.Stderr, "closing the herdr workspace: %v\n", err)
		}
	}
	return nil
}

// rmTarget resolves input to one registered worktree: an existing directory
// that is a worktree path wins, then whatever resolve.BranchInput makes of
// it, matched by branch. An empty input is the worktree containing cwd.
func rmTarget(ctx context.Context, cwd, repoRoot, input string) (git.Worktree, error) {
	wts, err := git.Worktrees(ctx, repoRoot)
	if err != nil {
		return git.Worktree{}, err
	}
	if input == "" {
		var best git.Worktree
		for _, w := range wts {
			if pathWithin(cwd, w.Path) && len(w.Path) > len(best.Path) {
				best = w
			}
		}
		if best.Path == "" {
			return git.Worktree{}, fmt.Errorf("%s is in no worktree of %s", cwd, repoRoot)
		}
		return best, nil
	}
	if fi, err := os.Stat(input); err == nil && fi.IsDir() {
		abs, _ := filepath.Abs(input)
		for _, w := range wts {
			if git.SamePath(w.Path, abs) {
				return w, nil
			}
		}
	}
	nwo, _ := git.OriginNWO(ctx, repoRoot)
	plan, err := resolve.BranchInput(ctx, repoRoot, nwo, input)
	if err != nil {
		return git.Worktree{}, err
	}
	for _, w := range wts {
		if w.Branch == plan.Branch {
			return w, nil
		}
	}
	return git.Worktree{}, fmt.Errorf("no worktree holds branch %q (%s)", plan.Branch, plan.Summary)
}

// rmBranchFate decides whether removing a worktree also deletes its branch:
// yes when internal/git.ClassifyDead calls it dead — the same judgment
// --prune makes — or under force; otherwise it stays, and why says so.
func rmBranchFate(ctx context.Context, repoRoot, branch string, merged map[string]bool, force bool) (del bool, why string) {
	if force {
		return true, ""
	}
	all, err := git.LocalBranches(ctx, repoRoot, git.CurrentBranch(ctx, repoRoot), nil)
	if err != nil {
		return false, "its state can't be read: " + err.Error()
	}
	for _, b := range all {
		if b.Name != branch {
			continue
		}
		dead, _ := git.ClassifyDead(ctx, repoRoot, []git.BranchInfo{b}, git.DefaultBranch(ctx, repoRoot), merged)
		if len(dead) > 0 {
			return true, ""
		}
		return false, "not merged and not on origin; --force deletes it"
	}
	return false, "no local branch of that name"
}

// pathWithin reports whether path is dir or lies inside it, symlinks resolved.
func pathWithin(path, dir string) bool {
	p, d := git.ResolvePath(path), git.ResolvePath(dir)
	return p == d || strings.HasPrefix(p, d+string(filepath.Separator))
}
