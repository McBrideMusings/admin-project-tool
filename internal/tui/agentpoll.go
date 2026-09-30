// agentpoll.go is the Work tab's per-tick agent status poller
// (admin-spjp.3): internal/agent.Driver.Info (or its InfoContext escape
// hatch, when the resolved Driver implements one — ADR-0021) is called once
// per live agent per tick, purely to tell "blocked on a prompt" apart from
// "finished" and to carry the agent's own activity title into
// livework.Entry.Activity (admin-spjp.4). Nothing polls while an agent is
// mid-run beyond that one query, so the Work tab shows `running` for the
// whole stretch between dispatch and settle, even once the agent has been
// sitting on an approval prompt for minutes.
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
	"errors"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/agent"
	_ "github.com/McBrideMusings/admin-project-tool/internal/agent/herdr" // registers the herdr Driver
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// errNoAgentDriver reports that no internal/agent.Driver is available to
// poll with — the TUI counterpart of internal/kinds' dispatch-time "no
// agent driver available" error. The poller treats it exactly like any
// other failed query: the entry's last known state and activity are left
// untouched (see applyAgentStatuses).
var errNoAgentDriver = errors.New("no agent driver available")

// agentPollTimeout bounds a single herdr query so one wedged agent can never
// stall the whole tick — an overlapping tick is already prevented by
// agentPollInflight, but each query still needs its own ceiling since
// pollAgentStatusCmd waits for every goroutine it starts before returning.
const agentPollTimeout = 2 * time.Second

// agentStatusFn is the test seam for pollAgentStatusCmd's per-agent query,
// bound to queryAgentInfo by default. Swapped like loadRemoteFn/fetchAllFn
// (remoteseam.go) and urlProber.probe.
var agentStatusFn = queryAgentInfo

// infoContexter is implemented by a Driver that can bound its Info query
// with a context — the herdr Driver does. queryAgentInfo type-asserts for
// it rather than widening internal/agent.Driver's 4-method interface with a
// 5th method that only this one caller (a poller that must bound each query
// at agentPollTimeout) needs.
type infoContexter interface {
	InfoContext(ctx context.Context, handle string) (agent.Info, error)
}

// queryAgentInfo resolves the compiled-in agent.Driver (lazily, per call —
// there is no persistent connection to hold onto) and asks it about handle,
// bounding the query with ctx when the resolved Driver supports it.
func queryAgentInfo(ctx context.Context, handle string) (agent.Info, error) {
	drv, ok := agent.Select()
	if !ok {
		return agent.Info{}, errNoAgentDriver
	}
	if ic, ok := drv.(infoContexter); ok {
		return ic.InfoContext(ctx, handle)
	}
	return drv.Info(handle)
}

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
	status   agent.Status
	activity string
	err      error
}

// agentStatusMsg carries a completed poll round back into Update.
type agentStatusMsg struct {
	results []agentStatusResult
}

// agentHandleFor returns the launcher handle to poll for e, or "" when e has
// none. dispatchOne (internal/kinds/workflowdispatch.go) is the only
// producer of a launcher-addressed entry today: it keys the entry's ID on
// the sanitized action-target name but its Handle on the launcher's own opaque address, sets
// Kind to "workflow", and always carries a non-nil Worker alongside a
// non-empty Via ("terminal"). A hosted-pty entry (kinds/hostedpty.go) leaves
// Via empty and Worker nil, so it never matches and is skipped without a
// query or an error.
func agentHandleFor(e *livework.Entry) string {
	if e.Kind == "workflow" && e.Via != "" && e.Worker != nil {
		return e.Handle
	}
	return ""
}

// pollAgentStatusCmd snapshots the live registry, queries the driver once
// for every running-or-waiting entry that has a launcher handle, and
// returns the results as a single tea.Msg. It never blocks the render path:
// bubbletea runs the returned tea.Cmd on its own goroutine, and this
// function itself returns immediately — the actual queries run one
// goroutine each, gathered with a WaitGroup before the outer func returns
// to that bubbletea goroutine.
func pollAgentStatusCmd() tea.Cmd {
	return func() tea.Msg {
		if !agentPollInflight.CompareAndSwap(false, true) {
			return nil
		}
		defer agentPollInflight.Store(false)

		// List returns live *Entry pointers, not copies, so State is read back
		// through the State() accessor rather than e.State directly — the same
		// race livestate_race_test.go guards against for every other reader.
		// Kind/Via/Worker are never mutated after Add, so agentHandleFor reads
		// them off the pointer safely.
		var targets []*livework.Entry
		for _, e := range livework.Default.List() {
			st, ok := livework.Default.State(e.ID)
			if !ok || (st != livework.StateRunning && st != livework.StateWaiting) {
				continue
			}
			if agentHandleFor(e) == "" {
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
			go func(i int, id, handle string) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), agentPollTimeout)
				defer cancel()
				info, err := agentStatusFn(ctx, handle)
				results[i] = agentStatusResult{id: id, status: info.Status, activity: info.Activity, err: err}
			}(i, e.ID, agentHandleFor(e))
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
		case agent.StatusBlocked:
			livework.Default.SetState(r.id, livework.StateWaiting, nil)
		case agent.StatusWorking:
			livework.Default.SetState(r.id, livework.StateRunning, nil)
			// idle, done, unknown, or any other word: leave the row alone —
			// dispatchOne owns the terminal transition (StateExited/StateFailed),
			// and an agent genuinely idle between prompts must not be
			// downgraded mid-run.
		}
	}
}
