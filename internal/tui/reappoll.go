// reappoll.go is the Work tab's per-tick worker reaper: once a tick it hands
// every live window worker to kinds.ReapWorker, which decides from the
// worker's done predicate (or its vanished pane) and a clean/merged check
// whether to close the pane, delete the worktree and branch, and free the
// worker's ceiling slot. The effects all go through the live registry, so the
// next repaint shows them and the poller returns no message.
package tui

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// reapTimeout bounds one worker's whole reap: predicate, git checks, pane
// close, worktree removal.
const reapTimeout = 30 * time.Second

// reapInflight makes an overlapping tick a no-op, like panePollInflight.
var reapInflight atomic.Bool

// reapWorkerFn is the test seam for the per-worker reap.
var reapWorkerFn = kinds.ReapWorker

// reapWorkersCmd reaps every running-or-waiting window worker once.
func reapWorkersCmd(repoRoot string, t config.Terminal) tea.Cmd {
	return func() tea.Msg {
		if !reapInflight.CompareAndSwap(false, true) {
			return nil
		}
		defer reapInflight.Store(false)

		var workers []*livework.Entry
		for _, e := range livework.Default.List() {
			if e.Worker == nil || e.Via != "terminal" {
				continue
			}
			if st, ok := livework.Default.State(e.ID); ok && (st == livework.StateRunning || st == livework.StateWaiting) {
				workers = append(workers, e)
			}
		}
		var wg sync.WaitGroup
		for _, e := range workers {
			wg.Add(1)
			go func(e *livework.Entry) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), reapTimeout)
				defer cancel()
				reapWorkerFn(ctx, repoRoot, e, t)
			}(e)
		}
		wg.Wait()
		return nil
	}
}
