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
	freshLivework(t)
	plain := &livework.Entry{ID: "plain", Name: "plain", Kind: "command", Handle: "pane-plain", Via: "terminal"}
	if err := livework.Default.Add(plain); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { livework.Default.SetState("plain", livework.StateExited, nil) })
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

// One worker's slow reap does not stop the tick from reaping the others.
func TestReapWorkersCmdSkipsOnlyBusyWorker(t *testing.T) {
	freshLivework(t)
	for _, id := range []string{"slow", "fast"} {
		e := &livework.Entry{
			ID: id, Name: id, Kind: "workflow", Handle: "pane-" + id, Via: "terminal",
			Worker: &livework.WorkerInfo{Branch: "b", Worktree: "/wt"},
		}
		if err := livework.Default.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	reapInflight.Store("slow", true)
	t.Cleanup(func() { reapInflight.Delete("slow") })

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

	if len(reaped) != 1 || reaped[0] != "fast" {
		t.Errorf("reaped = %v, want [fast]", reaped)
	}
}
