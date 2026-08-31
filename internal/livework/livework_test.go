package livework

import (
	"errors"
	"testing"
)

// TestAddDefaultsStateAndTimestamp proves an Entry added with zero-value
// State/StartedAt gets StateRunning and a non-zero StartedAt — the
// "long-running kind ... appears in the live registry" acceptance criterion
// doesn't require the caller to fill in bookkeeping it can compute itself.
func TestAddDefaultsStateAndTimestamp(t *testing.T) {
	r := NewRegistry()
	r.Add(&Entry{ID: "w1:p1", Name: "dev-server", Kind: "vite-dev", PaneID: "w1:p1"})

	e, ok := r.Get("w1:p1")
	if !ok {
		t.Fatal("Get(w1:p1) = false, want true")
	}
	if e.State != StateRunning {
		t.Errorf("State = %q, want %q", e.State, StateRunning)
	}
	if e.StartedAt.IsZero() {
		t.Error("StartedAt should default to now, not the zero time")
	}
}

// TestListSurvivesForLifeOfRegistry proves the acceptance criterion "the
// registry survives for the life of the process and reports each entry's
// state": entries persist across multiple List calls and their state is
// independently reportable, through more than one transition.
//
// MUTATION EVIDENCE: add a guard to SetState that skips entries not already
// StateRunning (`if e.State != StateRunning { return }`), and this test
// fails — entry a's second transition (Exited -> Failed) becomes a no-op,
// so a.State stays "exited" instead of "failed".
func TestListSurvivesForLifeOfRegistry(t *testing.T) {
	r := NewRegistry()
	r.Add(&Entry{ID: "a", Name: "one", Kind: "vite-dev"})
	r.Add(&Entry{ID: "b", Name: "two", Kind: "docker-dev"})

	if got := len(r.List()); got != 2 {
		t.Fatalf("List() has %d entries, want 2", got)
	}

	r.SetState("a", StateExited, nil)
	r.SetState("a", StateFailed, errors.New("crashed after exiting"))
	r.SetState("b", StateFailed, errors.New("boom"))

	entries := r.List()
	if len(entries) != 2 {
		t.Fatalf("List() has %d entries after SetState, want 2 (state changes, not removal)", len(entries))
	}
	a, _ := r.Get("a")
	if a.State != StateFailed {
		t.Errorf("entry a State = %q, want %q (its second transition)", a.State, StateFailed)
	}
	b, _ := r.Get("b")
	if b.State != StateFailed {
		t.Errorf("entry b State = %q, want %q", b.State, StateFailed)
	}
	if b.Err == nil || b.Err.Error() != "boom" {
		t.Errorf("entry b Err = %v, want the recorded failure", b.Err)
	}
}

// TestSetStateOnlyRecordsErrForFailed proves Err is left nil for a
// non-failed transition even if the caller passed one — StateExited never
// carries an Err.
func TestSetStateOnlyRecordsErrForFailed(t *testing.T) {
	r := NewRegistry()
	r.Add(&Entry{ID: "a", Name: "one"})
	r.SetState("a", StateExited, errors.New("ignored"))
	a, _ := r.Get("a")
	if a.Err != nil {
		t.Errorf("Err = %v, want nil for a StateExited transition", a.Err)
	}
}

// TestListOrderIsInsertionOrder proves List reports entries in the order
// they were Added, a basic determinism guarantee a future Work-tab reader
// depends on.
func TestListOrderIsInsertionOrder(t *testing.T) {
	r := NewRegistry()
	r.Add(&Entry{ID: "z", Name: "first"})
	r.Add(&Entry{ID: "a", Name: "second"})
	entries := r.List()
	if len(entries) != 2 || entries[0].Name != "first" || entries[1].Name != "second" {
		t.Errorf("List() = %+v, want [first, second] in insertion order", entries)
	}
}

// TestWorkerVsProcessEntry proves the two producer kinds are modeled
// honestly: an agent worker's Worker field carries its branch and worktree,
// a long-running process's is nil — never an empty-string stand-in for
// "not applicable".
func TestWorkerVsProcessEntry(t *testing.T) {
	r := NewRegistry()
	r.Add(&Entry{ID: "proc", Name: "dev-server", Kind: "vite-dev"})
	r.Add(&Entry{ID: "worker", Name: "review", Kind: "workflow",
		Worker: &WorkerInfo{Branch: "review-42", Worktree: "/tmp/wt/review-42"}})

	proc, _ := r.Get("proc")
	if proc.Worker != nil {
		t.Errorf("process entry Worker = %+v, want nil", proc.Worker)
	}
	worker, _ := r.Get("worker")
	if worker.Worker == nil {
		t.Fatal("worker entry Worker = nil, want a WorkerInfo")
	}
	if worker.Worker.Branch != "review-42" || worker.Worker.Worktree != "/tmp/wt/review-42" {
		t.Errorf("worker.Worker = %+v, want branch/worktree recorded", worker.Worker)
	}
}

// TestSetActivity proves the current-activity line is independently
// reportable, matching SetState's existing pattern.
func TestSetActivity(t *testing.T) {
	r := NewRegistry()
	r.Add(&Entry{ID: "a", Name: "one"})
	r.SetActivity("a", "writing internal/lua/host.go")
	e, _ := r.Get("a")
	if e.Activity != "writing internal/lua/host.go" {
		t.Errorf("Activity = %q, want the set line", e.Activity)
	}
	// A missing ID is silently ignored, like SetState.
	r.SetActivity("missing", "should not panic")
}

