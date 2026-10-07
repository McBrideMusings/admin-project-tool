package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/agent"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/tui"
)

// A shared entry another process wrote says running; list must ask the pane
// and the agent instead of believing it. Here worker "alive" is blocked on a
// prompt, worker "closed" lost its pane, and action "stale" lost its pane too.
func TestWorktreeAgentsReconcilesSharedEntries(t *testing.T) {
	root := t.TempDir()
	toml := "project_name = \"t\"\n[terminal]\nspawn = \"true\"\nattach = \"true\"\nkill = \"true\"\nstatus = \"true\"\n"
	if err := os.WriteFile(filepath.Join(root, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(livework.UseRegistry(livework.NewSharedRegistry(t.TempDir())))
	add := func(id, worktree string) {
		e := &livework.Entry{ID: id, Name: id, Kind: "command", Handle: "pane-" + id, Via: "terminal", Root: root}
		if worktree != "" {
			e.Kind = "workflow"
			e.Worker = &livework.WorkerInfo{Branch: id, Worktree: worktree, Target: id}
		}
		if err := livework.Default.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	add("alive", "/wt/alive")
	add("closed", "/wt/closed")
	add("stale", "")

	prevPane, prevAgent := kinds.PaneGoneFn, kinds.AgentInfoFn
	kinds.PaneGoneFn = func(_ context.Context, _ string, handle string) (bool, error) {
		return handle != "pane-alive", nil
	}
	kinds.AgentInfoFn = func(context.Context, string) (agent.Info, error) {
		return agent.Info{Status: agent.StatusBlocked, Activity: "approve edit?"}, nil
	}
	t.Cleanup(func() { kinds.PaneGoneFn, kinds.AgentInfoFn = prevPane, prevAgent })

	agents := worktreeAgents(root)

	if a := agentFor(agents, "/wt/alive"); a == nil || a.State != "waiting" || a.Activity != "approve edit?" {
		t.Errorf("alive = %+v, want waiting · approve edit?", a)
	}
	if a := agentFor(agents, "/wt/closed"); a == nil || a.State != "gone" {
		t.Errorf("closed = %+v, want gone", a)
	}
	if a := agentFor(agents, "/wt/none"); a != nil {
		t.Errorf("agent in an unused worktree = %+v, want nil", a)
	}
	if st, _ := livework.Default.State("stale"); st != livework.StateExited {
		t.Errorf("stale action state = %s, want exited", st)
	}
	if st, _ := livework.Default.State("closed"); st != livework.StateRunning {
		t.Errorf("closed worker state = %s, want running — the reaper ends a worker", st)
	}
}

func TestAgentForPicksNewest(t *testing.T) {
	now := time.Now()
	agents := []placedAgent{
		{worktree: "/wt/a", agent: worktreeAgent{ID: "old", Started: now.Add(-time.Hour)}},
		{worktree: "/wt/a", agent: worktreeAgent{ID: "new", Started: now}},
	}
	if a := agentFor(agents, "/wt/a"); a == nil || a.ID != "new" {
		t.Errorf("agentFor = %+v, want new", a)
	}
}

func TestGitCell(t *testing.T) {
	op := "rebase"
	for _, tc := range []struct {
		row  tui.WorktreeRow
		want string
	}{
		{tui.WorktreeRow{Status: &tui.WorktreeStatus{}}, "clean"},
		{tui.WorktreeRow{Status: &tui.WorktreeStatus{Staged: 1, Untracked: 3, Conflicted: 2}, Op: &op}, "+1 ?3 !2 rebase"},
		{tui.WorktreeRow{}, "unreadable"},
	} {
		if got := gitCell(tc.row); got != tc.want {
			t.Errorf("gitCell(%+v) = %q, want %q", tc.row, got, tc.want)
		}
	}
}
