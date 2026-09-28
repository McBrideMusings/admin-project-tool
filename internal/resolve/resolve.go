// Package resolve turns whatever a user typed — a branch name, an issue/PR
// number, a GitHub issue/PR/project-board URL — into the branch a worktree
// should be created for. It is the single resolution path shared by both front
// doors: `repo branch <input>` (cmd/branch.go) and the TUI's new-worktree
// prompt. Neither should classify input itself; a second implementation drifts.
package resolve

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/classify"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/slug"
)

// Plan is what an input resolved to: the branch to create or check out, whether
// it already exists, and a one-line summary a caller can show before acting.
//
// PRNum keeps the branch's origin rather than discarding it once a name has been
// derived. A PR from a fork has its head branch on the contributor's fork, not on
// origin, so nothing that only knows the name can find it — a caller that forgets
// where the name came from silently forks an empty new branch wearing it. Any
// caller acting on a Plan with PRNum set must fetch the head (git.FetchPRHead)
// before creating the worktree.
type Plan struct {
	Branch   string
	Existing bool
	Summary  string
	PRNum    int // >0 when Branch is a PR's head branch; 0 otherwise
}

// BranchInput resolves input against the repo at dir and nwo ("owner/repo", that
// repo's origin). A URL naming a different repo is an error rather than a branch named
// after a URL. Numbers and issue/PR URLs cost one gh lookup, so callers on a UI
// goroutine must run this off it.
func BranchInput(ctx context.Context, dir, nwo, input string) (Plan, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return Plan{}, errors.New("input must not be empty")
	}

	r := classify.Classify(input)

	// Only the GitHub-shaped inputs need to know which repo this is. Saying so
	// here — rather than refusing every input in a repo whose origin isn't on
	// GitHub — is what lets a plain branch name work in a local-only repo, since
	// creating a branch is pure git.
	if nwo == "" && (r.Kind == classify.KindPR || r.Kind == classify.KindNumber || r.Kind == classify.KindIssue) {
		return Plan{}, fmt.Errorf("%q names an issue or PR, but this repo has no GitHub origin to look it up in — pass a branch name instead", input)
	}

	if r.Kind == classify.KindPR || r.Kind == classify.KindIssue {
		if !strings.EqualFold(r.NWO, nwo) {
			return Plan{}, fmt.Errorf("repo mismatch: URL is for %s but you're in %s", r.NWO, nwo)
		}
	}

	switch r.Kind {
	case classify.KindPR, classify.KindNumber, classify.KindIssue:
		// All three reach the same lookup: issues and PRs share one number sequence
		// per repo, so #N is exactly one object and asking GitHub what it is beats
		// trusting the input's claimed type. An /issues/<n> URL that actually names
		// a PR therefore resolves to the PR's head branch, exactly as the bare
		// number does; a merged or closed PR still yields its head branch.
		return numberPlan(ctx, nwo, r.Number)
	default: // KindText
		switch {
		case git.BranchExistsLocal(ctx, dir, input):
			return Plan{Branch: input, Existing: true, Summary: fmt.Sprintf("Branch %q (local)", input)}, nil
		case git.BranchExistsRemote(ctx, dir, input):
			return Plan{Branch: input, Existing: true, Summary: fmt.Sprintf("Branch %q (on origin)", input)}, nil
		}
		// A bead's id is ordinary text — nothing about its shape tells it apart from
		// a branch name — so it can only be recognized by asking the workspace
		// whether that id exists. The lookup runs after the branch checks so an
		// existing branch always wins, and only in a repo that has beads at all.
		if p, ok := beadPlan(ctx, input); ok {
			return p, nil
		}
		// Text that names no existing branch is about to become one, so it has to
		// be a legal ref. Rejecting here — the moment the input is understood —
		// is what lets a caller report it against the input the user typed,
		// instead of git failing with an opaque status deep in the create path.
		// This is where a pasted URL git can't spell as a ref lands.
		if !git.ValidBranchName(ctx, dir, input) {
			return Plan{}, fmt.Errorf("invalid branch name %q", input)
		}
		return Plan{Branch: input, Existing: false, Summary: fmt.Sprintf("New branch %q", input)}, nil
	}
}

// beadPlan resolves input as a bead id, so typing "bd-4f2" builds a worktree on
// "bd-4f2-add-password-reset" the same way an issue number builds one on
// "42-add-password-reset". ok is false in a repo with no beads workspace, or when
// no bead carries that id — both of which leave the input to be a new branch name.
func beadPlan(ctx context.Context, input string) (Plan, bool) {
	if _, err := bd.Detect(ctx, ""); err != nil {
		return Plan{}, false
	}
	beads, err := bd.List(ctx, "")
	if err != nil {
		return Plan{}, false
	}
	for _, b := range beads {
		if !strings.EqualFold(b.ID, input) {
			continue
		}
		branch := slug.FromBead(b.ID, b.Title)
		return Plan{
			Branch:  branch,
			Summary: fmt.Sprintf("Bead %s: %s", b.ID, b.Title),
		}, true
	}
	return Plan{}, false
}

// numberPlan resolves #N with one authoritative lookup that reports whether the
// number is an issue or a PR — and, for a PR, its head branch in whatever state
// the PR is in — then builds the matching plan.
func numberPlan(ctx context.Context, nwo string, num int) (Plan, error) {
	ref, err := gh.ResolveNumber(ctx, nwo, num)
	if err != nil {
		return Plan{}, err
	}
	if ref.IsPR {
		return Plan{
			Branch:   ref.HeadBranch,
			Existing: true,
			Summary:  fmt.Sprintf("PR #%d (%s): %s → branch %q", num, ref.State, ref.Title, ref.HeadBranch),
			PRNum:    num,
		}, nil
	}
	return issueBranchPlan(num, ref.Title), nil
}

// issueBranchPlan derives the <n>-<slug> branch an issue gets worked on.
func issueBranchPlan(num int, title string) Plan {
	branch := slug.FromIssue(num, title)
	return Plan{
		Branch:   branch,
		Existing: false,
		Summary:  fmt.Sprintf("Issue #%d: %s → branch %q", num, title, branch),
	}
}