// TestStateWaitingIsDistinctFromRunning proves "waiting on input" is a
// third state a reader can see just from List/Get, without attaching to
// anything — the acceptance criterion's exact wording.
func TestStateWaitingIsDistinctFromRunning(t *testing.T) {
	r := NewRegistry()
	r.Add(&Entry{ID: "a", Name: "one"})
	r.SetState("a", StateWaiting, nil)
	e, _ := r.Get("a")
	if e.State != StateWaiting {
		t.Errorf("State = %q, want %q", e.State, StateWaiting)
	}
	if e.State == StateRunning {
		t.Error("StateWaiting must not equal StateRunning")
	}
}

// TestAttachInvokesOps proves Attach calls the entry's own Ops.Attach and
// surfaces its error.
//
// MUTATION EVIDENCE: change Attach to `return nil` unconditionally instead
// of calling e.Ops.Attach() — this test fails with "Attach() = <nil>, want
// an error" because the stubbed Attach (which always errors) is never
// invoked. Reverted after confirming.
func TestAttachInvokesOps(t *testing.T) {
	r := NewRegistry()
	called := false
	r.Add(&Entry{ID: "a", Name: "one", Ops: Ops{
		Attach: func() error { called = true; return errors.New("focus failed") },
	}})
	err := r.Attach("a")
	if err == nil || err.Error() != "focus failed" {
		t.Errorf("Attach() = %v, want the wired error", err)
	}
	if !called {
		t.Error("Attach did not invoke the entry's Ops.Attach")
	}
}

// TestAttachUnsupportedErrors proves Attach reports a clear error rather
// than panicking when the producer wired no Attach operation for this
// entry — e.g. a kind with no pane at all.
func TestAttachUnsupportedErrors(t *testing.T) {
	r := NewRegistry()
	r.Add(&Entry{ID: "a", Name: "one"})
	if err := r.Attach("a"); err == nil {
		t.Error("Attach() = nil, want an error for an entry with no Attach op")
	}
}

// TestAttachUnknownIDErrors proves Attach/Kill/Retry all report a clear
// error for an ID the registry has never seen, rather than panicking.
func TestAttachUnknownIDErrors(t *testing.T) {
	r := NewRegistry()
	if err := r.Attach("nope"); err == nil {
		t.Error("Attach(nope) = nil, want an error")
	}
	if err := r.Kill("nope"); err == nil {
		t.Error("Kill(nope) = nil, want an error")
	}
	if err := r.Retry("nope"); err == nil {
		t.Error("Retry(nope) = nil, want an error")
	}
}

// TestKillInvokesOps proves Kill calls the entry's own Ops.Kill.
func TestKillInvokesOps(t *testing.T) {
	r := NewRegistry()
	called := false
	r.Add(&Entry{ID: "a", Name: "one", Ops: Ops{
		Kill: func() error { called = true; return nil },
	}})
	if err := r.Kill("a"); err != nil {
		t.Errorf("Kill() = %v, want nil", err)
	}
	if !called {
		t.Error("Kill did not invoke the entry's Ops.Kill")
	}
}

// TestRetryRequiresFailedState proves Retry refuses to restart an entry
// that isn't StateFailed — retrying a still-running or cleanly-exited entry
// is not what "restart a failed entry" means.
//
// MUTATION EVIDENCE: remove the `e.State != StateFailed` guard from Retry —
// this test fails with "Retry() = <nil>, want an error for a running
// entry" because the wired Retry op (which would panic/mutate state
// wrongly if invoked) runs anyway. Reverted after confirming.
func TestRetryRequiresFailedState(t *testing.T) {
	r := NewRegistry()
	called := false
	r.Add(&Entry{ID: "a", Name: "one", State: StateRunning, Ops: Ops{
		Retry: func() error { called = true; return nil },
	}})
	if err := r.Retry("a"); err == nil {
		t.Error("Retry() = nil, want an error for a running entry")
	}
	if called {
		t.Error("Retry invoked Ops.Retry on a non-failed entry")
	}
}

// TestRetrySucceedsOnFailedEntry proves Retry invokes Ops.Retry once an
// entry has actually failed.
func TestRetrySucceedsOnFailedEntry(t *testing.T) {
	r := NewRegistry()
	called := false
	r.Add(&Entry{ID: "a", Name: "one", Ops: Ops{
		Retry: func() error { called = true; return nil },
	}})
	r.SetState("a", StateFailed, errors.New("boom"))
	if err := r.Retry("a"); err != nil {
		t.Errorf("Retry() = %v, want nil", err)
	}
	if !called {
		t.Error("Retry did not invoke the entry's Ops.Retry")
	}
}

// TestAddOverwritesInPlaceForRetry proves the mechanism a successful Retry
// relies on: re-Adding an Entry under an ID already in the registry
// replaces its pane/state/Ops but keeps the same List position — a retried
// worker stays the same row, it doesn't grow the list a second row.
func TestAddOverwritesInPlaceForRetry(t *testing.T) {
	r := NewRegistry()
	r.Add(&Entry{ID: "a", Name: "one", PaneID: "pane-1"})
	r.Add(&Entry{ID: "b", Name: "two", PaneID: "pane-2"})
	r.SetState("a", StateFailed, errors.New("boom"))

	// Simulate a successful Retry: a brand new Entry, same ID, new pane.
	r.Add(&Entry{ID: "a", Name: "one", PaneID: "pane-3", State: StateRunning})

	entries := r.List()
	if len(entries) != 2 {
		t.Fatalf("List() has %d entries, want 2 (retry replaces, doesn't append)", len(entries))
	}
	if entries[0].Name != "one" || entries[0].PaneID != "pane-3" {
		t.Errorf("entries[0] = %+v, want the retried entry (pane-3) still in slot 0", entries[0])
	}
	if entries[0].State != StateRunning {
		t.Errorf("entries[0].State = %q, want running after retry", entries[0].State)
	}
}
