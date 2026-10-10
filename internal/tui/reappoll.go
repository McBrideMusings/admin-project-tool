// reappoll.go is the Work tab's per-tick worker reaper: once a tick it hands
// every running or waiting worker to kinds.ReapWorker, which ends the worker
// once its pane has gone, its hosted process has exited or its done predicate
// passes, removes the worktree and branch when nothing in them is missing
// from origin, and frees the worker's ceiling slot. The ended row leaves the
// Workers tab, so each reap's one-line outcome comes back as a reapedMsg for
// the status line.
package tui

import (
	"context"
	"sort"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// reapTimeout bounds one worker's whole reap: predicate, git checks, pane
// close, worktree removal.
const reapTimeout = 30 * time.Second

// reapInflight holds the live ids whose reap is running. An overlapping tick
// skips those workers and still reaps the rest, so one slow reap (up to
// reapTimeout) does not stall every other worker.
var reapInflight sync.Map

// reapWorkerFn is the test seam for the per-worker reap.
var reapWorkerFn = kinds.ReapWorker

// reapedMsg carries the outcome line of every worker one tick ended.
type reapedMsg struct{ outcomes []string }

// reapWorkersCmd reaps every running-or-waiting worker once, returning a
// reapedMsg when any ended.
func reapWorkersCmd(repoRoot string, t config.Terminal) tea.Cmd {
	return func() tea.Msg {
		var workers []*livework.Entry
		for _, e := range livework.Default.List() {
			if e.Worker == nil {
				continue
			}
			if st, ok := livework.Default.State(e.ID); ok && st.Reapable() {
				workers = append(workers, e)
			}
		}
		var (
			wg       sync.WaitGroup
			mu       sync.Mutex
			outcomes []string
		)
		for _, e := range workers {
			if _, busy := reapInflight.LoadOrStore(e.ID, true); busy {
				continue
			}
			wg.Add(1)
			go func(e *livework.Entry) {
				defer wg.Done()
				defer reapInflight.Delete(e.ID)
				ctx, cancel := context.WithTimeout(context.Background(), reapTimeout)
				defer cancel()
				if ended, outcome := reapWorkerFn(ctx, repoRoot, e, t); ended {
					mu.Lock()
					outcomes = append(outcomes, outcome)
					mu.Unlock()
				}
			}(e)
		}
		wg.Wait()
		if len(outcomes) == 0 {
			return nil
		}
		sort.Strings(outcomes)
		return reapedMsg{outcomes: outcomes}
	}
}
