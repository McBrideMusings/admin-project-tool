package kinds

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/agent"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// stubLiveness swaps both queries for the test and counts the calls.
func stubLiveness(t *testing.T, gone func() (bool, error), info func() (agent.Info, error)) (paneCalls, agentCalls *atomic.Int32) {
	t.Helper()
	paneCalls, agentCalls = &atomic.Int32{}, &atomic.Int32{}
	prevPane, prevAgent := PaneGoneFn, AgentInfoFn
	PaneGoneFn = func(context.Context, string, string) (bool, error) {
		paneCalls.Add(1)
		return gone()
	}
	AgentInfoFn = func(context.Context, string) (agent.Info, error) {
		agentCalls.Add(1)
		return info()
	}
	t.Cleanup(func() { PaneGoneFn, AgentInfoFn = prevPane, prevAgent })
	return paneCalls, agentCalls
}

func paneAlive() (bool, error)          { return false, nil }
func agentWorking() (agent.Info, error) { return agent.Info{Status: agent.StatusWorking}, nil }
func agentIs(s agent.Status) func() (agent.Info, error) {
	return func() (agent.Info, error) { return agent.Info{Status: s}, nil }
}

// addLive registers one entry in a fresh registry: a launcher worker when
// worker is true, a plain launcher action otherwise, or a hosted pty when via
// is "".
func addLive(t *testing.T, id, via string, worker bool, state livework.State) *livework.Entry {
	t.Helper()
	e := &livework.Entry{ID: id, Name: id, Kind: "command", Handle: "pane-" + id, Via: via}
	if worker {
		e.Kind = "workflow"
		e.Worker = &livework.WorkerInfo{Branch: id, Worktree: "/wt/" + id, Target: id}
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatal(err)
	}
	livework.Default.SetState(id, state, nil)
	return e
}

func roundTrip(statusCmd string) {
	ApplyLiveness(ProbeLiveness(LiveEntries(), statusCmd, PaneAndAgent))
}

func stateOf(t *testing.T, id string) livework.State {
	t.Helper()
	st, _ := livework.Default.State(id)
	return st
}

func TestLivenessAgentBlockedReadsWaiting(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	addLive(t, "w", "terminal", true, livework.StateRunning)
	stubLiveness(t, paneAlive, agentIs(agent.StatusBlocked))
	roundTrip("status")
	if got := stateOf(t, "w"); got != livework.StateWaiting {
		t.Errorf("state = %s, want waiting", got)
	}
}

func TestLivenessAgentWorkingReadsRunning(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	addLive(t, "w", "terminal", true, livework.StateWaiting)
	stubLiveness(t, paneAlive, agentWorking)
	roundTrip("status")
	if got := stateOf(t, "w"); got != livework.StateRunning {
		t.Errorf("state = %s, want running", got)
	}
}

func TestLivenessFailedQueriesLeaveEntry(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	addLive(t, "w", "terminal", true, livework.StateRunning)
	addLive(t, "p", "terminal", false, livework.StateRunning)
	stubLiveness(t,
		func() (bool, error) { return true, errors.New("status: timed out") },
		func() (agent.Info, error) {
			return agent.Info{Status: agent.StatusBlocked}, errors.New("herdr: timed out")
		})
	roundTrip("status")
	for _, id := range []string{"w", "p"} {
		if got := stateOf(t, id); got != livework.StateRunning {
			t.Errorf("%s state = %s after failed queries, want running", id, got)
		}
	}
}

func TestLivenessNeverAsksAboutHostedPty(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	addLive(t, "pty", "", false, livework.StateRunning)
	pane, ag := stubLiveness(t, func() (bool, error) { return true, nil }, agentIs(agent.StatusBlocked))
	roundTrip("status")
	if pane.Load() != 0 || ag.Load() != 0 {
		t.Errorf("queries for a hosted pty: pane %d, agent %d; want 0, 0", pane.Load(), ag.Load())
	}
	if got := stateOf(t, "pty"); got != livework.StateRunning {
		t.Errorf("state = %s, want running", got)
	}
}

