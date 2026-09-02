// agentpoll.go is the Work tab's per-tick herdr agent status poller
// (admin-spjp.3): shim.AgentInfoContext is called once per live agent per
// tick, purely to tell "blocked on a prompt" apart from "finished" and to
// carry the agent's own activity title into livework.Entry.Activity
// (admin-spjp.4). Nothing polls while an agent is mid-run beyond that one
// query, so the Work tab shows `running` for the whole stretch between
// dispatch and settle, even once the agent has been sitting on an approval
// prompt for minutes.
//
// This mirrors urlprobe.go's shape — package-level, off the render path, a
// query per stale/live item per tick — but is message-driven rather than a
// mutex-guarded cache: the tick fires pollAgentStatusCmd, herdr replies with
// agentStatusMsg, and Update's handler is the only thing that calls
// livework.Default.SetState / SetActivity. Keeping the write on the Update
// goroutine (like every other livework mutation model.go makes) means no new
// locking is needed here at all.
package tui

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/shim"
)

// agentPollTimeout bounds a single herdr query so one wedged agent can never
// stall the whole tick — an overlapping tick is already prevented by
// agentPollInflight, but each query still needs its own ceiling since
// pollAgentStatusCmd waits for every goroutine it starts before returning.
const agentPollTimeout = 2 * time.Second

// agentStatusFn is the test seam for pollAgentStatusCmd's herdr query, bound
// to shim.AgentInfoContext by default. Swapped like loadRemoteFn/fetchAllFn
// (remoteseam.go) and urlProber.probe.
var agentStatusFn = shim.AgentInfoContext

// agentPollInflight makes an overlapping tick a no-op: if the previous poll
// hasn't returned by the time the next tick fires (herdr slower than the 1s
// tick, or genuinely wedged past agentPollTimeout), pollAgentStatusCmd skips
// the round entirely rather than piling up a second concurrent batch of
// queries against the same agents. This is what keeps the cost at one query
// per live agent per tick rather than per render.
var agentPollInflight atomic.Bool

// agentStatusResult is one entry's poll outcome.
type agentStatusResult struct {
	id       string
	status   string
	activity string
	err      error
}

// agentStatusMsg carries a completed poll round back into Update.
type agentStatusMsg struct {
	results []agentStatusResult
}

// agentNameFor returns the herdr agent name for e, or "" when e has none.
// dispatchOne (internal/kinds/workflowdispatch.go) is the only producer of a
// herdr-addressed entry today: it keys the entry's ID on agentSessionName,
// sets Kind to "workflow", and always carries a non-nil Worker alongside a
// non-empty PaneID. A hosted-pty entry (kinds/hostedpty.go) sets PTYID and
// leaves Worker nil, so it never matches and is skipped without a query or
// an error.
func agentNameFor(e *livework.Entry) string {
	if e.Kind == "workflow" && e.PaneID != "" && e.Worker != nil {
		return e.ID
	}
	return ""
}

// pollAgentStatusCmd snapshots the live registry, queries herdr once for
// every running-or-waiting entry that has an agent name, and returns the
// results as a single tea.Msg. It never blocks the render path: bubbletea
// runs the returned tea.Cmd on its own goroutine, and this function itself
// returns immediately — the actual queries run one goroutine each, gathered
// with a WaitGroup before the outer func returns to that bubbletea goroutine.
func pollAgentStatusCmd() tea.Cmd {
	return func() tea.Msg {
		if !agentPollInflight.CompareAndSwap(false, true) {
			return nil
		}
		defer agentPollInflight.Store(false)

		// List returns live *Entry pointers, not copies, so State is read back
		// through the State() accessor rather than e.State directly — the same
		// race livestate_race_test.go guards against for every other reader.
		// Kind/PaneID/Worker are never mutated after Add, so agentNameFor reads
		// them off the pointer safely.
		var targets []*livework.Entry
		for _, e := range livework.Default.List() {
			st, ok := livework.Default.State(e.ID)
			if !ok || (st != livework.StateRunning && st != livework.StateWaiting) {
				continue
			}
			if agentNameFor(e) == "" {
				continue
			}
			targets = append(targets, e)
		}
		if len(targets) == 0 {
			return nil
		}

		results := make([]agentStatusResult, len(targets))
		var wg sync.WaitGroup
		wg.Add(len(targets))
		for i, e := range targets {
			go func(i int, id, name string) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), agentPollTimeout)
				defer cancel()
				info, err := agentStatusFn(ctx, name)
				results[i] = agentStatusResult{id: id, status: info.Status, activity: info.Activity, err: err}
			}(i, e.ID, agentNameFor(e))
		}
		wg.Wait()

		return agentStatusMsg{results: results}
	}
}

// applyAgentStatuses writes msg's results into the live registry. A failed
// query leaves the entry's last known state and activity untouched. A result
// is also dropped if the entry has since moved off Running/Waiting
// (dispatchOne already gave it a terminal state) so a slow poll reply can
// never resurrect an entry the real dispatch already settled.
//
// Activity and status are written independently: an empty title (a herdr
// build that omits terminal_title_stripped, or a momentary blank one) leaves
// whatever Activity was last set rather than blanking a good line on a flaky
// reply, and an empty status word is likewise just skipped for the
// state-transition switch below.
func applyAgentStatuses(msg agentStatusMsg) {
	for _, r := range msg.results {
		if r.err != nil {
			continue
		}
		st, ok := livework.Default.State(r.id)
		if !ok || (st != livework.StateRunning && st != livework.StateWaiting) {
			continue
		}
		if r.activity != "" {
			livework.Default.SetActivity(r.id, r.activity)
		}
		switch r.status {
		case "blocked":
			livework.Default.SetState(r.id, livework.StateWaiting, nil)
		case "working":
			livework.Default.SetState(r.id, livework.StateRunning, nil)
			// idle, done, unknown, or any other word: leave the row alone —
			// dispatchOne owns the terminal transition (StateExited/StateFailed),
			// and an agent genuinely idle between prompts must not be
			// downgraded mid-run.
		}
	}
}
