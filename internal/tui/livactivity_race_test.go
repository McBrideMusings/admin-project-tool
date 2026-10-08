// livactivity_race_test.go is admin-8hlz.1's required regression test: it
// mirrors livestate_race_test.go's TestConcurrentStateReadsDoNotRace for the
// neighbouring field. It pairs a goroutine calling livework.Default.
// SetActivity against the reader paths that used to read Entry.Activity
// directly off a pointer with no lock — renderWorkRow (both the rowWorker
// and the wd.live branches), renderTargetRow, and bandFieldsWorker.
// attachBreadcrumb (admin-3rgp.72.17's replacement for the since-removed
// renderTermHeader) is exercised alongside them too, though it no longer
// reads Activity at all — the breadcrumb's copy has no activity field, only
// glyph/name/state/elapsed plus the log path — so it now guards the State
// read termEntry does instead. Before the Registry.Activity fix,
// `go test -race -run TestConcurrentActivityReadsDoNotRace ./internal/tui/`
// reports a data race on Entry.Activity; a green run alone proves nothing,
// since nothing else pairs a concurrent SetActivity against these specific
// reads. The 2-goroutine/200-iteration shape livestate_race_test.go uses for
// State did not reliably surface the race for this field on this machine —
// four reader goroutines and 20000 iterations does, and was confirmed to
// fail against the pre-fix (direct Entry.Activity read) code before this
// file's iteration count was raised.
package tui

import (
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
