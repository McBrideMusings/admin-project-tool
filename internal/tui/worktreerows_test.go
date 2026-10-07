package tui

import (
	"context"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// worktreeModel is a repo with a primary checkout on main and four linked
// worktrees: one with an open PR mid-rebase, one whose upstream was deleted and
// whose PR merged at its tip, one detached, and one whose status read failed.
func worktreeModel() Model {
	m := New(context.Background(), "/repo", "owner/repo", "main", "main", WithCheckout("/wt/pr"))
	m.local = localData{
		status: git.Status{Unstaged: 1},
		branches: []git.BranchInfo{
			{Name: "main", HasRemote: true, Behind: 2},
			{Name: "pr", HasRemote: true, Upstream: "pr", Ahead: 1},
			{Name: "landed", UpstreamGone: true},
			{Name: "unread"},
		},
		wts: []git.Worktree{
			{Path: "/repo", Branch: "main"},
			{Path: "/wt/pr", Branch: "pr", Op: git.OpRebase},
			{Path: "/wt/landed", Branch: "landed"},
			{Path: "/wt/detached", Detached: true},
			{Path: "/wt/unread", Branch: "unread"},
		},
		wtState: map[string]wtState{
			"/wt/pr":       {status: git.Status{Staged: 1, Unstaged: 2, Untracked: 3, Conflicted: 1}},
			"/wt/landed":   {},
			"/wt/detached": {},
			"/wt/unread":   {dirty: true, unread: true},
		},
		signals: map[string]gitSignals{
			"pr":     {aheadDefault: 4, behind: 5, defaultRead: true},
			"landed": {behind: 7, defaultRead: true},
		},
		tips: map[string]string{"landed": "tip1"},
	}
	m.haveLocal = true
	m.remote = data.Remote{
		PRs:         []gh.OpenPR{{Number: 12, HeadBranch: "pr", Checks: gh.ChecksFailing}},
		MergedHeads: map[string]string{"landed": "tip1"},
	}
	m.haveData = true
	return m
}

func rowByPath(t *testing.T, rows []WorktreeRow, path string) WorktreeRow {
	t.Helper()
	for _, r := range rows {
		if r.Path == path {
			return r
		}
	}
	t.Fatalf("no row for %s in %+v", path, rows)
	return WorktreeRow{}
}

func TestWorktreeRowsPrimary(t *testing.T) {
	r := rowByPath(t, worktreeModel().worktreeRows(), "/repo")
	if !r.Primary || r.Current {
		t.Errorf("primary=%v current=%v, want true false", r.Primary, r.Current)
	}
	if r.Status == nil || r.Status.Unstaged != 1 {
		t.Errorf("status = %+v, want the main tree's own read (1 unstaged)", r.Status)
	}
	if r.Default != nil {
		t.Errorf("default = %+v, want nil — main is not measured against itself", r.Default)
	}
	if r.Upstream == nil || r.Upstream.Name != "origin/main" || r.Upstream.Behind != 2 {
		t.Errorf("upstream = %+v, want origin/main ↓2", r.Upstream)
	}
}

func TestWorktreeRowsOpenPRMidRebase(t *testing.T) {
	r := rowByPath(t, worktreeModel().worktreeRows(), "/wt/pr")
	if !r.Current {
		t.Error("current = false for the checkout the model was opened in")
	}
	if want := (WorktreeStatus{Staged: 1, Unstaged: 2, Untracked: 3, Conflicted: 1}); r.Status == nil || *r.Status != want {
		t.Errorf("status = %+v, want %+v", r.Status, want)
	}
	if r.Op == nil || *r.Op != git.OpRebase {
		t.Errorf("op = %v, want rebase", r.Op)
	}
	if r.Default == nil || r.Default.Ahead != 4 || r.Default.Behind != 5 {
		t.Errorf("default = %+v, want ↑4 ↓5", r.Default)
	}
	if r.PR == nil || r.PR.Number == nil || *r.PR.Number != 12 || r.PR.State != "open" || *r.PR.Checks != "failing" {
		t.Errorf("pr = %+v, want #12 open failing", r.PR)
	}
}

func TestWorktreeRowsUpstreamGoneMergedPR(t *testing.T) {
	r := rowByPath(t, worktreeModel().worktreeRows(), "/wt/landed")
	if r.Upstream == nil || !r.Upstream.Gone {
		t.Errorf("upstream = %+v, want gone", r.Upstream)
	}
	if r.PR == nil || r.PR.State != "merged" || r.PR.Number != nil {
		t.Errorf("pr = %+v, want merged with no number", r.PR)
	}
}

// A merged PR whose head is not the branch's tip belongs to an earlier branch
// of the same name.
func TestWorktreeRowsMergedPRAtOtherTipIsNotThisBranch(t *testing.T) {
	m := worktreeModel()
	m.local.tips["landed"] = "newer"
	if r := rowByPath(t, m.worktreeRows(), "/wt/landed"); r.PR != nil {
		t.Errorf("pr = %+v, want nil", r.PR)
	}
}

func TestWorktreeRowsDetachedAndUnread(t *testing.T) {
	rows := worktreeModel().worktreeRows()
	d := rowByPath(t, rows, "/wt/detached")
	if !d.Detached || d.Default != nil || d.Upstream != nil || d.PR != nil {
		t.Errorf("detached row = %+v, want no branch groups", d)
	}
	u := rowByPath(t, rows, "/wt/unread")
	if u.Status != nil {
		t.Errorf("unread status = %+v, want nil", u.Status)
	}
	// Its count against main was never read: absent, not 0/0.
	if u.Default != nil {
		t.Errorf("unread default = %+v, want nil", u.Default)
	}
}
