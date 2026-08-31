package git

import (
	"context"
	"strings"
)

// HasCommitsNotOnOrigin reports whether branch holds any commit that lives on
// no origin ref at all. This is the hard backstop ClassifyDead checks first: it
// is deliberately broader than "ahead of its own upstream" (BranchInfo.Ahead) —
// a branch pushed under one name and later merged under another still has every
// commit reachable from *some* origin ref, and only a check against every
// remote-tracking ref, not just this branch's counterpart, can see that.
//
// A read that fails (e.g. no origin at all, so --remotes=origin matches nothing)
// answers true, not false — the same fail-safe direction as
// HasUnpushedCommits: a broken read must never claim a branch is provably empty
// of local-only work, which is what would let ClassifyDead treat it as safe to
// drop.
func HasCommitsNotOnOrigin(ctx context.Context, branch string) bool {
	out, err := runner(ctx, "rev-list", "refs/heads/"+branch, "--not", "--remotes=origin")
	if err != nil {
		return true
	}
	return strings.TrimSpace(out) != ""
}

// DeadBranch is a branch ClassifyDead judged safe to delete, and why.
type DeadBranch struct {
	Name   string
	Reason string
}

// ClassifyDead splits candidates into branches provably safe to delete and
// survivors that need a human to look. Ported from admin_lib/gitwt's
// classify_dead: origin is never touched, and the read is deliberately
// conservative.
//
// mergedPRs holds the head-branch names of PRs GitHub has merged (from gh) —
// the only way to catch a squash or rebase merge, which lands the work under
// new SHAs and leaves no trace on the default branch for MergedIntoDefault to
// find. Pass an empty map when gh is unavailable or the repo has no GitHub
// origin; every branch just falls through to the git-only checks.
//
// Precedence, first match wins:
//
//  1. Holds a commit that lives on no origin ref, and gh does not say its PR
//     merged: survivor. This is the hard backstop — nothing below can override
//     it — and is what stops a fresh branch, a worktree's branch, or a stale
//     personal branch with unlanded work from ever being offered.
//  2. Its upstream existed and is gone, and origin carries nothing under this
//     name now: dead, "remote gone".
//  3. gh says its PR merged, and it holds no commits ahead of its own upstream:
//     dead, "PR merged". Gated on not-ahead so a name reused for new work after
//     the merge survives instead of being read as the old, landed branch.
//  4. Reachable from the default branch (`git branch --merged`): dead, "merged".
//  5. Origin has a counterpart and this branch is not ahead of it: dead,
//     "on origin" — every local commit already lives there.
//  6. Otherwise: survivor.
func ClassifyDead(ctx context.Context, candidates []BranchInfo, def string, mergedPRs map[string]bool) (dead []DeadBranch, survivors []BranchInfo) {
	merged, _ := MergedIntoDefault(ctx, def)
	gone, fullyPushed := UpstreamState(candidates)
	for _, b := range candidates {
		ahead := b.HasRemote && b.Ahead > 0
		switch {
		case HasCommitsNotOnOrigin(ctx, b.Name) && !mergedPRs[b.Name]:
			survivors = append(survivors, b)
		case gone[b.Name]:
			dead = append(dead, DeadBranch{Name: b.Name, Reason: "remote gone"})
		case mergedPRs[b.Name] && !ahead:
			dead = append(dead, DeadBranch{Name: b.Name, Reason: "PR merged"})
		case merged[b.Name]:
			dead = append(dead, DeadBranch{Name: b.Name, Reason: "merged"})
		case fullyPushed[b.Name]:
			dead = append(dead, DeadBranch{Name: b.Name, Reason: "on origin"})
		default:
			survivors = append(survivors, b)
		}
	}
	return dead, survivors
}
