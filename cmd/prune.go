package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// pruneKind selects which candidate set `admin branch --prune` / `admin
// worktree --prune` sweeps.
type pruneKind int

const (
	// pruneBranches sweeps local branches that hold no worktree of their own.
	pruneBranches pruneKind = iota
	// pruneWorktrees sweeps worktrees (main tree and dirty ones excluded) whose
	// branch is dead, deleting that branch along with the worktree.
	pruneWorktrees
)

// mergedPRLimit caps how many recent merged PRs the merged-set lookup scans —
// enough to catch a squash/rebase merge without an unbounded query.
const mergedPRLimit = 200

// runPrune is the shared body of `admin branch --prune` and `admin worktree
// --prune`: fetch, classify, print, confirm, delete. kind picks the candidate
// set; internal/git.ClassifyDead is the one place either verb decides what
// counts as safe to drop, so the two commands can never disagree about it.
func runPrune(ctx context.Context, kind pruneKind, dryRun, yes bool) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	repoRoot, err := git.RepoRoot(ctx, dir)
	if err != nil {
		return err
	}
	// Never mutates origin — only refreshes/prunes the local remote-tracking
	// refs so "gone" and "fully pushed" are judged against current origin.
	git.FetchPrune(ctx, dir)

	nwo, _ := git.OriginNWO(ctx, dir)
	merged := mergedPRHeads(ctx, nwo)

	if kind == pruneWorktrees {
		return pruneDeadWorktrees(ctx, dir, repoRoot, merged, dryRun, yes)
	}
	return pruneDeadBranches(ctx, dir, merged, dryRun, yes)
}

// mergedPRHeads is the head-branch set of PRs GitHub has merged, for the
// squash/rebase case git's own --merged check can't see. Empty (never an
// error) when there is no GitHub origin or the gh lookup fails, so a repo
// with no gh CLI still classifies correctly against git alone, just more
// conservatively.
func mergedPRHeads(ctx context.Context, nwo string) map[string]bool {
	out := map[string]bool{}
	if nwo == "" {
		return out
	}
	prs, err := gh.MergedPRs(ctx, nwo, mergedPRLimit)
	if err != nil {
		return out
	}
	for _, pr := range prs {
		out[pr.HeadBranch] = true
	}
	return out
}

// pruneDeadBranches implements `admin branch --prune`: local branches with no
// worktree of their own, excluding the branch currently checked out and the
// default branch, classified and offered for deletion.
func pruneDeadBranches(ctx context.Context, dir string, merged map[string]bool, dryRun, yes bool) error {
	def := git.DefaultBranch(ctx, dir)
	current := git.CurrentBranch(ctx, dir)

	wts, err := git.Worktrees(ctx, dir)
	if err != nil {
		return err
	}
	inWorktree := map[string]bool{}
	for _, w := range wts {
		if w.Branch != "" {
			inWorktree[w.Branch] = true
		}
	}

	all, err := git.LocalBranches(ctx, dir, current, nil)
	if err != nil {
		return err
	}
	var candidates []git.BranchInfo
	for _, b := range all {
		if b.Current || b.Name == def || inWorktree[b.Name] {
			continue
		}
		candidates = append(candidates, b)
	}

	dead, _ := git.ClassifyDead(ctx, dir, candidates, def, merged)
	if len(dead) == 0 {
		fmt.Fprintln(os.Stderr, "no dead branches to prune")
		return nil
	}
	fmt.Fprintln(os.Stderr, "dead local branches:")
	for _, d := range dead {
		fmt.Fprintf(os.Stderr, "  %s  (%s)\n", d.Name, d.Reason)
	}
	if dryRun {
		return nil
	}
	if !yes && !confirm(fmt.Sprintf("delete %d branch(es)? (enter/y: yes · esc/n: cancel) ", len(dead))) {
		fmt.Fprintln(os.Stderr, "cancelled")
		return nil
	}
	var failed int
	for _, d := range dead {
		if err := git.DeleteBranch(ctx, dir, d.Name, true); err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", d.Name, err)
			failed++
			continue
		}
		fmt.Fprintf(os.Stderr, "deleted %s\n", d.Name)
	}
	if failed > 0 {
		return fmt.Errorf("%d branch(es) failed to delete", failed)
	}
	return nil
}

// pruneDeadWorktrees implements `admin worktree --prune`: worktrees (skipping
// the main tree and anything with uncommitted changes) whose branch classifies
// dead, removed along with that branch.
func pruneDeadWorktrees(ctx context.Context, dir, repoRoot string, merged map[string]bool, dryRun, yes bool) error {
	wts, err := git.Worktrees(ctx, dir)
	if err != nil {
		return err
	}
	def := git.DefaultBranch(ctx, dir)
	current := git.CurrentBranch(ctx, dir)
	all, err := git.LocalBranches(ctx, dir, current, nil)
	if err != nil {
		return err
	}
	byName := make(map[string]git.BranchInfo, len(all))
	for _, b := range all {
		byName[b.Name] = b
	}

	type victim struct{ path, branch, reason string }
	var victims []victim
	var skippedDirty []string
	for _, w := range wts {
		if w.Bare || w.Path == repoRoot || w.Branch == "" {
			continue
		}
		bi, ok := byName[w.Branch]
		if !ok {
			continue
		}
		dead, _ := git.ClassifyDead(ctx, dir, []git.BranchInfo{bi}, def, merged)
		if len(dead) == 0 {
			continue
		}
		st, err := git.WorkingStatus(ctx, w.Path)
		if err != nil || !st.Clean() {
			skippedDirty = append(skippedDirty, w.Path)
			continue
		}
		victims = append(victims, victim{path: w.Path, branch: w.Branch, reason: dead[0].Reason})
	}
	for _, p := range skippedDirty {
		fmt.Fprintf(os.Stderr, "skipping %s — uncommitted changes\n", p)
	}
	if len(victims) == 0 {
		fmt.Fprintln(os.Stderr, "no dead worktrees to prune")
		return nil
	}
	fmt.Fprintln(os.Stderr, "dead worktrees:")
	for _, v := range victims {
		fmt.Fprintf(os.Stderr, "  %s  [%s] (%s)\n", v.path, v.branch, v.reason)
	}
	if dryRun {
		return nil
	}
	if !yes && !confirm(fmt.Sprintf("remove %d worktree(s) and their branches? (enter/y: yes · esc/n: cancel) ", len(victims))) {
		fmt.Fprintln(os.Stderr, "cancelled")
		return nil
	}
	var failed int
	for _, v := range victims {
		if err := git.RemoveWorktree(ctx, dir, v.path, false); err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", v.path, err)
			failed++
			continue
		}
		if err := git.DeleteBranch(ctx, dir, v.branch, true); err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", v.branch, err)
			failed++
			continue
		}
		fmt.Fprintf(os.Stderr, "removed %s [%s]\n", v.path, v.branch)
	}
	if failed > 0 {
		return fmt.Errorf("%d worktree(s) failed to remove", failed)
	}
	return nil
}
