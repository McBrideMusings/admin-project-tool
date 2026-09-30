package tui

import (
	"context"
	"sync"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// The reap tick hands every running window worker to the reaper and nothing
// else: not a process entry, not a worker that has already exited.
func TestReapWorkersCmdReapsOnlyRunningWorkers(t *testing.T) {
	addPaneEntry(t, "plain", "command", livework.StateRunning)
	freshWorker := &livework.Entry{
		ID: "gone", Name: "gone", Kind: "workflow", Handle: "pane-gone", Via: "terminal",
		Worker: &livework.WorkerInfo{Branch: "b", Worktree: "/wt"},
	}
	if err := livework.Default.Add(freshWorker); err != nil {
		t.Fatal(err)
	}
	livework.Default.SetState("gone", livework.StateExited, nil)
	live := &livework.Entry{
		ID: "live", Name: "live", Kind: "workflow", Handle: "pane-live", Via: "terminal",
		Worker: &livework.WorkerInfo{Branch: "b", Worktree: "/wt"},
	}
	if err := livework.Default.Add(live); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { livework.Default.SetState("live", livework.StateExited, nil) })

	var mu sync.Mutex
	var reaped []string
	prev := reapWorkerFn
	reapWorkerFn = func(_ context.Context, _ string, e *livework.Entry, _ config.Terminal) bool {
		mu.Lock()
		defer mu.Unlock()
		reaped = append(reaped, e.ID)
		return false
	}
	t.Cleanup(func() { reapWorkerFn = prev })

	reapWorkersCmd("/repo", config.Terminal{})()

	if len(reaped) != 1 || reaped[0] != "live" {
		t.Errorf("reaped = %v, want [live]", reaped)
	}
}

// A worker whose pane has vanished is left for the reaper: the pane poller
// must not flip it to exited, which would hide its worktree from the reap.
func TestPaneStatusLeavesWorkersToTheReaper(t *testing.T) {
	freshLivework(t)
	w := &livework.Entry{
		ID: "w", Name: "w", Kind: "workflow", Handle: "pane-w", Via: "terminal",
		Worker: &livework.WorkerInfo{Branch: "b", Worktree: "/wt"},
	}
	if err := livework.Default.Add(w); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { livework.Default.SetState("w", livework.StateExited, nil) })
	counter := stubPaneStatus(t, func(context.Context, string, string) (bool, error) { return true, nil })

	if msg := pollPaneStatusCmd("status")(); msg != nil {
		applyPaneStatuses(msg.(paneStatusMsg))
	}

	if st, _ := livework.Default.State("w"); st != livework.StateRunning {
		t.Errorf("worker state = %s, want running", st)
	}
	if got := counter.value(); got != 0 {
		t.Errorf("pane status queried %d times for a worker, want 0", got)
	}
}
