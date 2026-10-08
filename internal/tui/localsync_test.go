package tui

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(c.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// rateLimitedSync makes every GitHub call fail the way a rate-limited `gh` does.
func rateLimitedSync(t *testing.T) {
	t.Helper()
	prevAll, prevPrune := fetchAllFn, fetchPruneFn
	fetchAllFn = func(context.Context, string, data.SyncOpts) data.Remote {
		return data.Remote{
			PRsFetchError:    "PRs: exit status 1: gh: API rate limit already exceeded",
			MergedFetchError: "merged: exit status 1: gh: API rate limit already exceeded",
		}
	}
	fetchPruneFn = func(context.Context, string) error { return nil }
	t.Cleanup(func() { fetchAllFn, fetchPruneFn = prevAll, prevPrune })
}

// pumpMsgs runs cmd and feeds the local-git, sync and delete messages it produces back
// through Update until none are left.
func pumpMsgs(m Model, cmd tea.Cmd) Model {
	queue := []tea.Cmd{cmd}
	for n := 0; len(queue) > 0 && n < 50; n++ {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		if b, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, b...)
			continue
		}
		switch msg.(type) {
		case localDataMsg, syncDoneMsg, deleteStepMsg:
			next, c2 := m.Update(msg)
			m = next.(Model)
			queue = append(queue, c2)
		}
	}
	return m
}

func branchRowNames(m Model) []string {
	var out []string
	for _, r := range m.branchRows() {
		out = append(out, r.name)
	}
	return out
}

// repoWithWorktree is a repo on main holding branch feat-x checked out in a linked
// worktree, with a Model that has read it.
func repoWithWorktree(t *testing.T) Model {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "commit", "--allow-empty", "-m", "init")
	runGit(t, root, "worktree", "add", "-b", "feat-x", filepath.Join(t.TempDir(), "wt"))
	m := New(context.Background(), root, "o/r", "main", "main")
	m.width, m.height = 120, 40
	m.focus = focusBody
	return pumpMsgs(m, loadLocalCmd(m.ctx, m.repoRoot, m.checkoutDir(), m.current, m.def))
}

func TestFailedSyncStillShowsLocalBranches(t *testing.T) {
	rateLimitedSync(t)
	m := repoWithWorktree(t)
	m = pumpMsgs(m, m.startSyncCmd(true))
	if len(m.remote.FetchErrors()) == 0 {
		t.Fatal("fixture: the sync should have failed")
	}
	if got := branchRowNames(m); !slices.Equal(got, []string{"main", "feat-x"}) {
		t.Fatalf("rows after failed sync = %v, want [main feat-x]", got)
	}
}

func TestDeletedBranchLeavesPaneWhenSyncFails(t *testing.T) {
	rateLimitedSync(t)
	m := repoWithWorktree(t)
	m = pumpMsgs(m, m.startSyncCmd(true))

	m.confirming, m.confirmTargets = true, []cleanupTarget{m.targetFor(m.branchInfo("feat-x"))}
	next, cmd := m.startDelete()
	m = pumpMsgs(next.(Model), cmd)

	if got := branchRowNames(m); !slices.Equal(got, []string{"main"}) {
		t.Fatalf("rows after delete = %v, want [main]", got)
	}
}

// A read that started before the delete and finishes after the read that started
// once it was done must not put the deleted row back.
func TestSlowStaleLocalReadCannotResurrectDeletedBranch(t *testing.T) {
	m := repoWithWorktree(t)
	slow := loadLocalCmd(m.ctx, m.repoRoot, m.checkoutDir(), m.current, m.def)().(localDataMsg)
	if !slices.ContainsFunc(slow.d.branches, func(b git.BranchInfo) bool { return b.Name == "feat-x" }) {
		t.Fatal("fixture: the early read should still see feat-x")
	}

	m.confirming, m.confirmTargets = true, []cleanupTarget{m.targetFor(m.branchInfo("feat-x"))}
	next, cmd := m.startDelete()
	m = pumpMsgs(next.(Model), cmd)

	next, _ = m.Update(slow)
	m = next.(Model)
	if got := branchRowNames(m); !slices.Equal(got, []string{"main"}) {
		t.Fatalf("rows after the stale read landed = %v, want [main]", got)
	}
}

func TestUnknownPRListGivesNoDeleteProof(t *testing.T) {
	m := seededModel()
	m.local.signals["old-merged"] = gitSignals{mergedLocal: true}
	if m.landedProof("old-merged", false) == "" {
		t.Fatal("fixture: a branch merged into the default branch should have a proof")
	}
	m.remote.PRsFetchError = "rate limited"
	m.remote.PRsFetchedAt = time.Time{}
	if got := m.landedProof("old-merged", false); got != "" {
		t.Fatalf("proof with the PR list never loaded = %q, want none", got)
	}
}
