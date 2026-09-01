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
	e := &livework.Entry{
		ID: "livestate-race-entry", Name: "livestate-race",
		PTYID: "pty-livestate-race", State: livework.StateRunning,
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add(%s): %v", e.ID, err)
	}
	// livework.Default is a process-wide singleton with no Remove — leaving
	// this entry StateRunning would make hostedRunningEntries() non-empty for
	// every test that runs after this one, including
	// TestRequestQuitWithNothingHostedQuitsImmediately.
	t.Cleanup(func() {
		livework.Default.SetState(e.ID, livework.StateExited, nil)
	})

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
