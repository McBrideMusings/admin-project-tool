package livework

import (
	"bytes"
	"errors"
	"sync"
	"testing"
)

// TestAddDefaultsStateAndTimestamp proves an Entry added with zero-value
// State/StartedAt gets StateRunning and a non-zero StartedAt — the
// "long-running kind ... appears in the live registry" acceptance criterion
// doesn't require the caller to fill in bookkeeping it can compute itself.
func TestAddDefaultsStateAndTimestamp(t *testing.T) {
	r := NewRegistry()
	r.Add(&Entry{ID: "w1:p1", Name: "dev-server", Kind: "vite-dev", Handle: "w1:p1", Via: "terminal"})

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
	r.Add(&Entry{ID: "a", Name: "one", Kind: "vite-dev", Handle: "pane-a", Via: "terminal"})
	r.Add(&Entry{ID: "b", Name: "two", Kind: "docker-dev", Handle: "pane-b", Via: "terminal"})

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
	r.Add(&Entry{ID: "a", Name: "one", Handle: "pane-a", Via: "terminal"})
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
	r.Add(&Entry{ID: "z", Name: "first", Handle: "pane-z", Via: "terminal"})
	r.Add(&Entry{ID: "a", Name: "second", Handle: "pane-a", Via: "terminal"})
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
	r.Add(&Entry{ID: "proc", Name: "dev-server", Kind: "vite-dev", Handle: "pane-proc", Via: "terminal"})
	r.Add(&Entry{ID: "worker", Name: "review", Kind: "workflow", Handle: "pane-worker", Via: "terminal",
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
	r.Add(&Entry{ID: "a", Name: "one", Handle: "pane-a", Via: "terminal"})
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
	r.Add(&Entry{ID: "a", Name: "one", Handle: "pane-a", Via: "terminal"})
	r.SetState("a", StateWaiting, nil)
	e, _ := r.Get("a")
	if e.State != StateWaiting {
		t.Errorf("State = %q, want %q", e.State, StateWaiting)
	}
	if e.State == StateRunning {
		t.Error("StateWaiting must not equal StateRunning")
	}
}

// TestStateStartingIsDistinctFromEveryOtherState proves the pre-registration
// state (internal/tui's pending-dispatch window) does not alias any of the
// states an actual Entry can carry.
func TestStateStartingIsDistinctFromEveryOtherState(t *testing.T) {
	for _, other := range []State{StateRunning, StateWaiting, StateExited, StateFailed} {
		if StateStarting == other {
			t.Errorf("StateStarting must not equal %q", other)
		}
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
	r.Add(&Entry{ID: "a", Name: "one", Handle: "pane-a", Via: "terminal", Ops: Ops{
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
	r.Add(&Entry{ID: "a", Name: "one", Handle: "pane-a", Via: "terminal"})
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
	r.Add(&Entry{ID: "a", Name: "one", Handle: "pane-a", Via: "terminal", Ops: Ops{
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
	r.Add(&Entry{ID: "a", Name: "one", Handle: "pane-a", Via: "terminal", State: StateRunning, Ops: Ops{
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
	r.Add(&Entry{ID: "a", Name: "one", Handle: "pane-a", Via: "terminal", Ops: Ops{
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
	r.Add(&Entry{ID: "a", Name: "one", Handle: "pane-1", Via: "terminal"})
	r.Add(&Entry{ID: "b", Name: "two", Handle: "pane-2", Via: "terminal"})
	r.SetState("a", StateFailed, errors.New("boom"))

	// Simulate a successful Retry: a brand new Entry, same ID, new pane.
	r.Add(&Entry{ID: "a", Name: "one", Handle: "pane-3", Via: "terminal", State: StateRunning})

	entries := r.List()
	if len(entries) != 2 {
		t.Fatalf("List() has %d entries, want 2 (retry replaces, doesn't append)", len(entries))
	}
	if entries[0].Name != "one" || entries[0].Handle != "pane-3" {
		t.Errorf("entries[0] = %+v, want the retried entry (pane-3) still in slot 0", entries[0])
	}
	if entries[0].State != StateRunning {
		t.Errorf("entries[0].State = %q, want running after retry", entries[0].State)
	}
}

// TestAddRejectsEmptyHandle proves Add refuses (and stores nothing for) an
// entry carrying an empty Handle — an entry must address something.
func TestAddRejectsEmptyHandle(t *testing.T) {
	r := NewRegistry()
	err := r.Add(&Entry{ID: "a", Name: "one"})
	if err == nil {
		t.Fatal("Add() = nil, want an error for an entry with an empty Handle")
	}
	if _, ok := r.Get("a"); ok {
		t.Error("Get(a) found an entry, want the rejected Add to have stored nothing")
	}
}

// TestAttachOpensTerminalForPTYEntry proves a hosted-pty entry's (Via
// empty) Attach resolves to the installed terminal opener, called with its
// Handle, rather than Ops.Attach.
//
// MUTATION EVIDENCE: change Attach to always call e.Ops.Attach regardless of
// Via — this test fails because the opener is never invoked and Attach
// returns the launcher-only "does not support attach" error instead of nil.
func TestAttachOpensTerminalForPTYEntry(t *testing.T) {
	r := NewRegistry()
	var openedID string
	r.SetTerminalOpener(func(handle string) error {
		openedID = handle
		return nil
	})
	r.Add(&Entry{ID: "a", Name: "one", Handle: "pty-7"})

	if err := r.Attach("a"); err != nil {
		t.Errorf("Attach() = %v, want nil", err)
	}
	if openedID != "pty-7" {
		t.Errorf("terminal opener got %q, want %q", openedID, "pty-7")
	}
}

// TestAttachPTYEntryWithNoOpenerErrors proves a hosted-pty entry with no
// terminal opener installed errors clearly rather than panicking.
func TestAttachPTYEntryWithNoOpenerErrors(t *testing.T) {
	r := NewRegistry()
	r.Add(&Entry{ID: "a", Name: "one", Handle: "pty-7"})
	if err := r.Attach("a"); err == nil {
		t.Error("Attach() = nil, want an error when no terminal opener is installed")
	}
}

// TestRetryReplacesPTYEntryInPlace proves the pty analogue of
// TestAddOverwritesInPlaceForRetry: re-Adding a hosted-pty entry under an
// ID already in the registry (a successful Retry building a brand new Entry
// with a new Handle) replaces it in place — one row, not two — exactly like
// a launcher-dispatched retry.
func TestRetryReplacesPTYEntryInPlace(t *testing.T) {
	r := NewRegistry()
	r.Add(&Entry{ID: "a", Name: "one", Handle: "pty-1"})
	r.Add(&Entry{ID: "b", Name: "two", Handle: "pane-b", Via: "terminal"})
	r.SetState("a", StateFailed, errors.New("boom"))

	// Simulate a successful Retry: a brand new Entry, same ID, new pty.
	if err := r.Add(&Entry{ID: "a", Name: "one", Handle: "pty-2", State: StateRunning}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries := r.List()
	if len(entries) != 2 {
		t.Fatalf("List() has %d entries, want 2 (retry replaces, doesn't append)", len(entries))
	}
	if entries[0].Name != "one" || entries[0].Handle != "pty-2" {
		t.Errorf("entries[0] = %+v, want the retried entry (pty-2) still in slot 0", entries[0])
	}
	if entries[0].State != StateRunning {
		t.Errorf("entries[0].State = %q, want running after retry", entries[0].State)
	}
}

// TestRingBoundedOverwrite proves a Ring never grows past its limit and
// keeps only the most recent bytes once it overflows — the property a
// long-lived hosted pty depends on to not grow the registry unboundedly.
func TestRingBoundedOverwrite(t *testing.T) {
	r := NewRing(4)

	if n, err := r.Write([]byte("ab")); n != 2 || err != nil {
		t.Fatalf("Write(ab) = (%d, %v), want (2, nil)", n, err)
	}
	if got := r.Bytes(); !bytes.Equal(got, []byte("ab")) {
		t.Errorf("Bytes() = %q, want %q", got, "ab")
	}

	// Overflow: writing "cdef" (4 bytes) onto a ring already holding "ab"
	// (2 bytes, limit 4) must retain only the last 4 bytes written overall.
	if _, err := r.Write([]byte("cdef")); err != nil {
		t.Fatalf("Write(cdef): %v", err)
	}
	if got := r.Bytes(); !bytes.Equal(got, []byte("cdef")) {
		t.Errorf("Bytes() after overflow = %q, want %q", got, "cdef")
	}
	if got := r.Len(); got != 4 {
		t.Errorf("Len() = %d, want 4 (bounded at the ring's limit)", got)
	}

	// A single write longer than the whole ring leaves only its own tail.
	if _, err := r.Write([]byte("0123456789")); err != nil {
		t.Fatalf("Write(0123456789): %v", err)
	}
	if got := r.Bytes(); !bytes.Equal(got, []byte("6789")) {
		t.Errorf("Bytes() after an oversized write = %q, want %q", got, "6789")
	}
}

// TestRingBytesSnapshot proves Bytes() returns an oldest-first copy that is
// independent of the ring — a caller can hold and read it safely even while
// further Writes land, which matters once a later reader (admin-3rgp.65)
// polls Bytes() while the drain goroutine keeps writing.
func TestRingBytesSnapshot(t *testing.T) {
	r := NewRing(8)
	if _, err := r.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	snap := r.Bytes()
	if !bytes.Equal(snap, []byte("hello")) {
		t.Fatalf("Bytes() = %q, want %q", snap, "hello")
	}

	// Mutating the snapshot must not affect the ring's own storage.
	snap[0] = 'X'
	if again := r.Bytes(); !bytes.Equal(again, []byte("hello")) {
		t.Errorf("Bytes() after mutating a prior snapshot = %q, want %q (snapshot must be independent)", again, "hello")
	}

	// A further write must not retroactively change an already-taken
	// snapshot.
	if _, err := r.Write([]byte("!")); err != nil {
		t.Fatalf("Write(!): %v", err)
	}
	if !bytes.Equal(snap, []byte("Xello")) {
		t.Errorf("prior snapshot changed after a later Write: got %q", snap)
	}
}

// TestRingConcurrentWriteAndRead proves a Ring survives concurrent
// Write/Bytes calls under the race detector — the shape a drain goroutine
// (constantly writing) and a terminal-view reader (constantly polling
// Bytes) will actually exercise it in.
func TestRingConcurrentWriteAndRead(t *testing.T) {
	r := NewRing(256)
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_, _ = r.Write([]byte("x"))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = r.Bytes()
			_ = r.Len()
		}
	}()
	wg.Wait()

	if got := r.Len(); got > 256 {
		t.Errorf("Len() = %d, want <= 256 (the ring's limit)", got)
	}
}
