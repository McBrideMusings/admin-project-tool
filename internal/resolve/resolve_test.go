package resolve

import (
	"context"
	"strings"
	"testing"
)

// These cases all settle before the gh lookup, so they need no network: a
// mismatched URL is rejected on sight, and free-form text is answered by local
// git alone.

func TestBranchInputRejectsOtherRepoURL(t *testing.T) {
	_, err := BranchInput(context.Background(), t.TempDir(), "owner/repo", "https://github.com/other/proj/issues/45")
	if err == nil {
		t.Fatal("want an error for a URL naming a different repo, got none")
	}
	if !strings.Contains(err.Error(), "other/proj") {
		t.Fatalf("error should name the URL's repo, got %q", err)
	}
}

func TestBranchInputRejectsURLGitCannotSpell(t *testing.T) {
	// A GitHub URL that is neither an issue nor a PR (here an Actions run)
	// classifies as free-form text. It names no branch, and git won't take it as
	// a ref, so it must be rejected rather than become a branch name.
	in := "https://github.com/owner/repo/actions/runs/30307506559"
	_, err := BranchInput(context.Background(), t.TempDir(), "owner/repo", in)
	if err == nil {
		t.Fatalf("want an error for %q, got none", in)
	}
	if !strings.Contains(err.Error(), "invalid branch name") {
		t.Fatalf("want an invalid-branch-name error, got %q", err)
	}
}

func TestBranchInputPlainNameIsANewBranch(t *testing.T) {
	plan, err := BranchInput(context.Background(), t.TempDir(), "owner/repo", "some-new-branch-name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Branch != "some-new-branch-name" {
		t.Fatalf("branch = %q, want %q", plan.Branch, "some-new-branch-name")
	}
	if plan.Existing {
		t.Error("a name matching no ref should not be reported as existing")
	}
}

func TestBranchInputRejectsEmpty(t *testing.T) {
	if _, err := BranchInput(context.Background(), t.TempDir(), "owner/repo", "   "); err == nil {
		t.Fatal("want an error for empty input, got none")
	}
}

// TestBranchInputWithoutGitHubOrigin pins that a repo with no GitHub origin can
// still make a worktree from a typed branch name. Only an issue/PR number or a
// GitHub URL needs the repo's name-with-owner; creating a branch is pure git, so
// an empty nwo must not refuse it. A number in the same repo has nowhere to be
// looked up and is rejected by name.
func TestBranchInputWithoutGitHubOrigin(t *testing.T) {
	plan, err := BranchInput(context.Background(), t.TempDir(), "", "local-only-branch")
	if err != nil {
		t.Fatalf("a branch name in a repo with no GitHub origin: %v", err)
	}
	if plan.Branch != "local-only-branch" {
		t.Fatalf("branch = %q, want %q", plan.Branch, "local-only-branch")
	}

	_, err = BranchInput(context.Background(), t.TempDir(), "", "42")
	if err == nil {
		t.Fatal("want an error for #42 with no GitHub origin, got none")
	}
	if !strings.Contains(err.Error(), "GitHub") {
		t.Fatalf("error should say the repo has no GitHub origin, got %q", err)
	}
}