func TestLivenessNoStatusCommandAsksNoPane(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	addLive(t, "p", "terminal", false, livework.StateRunning)
	pane, _ := stubLiveness(t, func() (bool, error) { return true, nil }, agentWorking)
	roundTrip("")
	if pane.Load() != 0 {
		t.Errorf("pane queried %d times with no [terminal].status, want 0", pane.Load())
	}
	if got := stateOf(t, "p"); got != livework.StateRunning {
		t.Errorf("state = %s, want running", got)
	}
}

func TestLivenessGonePaneEndsActionNotWorker(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	addLive(t, "p", "terminal", false, livework.StateRunning)
	addLive(t, "w", "terminal", true, livework.StateRunning)
	// The driver's answer about a closed pane is not believed either.
	stubLiveness(t, func() (bool, error) { return true, nil }, agentIs(agent.StatusBlocked))
	results := ProbeLiveness(LiveEntries(), "status", PaneAndAgent)
	ApplyLiveness(results)
	if got := stateOf(t, "p"); got != livework.StateExited {
		t.Errorf("action state = %s, want exited", got)
	}
	// The reaper decides when a worker is over; the probe still reports it.
	if got := stateOf(t, "w"); got != livework.StateRunning {
		t.Errorf("worker state = %s, want running", got)
	}
	var sawWorkerGone bool
	for _, l := range results {
		sawWorkerGone = sawWorkerGone || (l.ID == "w" && l.Gone())
	}
	if !sawWorkerGone {
		t.Error("probe did not report the worker's pane gone")
	}
}

func TestLivenessAlivePaneStaysRunning(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	addLive(t, "p", "terminal", false, livework.StateRunning)
	stubLiveness(t, paneAlive, agentWorking)
	roundTrip("status")
	if got := stateOf(t, "p"); got != livework.StateRunning {
		t.Errorf("state = %s, want running", got)
	}
}

// A reply for an entry that settled after the probe began must not revive it.
func TestLivenessDoesNotReviveSettledEntry(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	addLive(t, "w", "terminal", true, livework.StateRunning)
	livework.Default.SetState("w", livework.StateExited, nil)
	ApplyLiveness([]Liveness{{ID: "w", Worker: true, AgentAsked: true, Agent: agent.Info{Status: agent.StatusBlocked}}})
	if got := stateOf(t, "w"); got != livework.StateExited {
		t.Errorf("state = %s, want exited", got)
	}
}

func TestLivenessActivityWrittenAndNotBlanked(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	addLive(t, "w", "terminal", true, livework.StateRunning)
	stubLiveness(t, paneAlive, func() (agent.Info, error) {
		return agent.Info{Status: agent.StatusWorking, Activity: "Chess puzzles"}, nil
	})
	roundTrip("status")
	stubLiveness(t, paneAlive, agentWorking)
	roundTrip("status")
	if got, _ := livework.Default.Activity("w"); got != "Chess puzzles" {
		t.Errorf("activity = %q, want %q kept over an empty reply", got, "Chess puzzles")
	}
}

// Each query gets its own deadline, so one wedged launcher cannot hold the
// round.
func TestLivenessQueriesCarryDeadline(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	addLive(t, "w", "terminal", true, livework.StateRunning)
	var remaining atomic.Int64
	prev := AgentInfoFn
	AgentInfoFn = func(ctx context.Context, _ string) (agent.Info, error) {
		if dl, ok := ctx.Deadline(); ok {
			remaining.Store(int64(time.Until(dl)))
		}
		return agent.Info{}, nil
	}
	t.Cleanup(func() { AgentInfoFn = prev })
	ProbeLiveness(LiveEntries(), "", PaneAndAgent)
	if r := time.Duration(remaining.Load()); r <= 0 || r > LivenessTimeout {
		t.Errorf("deadline in %v, want within (0, %v]", r, LivenessTimeout)
	}
}
