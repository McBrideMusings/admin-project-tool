// livactivity_race_test.go is livestate_race_test.go's counterpart for the
// activity line: it pairs a goroutine calling livework.Default.SetActivity
// against the reader paths that render it — renderWorkRow (both the
// rowWorker and the wd.live branches), renderTargetRow, and bandFieldsWorker
// — plus attachBreadcrumb, which reads the state termEntry looks up. `go
// test -race` reports any of them reading without the registry's lock. Four
// reader goroutines and 20000 iterations are what it takes for -race to
// catch an unlocked activity read on this machine; two goroutines and 200
// iterations miss it.
package tui

import (
	"github.com/McBrideMusings/admin-project-tool/internal/viewstate"
	"sync"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

func TestConcurrentActivityReadsDoNotRace(t *testing.T) {
	// freshLivework(t) swaps livework.Default for a throwaway registry before
	// either goroutine below is spawned, so this entry can never linger for a
	// later test the way a process-wide singleton could — see its own doc
	// comment (internal/tui/hermetic_test.go) and admin-hw2i.
	freshLivework(t)
	e := &livework.Entry{
		ID: "livactivity-race-entry", Name: "livactivity-race",
		Handle: "pty-livactivity-race",
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add(%s): %v", e.ID, err)
	}

	workerRow := rowItem{kind: rowWorker, work: &workRowData{worker: e}}
	liveCommandRow := rowItem{kind: rowCommand, work: &workRowData{cmdName: "cmd", live: e}}
	targetWD := &workRowData{live: e, targetKey: "target", targetLast: true}
	m := Model{termAttached: true, termEntryID: e.ID}
	m.mountTabs(viewstate.State{})

	const iterations = 20000
	var wg sync.WaitGroup
	wg.Add(5)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			activity := "doing work"
			if i%2 == 1 {
				activity = "doing other work"
			}
			livework.Default.SetActivity(e.ID, activity)
		}
	}()

	for n := 0; n < 4; n++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				m.renderWorkRow(workerRow, false, 80)
				m.renderWorkRow(liveCommandRow, false, 80)
				renderTargetRow(targetWD, false, 40)
				bandFieldsWorker(e, 40)
				m.attachBreadcrumb(80)
			}
		}()
	}

	wg.Wait()
}
