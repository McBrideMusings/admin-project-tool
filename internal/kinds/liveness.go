// liveness.go is the one place a live launcher entry is asked whether it is
// still there and what it is doing. A launcher pane outlives the admin process
// that opened it, and nothing tells that process when someone closes the pane
// by hand or an agent stops on a prompt, so every reader that wants an honest
// answer has to ask: the dashboard's Work tick, a shell dispatch deciding
// whether a target already has a worker, and `admin worktree list`.
//
// Two questions, asked together per entry: does the handle still exist
// ([terminal].status, PaneGone), and what is the agent in it doing
// (internal/agent.Driver.Info: blocked on a prompt, or working, plus its
// activity line). ProbeLiveness only asks; ApplyLiveness writes the
// transitions every reader agrees on. A worker whose pane is gone is not one
// of them — the reaper decides when a worker is over, because its work may
// still be on disk — so each reader handles that case itself.
package kinds

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/agent"
	_ "github.com/McBrideMusings/admin-project-tool/internal/agent/herdr" // registers the herdr Driver
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// LivenessTimeout bounds one status or agent query, so one wedged launcher
// cannot stall the round.
const LivenessTimeout = 2 * time.Second

// PaneGoneFn and AgentInfoFn are the queries ProbeLiveness makes, swapped by
// tests.
var (
	PaneGoneFn  = PaneGone
	AgentInfoFn = queryAgentInfo
)

// errNoAgentDriver reports that no agent.Driver is available to ask. The
// probe treats it like any failed query: the entry is left as it was.
var errNoAgentDriver = errors.New("no agent driver available")

// queryAgentInfo asks the compiled-in agent.Driver about handle.
func queryAgentInfo(ctx context.Context, handle string) (agent.Info, error) {
	drv, ok := agent.Select()
	if !ok {
		return agent.Info{}, errNoAgentDriver
	}
	return drv.Info(ctx, handle)
}

// Liveness is what one probe round learned about one entry. A question not
// asked, or asked and not answered, leaves its Asked false or its Err set;
// either says nothing about the entry.
type Liveness struct {
	ID     string
	Handle string
	Worker bool

	PaneAsked bool
	PaneGone  bool
	PaneErr   error

	AgentAsked bool
	Agent      agent.Info
	AgentErr   error
}

// Gone reports whether the status command answered that the pane is gone.
func (l Liveness) Gone() bool { return l.PaneAsked && l.PaneErr == nil && l.PaneGone }

// AgentHandle is the handle to ask the agent driver about for e, or "" when
// e holds no agent: only a launcher-dispatched workflow worker does. A hosted
// pty (Via empty) never matches.
func AgentHandle(e *livework.Entry) string {
	if e.Kind == "workflow" && e.Via != "" && e.Worker != nil {
		return e.Handle
	}
	return ""
}

// Questions selects what ProbeLiveness asks.
type Questions int

const (
	// PaneAndAgent asks both questions: what a reader showing state needs.
	PaneAndAgent Questions = iota
	// PaneOnly asks only whether the pane exists, for a caller that acts on
	// nothing else.
	PaneOnly
)

// ProbeLiveness asks about every entry in entries at once and returns one
// Liveness per entry it asked anything about, in entries' order. The pane is
// asked about when statusCmd is declared and the entry went through the
// launcher; the agent, unless q is PaneOnly, when AgentHandle names one. It
// writes nothing.
func ProbeLiveness(entries []*livework.Entry, statusCmd string, q Questions) []Liveness {
	var out []Liveness
	for _, e := range entries {
		l := Liveness{ID: e.ID, Handle: e.Handle, Worker: e.Worker != nil}
		l.PaneAsked = statusCmd != "" && e.Via == "terminal"
		l.AgentAsked = q == PaneAndAgent && AgentHandle(e) != ""
		if l.PaneAsked || l.AgentAsked {
			out = append(out, l)
		}
	}
	var wg sync.WaitGroup
	for i := range out {
		l := &out[i]
		handle := l.Handle
		if l.PaneAsked {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), LivenessTimeout)
				defer cancel()
				l.PaneGone, l.PaneErr = PaneGoneFn(ctx, statusCmd, handle)
			}()
		}
		if l.AgentAsked {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), LivenessTimeout)
				defer cancel()
				l.Agent, l.AgentErr = AgentInfoFn(ctx, handle)
			}()
		}
	}
	wg.Wait()
	return out
}

// ApplyLiveness writes what results learned into livework.Default. Only an
// entry still running or waiting is touched, so a slow reply never revives one
// that settled meanwhile. A non-worker whose pane is gone exits, the same
// state its own kill records; a worker whose pane is gone is left as it was.
// Otherwise an agent blocked on a prompt reads waiting, one
// working reads running, and a non-empty activity line replaces the last one;
// any other agent word, and any failed query, leaves the entry as it was.
func ApplyLiveness(results []Liveness) {
	for _, l := range results {
		st, ok := livework.Default.State(l.ID)
		if !ok || (st != livework.StateRunning && st != livework.StateWaiting) {
			continue
		}
		if l.Gone() {
			// Whatever the agent driver said, no agent runs in a closed pane.
			if !l.Worker {
				livework.Default.SetState(l.ID, livework.StateExited, nil)
			}
			continue
		}
		if !l.AgentAsked || l.AgentErr != nil {
			continue
		}
		if l.Agent.Activity != "" {
			livework.Default.SetActivity(l.ID, l.Agent.Activity)
		}
		switch l.Agent.Status {
		case agent.StatusBlocked:
			livework.Default.SetState(l.ID, livework.StateWaiting, nil)
		case agent.StatusWorking:
			livework.Default.SetState(l.ID, livework.StateRunning, nil)
		}
	}
}

// LiveEntries is every entry in livework.Default currently running or
// waiting — the set a probe round asks about.
func LiveEntries() []*livework.Entry {
	var out []*livework.Entry
	for _, e := range livework.Default.List() {
		if st, ok := livework.Default.State(e.ID); ok && (st == livework.StateRunning || st == livework.StateWaiting) {
			out = append(out, e)
		}
	}
	return out
}
