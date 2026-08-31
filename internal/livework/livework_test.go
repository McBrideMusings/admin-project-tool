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
