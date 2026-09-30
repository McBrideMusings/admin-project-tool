// panepoll_test.go covers panepoll.go's contract: a pane the status command
// reports gone flips to StateExited, a still-alive pane is left running, a
// failed/timed-out query leaves the previous state untouched, a no-status
// launcher never queries at all, and a stale reply never resurrects an
// entry the real dispatch already settled.
package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// stubPaneStatus swaps paneStatusFn for a fixed response and returns a
// restore func for t.Cleanup, matching agentpoll_test.go's stubAgentStatus.
func stubPaneStatus(t *testing.T, fn func(context.Context, string, string) (bool, error)) *int32Counter {
	t.Helper()
	prev := paneStatusFn
	counter := &int32Counter{}
	paneStatusFn = func(ctx context.Context, statusCmd, handle string) (bool, error) {
		counter.inc()
		return fn(ctx, statusCmd, handle)
	}
	t.Cleanup(func() { paneStatusFn = prev })
	return counter
}

// addPaneEntry registers a terminal-addressed entry (any Kind — panepoll
// polls every Via=="terminal" entry, not just workflow ones) and tears it
// down to StateExited on cleanup, matching addWorkflowEntry's convention.
func addPaneEntry(t *testing.T, id string, kind string, state livework.State) *livework.Entry {
	t.Helper()
	freshLivework(t)
	e := &livework.Entry{
		ID:     id,
		Name:   id,
		Kind:   kind,
		Handle: "pane-" + id,
		Via:    "terminal",
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

func runPanePollRoundTrip(t *testing.T, statusCmd string) {
	t.Helper()
	msg := pollPaneStatusCmd(statusCmd)()
	m, ok := msg.(paneStatusMsg)
	if !ok {
		t.Fatalf("pollPaneStatusCmd() returned %T, want paneStatusMsg (nil means inflight guard, no status cmd, or no targets)", msg)
	}
	applyPaneStatuses(m)
}

func TestPanePollMarksGoneEntryExited(t *testing.T) {
	e := addPaneEntry(t, "panepoll-gone", "dev-server", livework.StateRunning)
	stubPaneStatus(t, func(context.Context, string, string) (bool, error) {
		return true, nil
	})

	runPanePollRoundTrip(t, "herdr pane get \"$ADMIN_HANDLE\"")

	st, _ := livework.Default.State(e.ID)
	if st != livework.StateExited {
		t.Errorf("State = %q, want %q", st, livework.StateExited)
	}
}

func TestPanePollLeavesAliveEntryRunning(t *testing.T) {
	e := addPaneEntry(t, "panepoll-alive", "dev-server", livework.StateRunning)
	stubPaneStatus(t, func(context.Context, string, string) (bool, error) {
		return false, nil
	})

	runPanePollRoundTrip(t, "herdr pane get \"$ADMIN_HANDLE\"")

	st, _ := livework.Default.State(e.ID)
	if st != livework.StateRunning {
		t.Errorf("State = %q, want unchanged %q", st, livework.StateRunning)
	}
}

func TestPanePollFailureLeavesStateUnchanged(t *testing.T) {
	e := addPaneEntry(t, "panepoll-timeout", "dev-server", livework.StateRunning)
	stubPaneStatus(t, func(context.Context, string, string) (bool, error) {
		return false, errors.New("status: timed out")
	})

	runPanePollRoundTrip(t, "herdr pane get \"$ADMIN_HANDLE\"")

	st, _ := livework.Default.State(e.ID)
	if st != livework.StateRunning {
		t.Errorf("State = %q after a failed query, want unchanged %q", st, livework.StateRunning)
	}
}

func TestPanePollSkipsWhenNoStatusCommandDeclared(t *testing.T) {
	e := addPaneEntry(t, "panepoll-no-status", "dev-server", livework.StateRunning)
	counter := stubPaneStatus(t, func(context.Context, string, string) (bool, error) {
		return true, nil
	})

	msg := pollPaneStatusCmd("")()
	if msg != nil {
		t.Fatalf("pollPaneStatusCmd(\"\")() = %v, want nil (no [terminal].status declared)", msg)
	}

	if got := counter.value(); got != 0 {
		t.Errorf("paneStatusFn called %d times with no status command, want 0", got)
	}
	st, _ := livework.Default.State(e.ID)
	if st != livework.StateRunning {
		t.Errorf("State = %q, want unchanged %q (never queried)", st, livework.StateRunning)
	}
}

func TestPanePollSkipsPTYEntry(t *testing.T) {
	freshLivework(t)
	id := "panepoll-pty"
	e := &livework.Entry{
		ID: id, Name: id, Kind: "hosted-pty", Handle: "pty-" + id,
		State: livework.StateRunning,
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add(%s): %v", id, err)
	}
	t.Cleanup(func() {
		livework.Default.SetState(id, livework.StateExited, nil)
	})

	counter := stubPaneStatus(t, func(context.Context, string, string) (bool, error) {
		return true, nil
	})

	msg := pollPaneStatusCmd("herdr pane get \"$ADMIN_HANDLE\"")()
	if m, ok := msg.(paneStatusMsg); ok {
		applyPaneStatuses(m)
	}

	if got := counter.value(); got != 0 {
		t.Errorf("paneStatusFn called %d times for a pty-hosted entry (Via unset), want 0", got)
	}
	st, _ := livework.Default.State(id)
	if st != livework.StateRunning {
		t.Errorf("State = %q, want unchanged %q (never queried, never errored)", st, livework.StateRunning)
	}
}

func TestPanePollDoesNotResurrectExitedEntry(t *testing.T) {
	id := "panepoll-exited"
	e := addPaneEntry(t, id, "dev-server", livework.StateRunning)
	// Simulate the real producer settling the entry between the poll's
	// snapshot and its reply landing, same as agentpoll_test.go's analogous
	// case: build the msg by hand rather than race real goroutine timing.
	livework.Default.SetState(id, livework.StateExited, nil)

	applyPaneStatuses(paneStatusMsg{results: []paneStatusResult{
		{id: e.ID, gone: true, err: nil},
	}})

	st, _ := livework.Default.State(id)
	if st != livework.StateExited {
		t.Errorf("State = %q, want %q (a stale poll reply must not resurrect a settled entry)", st, livework.StateExited)
	}
}
