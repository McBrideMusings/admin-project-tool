// agentpoll_test.go covers admin-spjp.3's acceptance criteria against the
// agentStatusFn seam: a blocked agent flips to StateWaiting, a resumed one
// returns to StateRunning, a failed/timed-out query leaves the previous
// state untouched, a pty-hosted entry is never queried, and an entry the
// real dispatch already settled is never resurrected by a stale poll reply.
package tui

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// stubAgentStatus swaps agentStatusFn for a fixed response (or a caller-
// supplied func) and returns a restore func for t.Cleanup, matching
// remoteseam_test.go's save/restore convention.
func stubAgentStatus(t *testing.T, fn func(context.Context, string) (string, error)) *int32Counter {
	t.Helper()
	prev := agentStatusFn
	counter := &int32Counter{}
	agentStatusFn = func(ctx context.Context, name string) (string, error) {
		counter.inc()
		return fn(ctx, name)
	}
	t.Cleanup(func() { agentStatusFn = prev })
	return counter
}

type int32Counter struct {
	mu sync.Mutex
	n  int
}

func (c *int32Counter) inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *int32Counter) value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// addWorkflowEntry registers a pane-addressed workflow entry (the only shape
// agentNameFor recognizes) and arranges for it to be torn down to
// StateExited on cleanup — livework.Default is a process-wide singleton with
// no Remove, so a running entry left behind pollutes later tests in this
// package (livestate_race_test.go's own comment explains why).
func addWorkflowEntry(t *testing.T, id string, state livework.State) *livework.Entry {
	t.Helper()
	e := &livework.Entry{
		ID:     id,
		Name:   id,
		Kind:   "workflow",
		PaneID: "pane-" + id,
		Worker: &livework.WorkerInfo{Branch: id, Worktree: "/tmp/" + id},
		State:  state,
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add(%s): %v", id, err)
	}
	livework.Default.SetState(id, state, nil)
	t.Cleanup(func() {
		livework.Default.SetState(id, livework.StateExited, nil)
	})
	return e
}

func runPollRoundTrip(t *testing.T) {
	t.Helper()
	msg := pollAgentStatusCmd()()
	m, ok := msg.(agentStatusMsg)
	if !ok {
		t.Fatalf("pollAgentStatusCmd() returned %T, want agentStatusMsg (nil means inflight guard or no targets)", msg)
	}
	applyAgentStatuses(m)
}

func TestAgentPollMarksBlockedEntryWaiting(t *testing.T) {
	e := addWorkflowEntry(t, "agentpoll-blocked", livework.StateRunning)
	stubAgentStatus(t, func(context.Context, string) (string, error) { return "blocked", nil })

	runPollRoundTrip(t)

	st, _ := livework.Default.State(e.ID)
	if st != livework.StateWaiting {
		t.Errorf("State = %q, want %q", st, livework.StateWaiting)
	}
}

func TestAgentPollReturnsResumedEntryToRunning(t *testing.T) {
	e := addWorkflowEntry(t, "agentpoll-resumed", livework.StateWaiting)
	stubAgentStatus(t, func(context.Context, string) (string, error) { return "working", nil })

	runPollRoundTrip(t)

	st, _ := livework.Default.State(e.ID)
	if st != livework.StateRunning {
		t.Errorf("State = %q, want %q", st, livework.StateRunning)
	}
}

func TestAgentPollFailureLeavesStateUnchanged(t *testing.T) {
	e := addWorkflowEntry(t, "agentpoll-timeout", livework.StateRunning)
	stubAgentStatus(t, func(context.Context, string) (string, error) {
		return "", errors.New("herdr: timed out")
	})

	runPollRoundTrip(t)

	st, _ := livework.Default.State(e.ID)
	if st != livework.StateRunning {
		t.Errorf("State = %q after a failed query, want unchanged %q", st, livework.StateRunning)
	}
}

func TestAgentPollSkipsPTYEntry(t *testing.T) {
	id := "agentpoll-pty"
	e := &livework.Entry{
		ID: id, Name: id, Kind: "hosted-pty", PTYID: "pty-" + id,
		State: livework.StateRunning,
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add(%s): %v", id, err)
	}
	t.Cleanup(func() {
		livework.Default.SetState(id, livework.StateExited, nil)
	})

	counter := stubAgentStatus(t, func(context.Context, string) (string, error) {
		return "blocked", nil
	})

	msg := pollAgentStatusCmd()()
	if m, ok := msg.(agentStatusMsg); ok {
		applyAgentStatuses(m)
	}

	if got := counter.value(); got != 0 {
		t.Errorf("agentStatusFn called %d times for a pty-hosted entry, want 0", got)
	}
	st, _ := livework.Default.State(id)
	if st != livework.StateRunning {
		t.Errorf("State = %q, want unchanged %q (never queried, never errored)", st, livework.StateRunning)
	}
}

func TestAgentPollDoesNotResurrectExitedEntry(t *testing.T) {
	id := "agentpoll-exited"
	e := addWorkflowEntry(t, id, livework.StateRunning)
	// Simulate dispatchOne settling the entry to a terminal state between the
	// poll's snapshot and its reply landing: build the msg by hand rather than
	// through pollAgentStatusCmd, since the real race window is a single tick
	// wide and not worth reproducing with real goroutine timing.
	livework.Default.SetState(id, livework.StateExited, nil)

	applyAgentStatuses(agentStatusMsg{results: []agentStatusResult{
		{id: e.ID, status: "blocked", err: nil},
	}})

	st, _ := livework.Default.State(id)
	if st != livework.StateExited {
		t.Errorf("State = %q, want %q (a stale poll reply must not resurrect a settled entry)", st, livework.StateExited)
	}
}
