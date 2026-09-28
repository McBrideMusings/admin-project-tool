// Package worktree resolves the -w/--worktree flag's <target> against `git
// worktree list --porcelain`, and backs the dashboard's worktree picker with
// the same listing. See cmd/root.go's worktree-flag wiring for how a
// resolved Target becomes the process's effective project root.
package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// Target is a resolved -w argument: the directory everything downstream runs
// against, plus the branch checked out there when it's known.
type Target struct {
	Path   string
	Branch string
}

// AmbiguousError reports that a target string matched more than one worktree
// at the tier that decided the match. Resolve never guesses between
// candidates — the caller prints every one of them (path + branch) and
// exits non-zero.
type AmbiguousError struct {
	Target     string
	Candidates []git.Worktree
}

func (e *AmbiguousError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "worktree target %q is ambiguous — matches:\n", e.Target)
	for _, w := range e.Candidates {
		fmt.Fprintf(&b, "  %s  (%s)\n", w.Path, branchLabel(w))
	}
	return strings.TrimRight(b.String(), "\n")
}

// NotFoundError reports that a target string matched no worktree at any of
// the four tiers.
type NotFoundError struct{ Target string }

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no worktree matches %q (checked: an existing directory, a worktree's directory name, its branch name, then a substring of either)", e.Target)
}

func branchLabel(w git.Worktree) string {
	switch {
	case w.Branch != "":
		return w.Branch
	case w.Detached:
		return "detached"
	default:
		return "(unknown branch)"
	}
}

// listFunc is the seam a test substitutes to resolve against a fixture's
// worktree list without shelling out — see resolve_test.go.
type listFunc func(ctx context.Context, dir string) ([]git.Worktree, error)

// Resolve resolves target against the worktrees of the repo containing dir, in
// four tiers, stopping at the first that produces any match at all:
//
//  1. target names an existing directory (absolute, or relative to dir) —
//     resolves directly, whether or not it belongs to this repo's worktree
//     list.
//  2. exactly one worktree's directory basename equals target.
//  3. exactly one worktree's branch equals target.
//  4. exactly one worktree's branch or directory basename contains target,
//     case-insensitively.
//
// Two or more matches at the tier that decided it, or none at all across
// every tier, is an error: *AmbiguousError or *NotFoundError respectively.
func Resolve(ctx context.Context, dir, target string) (Target, error) {
	return resolveWith(ctx, dir, target, git.Worktrees)
}

func resolveWith(ctx context.Context, dir, target string, list listFunc) (Target, error) {
	if t, ok := resolveExistingDir(ctx, dir, target, list); ok {
		return t, nil
	}

	worktrees, err := list(ctx, dir)
	if err != nil {
		return Target{}, err
	}

	lower := strings.ToLower(target)
	tiers := []func(git.Worktree) bool{
		func(w git.Worktree) bool { return filepath.Base(w.Path) == target },
		func(w git.Worktree) bool { return w.Branch != "" && w.Branch == target },
		func(w git.Worktree) bool {
			return strings.Contains(strings.ToLower(w.Branch), lower) ||
				strings.Contains(strings.ToLower(filepath.Base(w.Path)), lower)
		},
	}
	for _, match := range tiers {
		var hits []git.Worktree
		for _, w := range worktrees {
			if match(w) {
				hits = append(hits, w)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return Target{Path: hits[0].Path, Branch: hits[0].Branch}, nil
		default:
			return Target{}, &AmbiguousError{Target: target, Candidates: hits}
		}
	}
	return Target{}, &NotFoundError{Target: target}
}

// resolveExistingDir is tier 1: target is a real directory, resolved against
// dir when it's relative. It still tries to attach a branch label (by
// matching the resolved path against the worktree list) purely for
// reporting — an unmatched directory is still a valid resolution.
func resolveExistingDir(ctx context.Context, dir, target string, list listFunc) (Target, bool) {
	candidate := target
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(dir, candidate)
	}
	st, err := os.Stat(candidate)
	if err != nil || !st.IsDir() {
		return Target{}, false
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		abs = candidate
	}
	t := Target{Path: abs}
	if worktrees, err := list(ctx, dir); err == nil {
		for _, w := range worktrees {
			if git.SamePath(w.Path, abs) {
				t.Branch = w.Branch
				break
			}
		}
	}
	return t, true
}
