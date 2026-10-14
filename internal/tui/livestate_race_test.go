// livestate_race_test.go pairs a goroutine calling livework.Default.SetState
// against the reader paths that render an entry's state —
// hostedRunningEntries, bandFieldsWorker/bandTitle, and quitConfirmBox — so
// `go test -race` reports any of them reading the state without the
// registry's lock. Only this test pairs a concurrent SetState against these
// reads, so a green -race run of the rest of the suite proves nothing.
package tui

import (
	"sync"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

func TestConcurrentStateReadsDoNotRace(t *testing.T) {
	// freshLivework(t) swaps livework.Default for a throwaway registry before
	// either goroutine below is spawned, so this entry can never make
	// hostedRunningEntries() non-empty for a later test — e.g.
	// TestRequestQuitWithNothingHostedQuitsImmediately, which is exactly the
	// failure this isolation exists to prevent (admin-hw2i). See
	// freshLivework's own doc comment in hermetic_test.go.
	freshLivework(t)
	e := &livework.Entry{
		ID: "livestate-race-entry", Name: "livestate-race",
		Handle: "pty-livestate-race",
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add(%s): %v", e.ID, err)
	}

	m := Model{shell: shell{quitHosted: []*livework.Entry{e}}}

	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			st := livework.StateRunning
			if i%2 == 1 {
				st = livework.StateWaiting
			}
			livework.Default.SetState(e.ID, st, nil)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			hostedRunningEntries()
			bandFieldsWorker(e, 40)
			bandTitle(40, e, e.Name)
			m.quitConfirmBox(quitBoxInner, 30)
		}
	}()

	wg.Wait()
}
