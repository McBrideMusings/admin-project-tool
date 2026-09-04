// livestate_race_test.go is admin-ddc8's required regression test: it pairs a
// goroutine calling livework.Default.SetState against the reader paths that
// used to read Entry.State directly off a pointer with no lock —
// hostedRunningEntries, workerDetail, and quitConfirmContent. Before the fix,
// `go test -race -run TestConcurrentStateReadsDoNotRace ./internal/tui/`
// reports a data race on Entry.State; a green run alone (this suite already
// passes -race today without this test, because nothing else pairs a
// concurrent SetState against these specific reads) proves nothing.
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
		Handle: "pty-livestate-race", State: livework.StateRunning,
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add(%s): %v", e.ID, err)
	}

	m := Model{quitHosted: []*livework.Entry{e}}

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
			workerDetail(e, 40)
			m.quitConfirmContent()
		}
	}()

	wg.Wait()
}
