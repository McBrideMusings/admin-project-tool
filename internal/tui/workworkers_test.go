// workworkers_test.go covers admin-3rgp.72.16: Workers is its own section
// under the Commands list, rendered on the same column grid, under one
// combined cursor that walks Commands then Workers as a single ordered list.
// See docs/spikes/admin-tui-desktop/swarm.txt and commands.txt, the reference
// frames this ticket is graded against.
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// workersFixtureModel builds a Model with 5 flat commands and 3 live workers
// (running/waiting/failed, matching swarm.txt's own fixture) — enough rows
// to exercise the combined cursor without needing to scroll.
func workersFixtureModel(t *testing.T) (Model, []rowItem) {
	t.Helper()
	freshLivework(t)
	custom := map[string]manifest.Command{}
	actions := map[string]manifest.Action{}
	for _, name := range []string{"build", "deploy", "lint", "fmt", "clean"} {
		custom[name] = manifest.Command{Desc: "desc " + name, Steps: []string{name}}
		actions[name] = manifest.Action{"kind": "shell"}
	}
	m := &manifest.Manifest{Custom: custom, Actions: actions}

	entries := []*livework.Entry{
		{ID: "admin-3rgp.36", Name: "admin-3rgp.36", Handle: "pane 3", Via: "terminal",
			State: livework.StateRunning, StartedAt: time.Now().Add(-18 * time.Minute),
			Worker: &livework.WorkerInfo{Branch: "b1", Worktree: "/tmp/b1"}},
		{ID: "admin-3rgp.7", Name: "admin-3rgp.7", Handle: "pane 4", Via: "terminal",
			State: livework.StateWaiting, StartedAt: time.Now().Add(-6 * time.Minute),
			Worker: &livework.WorkerInfo{Branch: "b2", Worktree: "/tmp/b2"}},
		{ID: "admin-3rgp.33", Name: "admin-3rgp.33", Handle: "pane 5", Via: "terminal",
			State: livework.StateFailed, StartedAt: time.Now().Add(-2 * time.Minute),
			Worker: &livework.WorkerInfo{Branch: "b3", Worktree: "/tmp/b3"}},
	}
	for _, e := range entries {
		if err := livework.Default.Add(e); err != nil {
			t.Fatalf("Add(%s): %v", e.ID, err)
		}
	}
	livework.Default.SetActivity("admin-3rgp.36", "writing internal/lua/host.go")
	livework.Default.SetActivity("admin-3rgp.7", "needs input: root command name")
	livework.Default.SetActivity("admin-3rgp.33", "go test ./internal/detect FAIL")

	mm := workTestModel(m)
	mm.focus = focusBody
	mm.width, mm.height = 96, 25
	return mm, mm.workRows()
}

// firstSelectable/lastSelectable are the fixture's first and last selectable
// row indices — the "top of Commands" and "last Workers row" the ticket's
// own interaction spec names.
func firstSelectable(rows []rowItem) int {
	for i, r := range rows {
		if r.selectable() {
			return i
		}
	}
	return -1
}

func lastSelectable(rows []rowItem) int {
	last := -1
	for i, r := range rows {
		if r.selectable() {
			last = i
		}
	}
	return last
}

func selectableCommandCount(rows []rowItem) int {
	n := 0
	for _, r := range rows {
		if r.selectable() && r.kind != rowWorker {
			n++
		}
	}
	return n
}

// TestNDownsFromTopLandsOnFirstWorkerRow is AC1: N presses of ↓ (N = the
// selectable-Commands count) from the top of Commands lands on the first
// Workers row, and the band's block changes at exactly that step.
func TestNDownsFromTopLandsOnFirstWorkerRow(t *testing.T) {
	mm, rows := workersFixtureModel(t)
	n := selectableCommandCount(rows)
	firstWorker := -1
	for i, r := range rows {
		if r.kind == rowWorker {
			firstWorker = i
			break
		}
	}
	if firstWorker < 0 {
		t.Fatal("fixture produced no worker row")
	}

	mm.cursor[tabWork] = firstSelectable(rows)
	for i := 0; i < n-1; i++ {
		mm.moveCursor(1)
		if rows[mm.cursor[tabWork]].kind == rowWorker {
			t.Fatalf("moveCursor landed on a worker row after only %d of %d downs (index %d)", i+1, n, mm.cursor[tabWork])
		}
	}
	// The N-th down is the transition: band content just before it must be a
	// command's, and right after it must be the worker's.
	before := mm.renderWorkBand(rows[mm.cursor[tabWork]], 90, 12)
	mm.moveCursor(1)
	if got := mm.cursor[tabWork]; got != firstWorker {
		t.Fatalf("N=%d downs from the top landed on row %d (%+v), want %d (the first Workers row)", n, got, rows[got], firstWorker)
	}
	after := mm.renderWorkBand(rows[mm.cursor[tabWork]], 90, 12)
	if strings.Join(before, "\n") == strings.Join(after, "\n") {
		t.Errorf("band content did not change at the Commands->Workers transition")
	}
}

// TestNoCrossSectionMarker is AC2: no Commands row shows the cursor marker
// while the cursor is in Workers, and vice versa.
func TestNoCrossSectionMarker(t *testing.T) {
	mm, rows := workersFixtureModel(t)

	firstWorker := -1
	for i, r := range rows {
		if r.kind == rowWorker {
			firstWorker = i
			break
		}
	}
	mm.cursor[tabWork] = firstWorker
	lines, _ := mm.renderWorkBody(96, 25)
	for i, r := range rows {
		if r.kind != rowCommand || r.work == nil || len(r.work.targets) > 0 {
			continue
		}
		if i < len(lines) && strings.Contains(stripANSI(lines[i]), "◆") {
			t.Errorf("Commands row %d carries the current-cursor marker while the cursor is on a worker row: %q", i, stripANSI(lines[i]))
		}
	}

	mm.cursor[tabWork] = firstSelectable(rows)
	lines, _ = mm.renderWorkBody(96, 25)
	for i, r := range rows {
		if r.kind != rowWorker {
			continue
		}
		if i < len(lines) && strings.Contains(stripANSI(lines[i]), "◆") {
			t.Errorf("Worker row %d carries the current-cursor marker while the cursor is on a Commands row: %q", i, stripANSI(lines[i]))
		}
	}
}

// TestUpFromTopWrapsToLastWorkerRow is AC3.
func TestUpFromTopWrapsToLastWorkerRow(t *testing.T) {
	mm, rows := workersFixtureModel(t)
	last := lastSelectable(rows)
	if rows[last].kind != rowWorker {
		t.Fatalf("fixture's last selectable row is %+v, want a worker row", rows[last])
	}
	mm.cursor[tabWork] = firstSelectable(rows)
	mm.moveCursor(-1)
	if got := mm.cursor[tabWork]; got != last {
		t.Errorf("up from the top row landed on %d (%+v), want %d, the last Workers row", got, rows[got], last)
	}
}

// TestScrolledListNamesItsSection is AC4: a scrolled combined list's first
// visible row names the section (Commands or Workers) the window is inside —
// docs/spikes/admin-tui-desktop/swarm.txt:4's "Commands   ↑ 49 more".
func TestScrolledListNamesItsSection(t *testing.T) {
	freshLivework(t)
	custom := map[string]manifest.Command{}
	actions := map[string]manifest.Action{}
	for i := 0; i < 5; i++ {
		name := "cmd" + string(rune('a'+i))
		custom[name] = manifest.Command{Desc: "desc", Steps: []string{name}}
		actions[name] = manifest.Action{"kind": "shell"}
	}
	m := &manifest.Manifest{Custom: custom, Actions: actions}
	for i := 0; i < 20; i++ {
		id := "worker" + string(rune('a'+i))
		e := &livework.Entry{
			ID: id, Name: id, Handle: "pane", Via: "terminal", State: livework.StateRunning,
			StartedAt: time.Now(), Worker: &livework.WorkerInfo{Branch: "b", Worktree: "/tmp/w"},
		}
		if err := livework.Default.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	mm := workTestModel(m)
	mm.focus = focusBody
	mm.width, mm.height = 96, 25
	rows := mm.workRows()

	// Cursor on the last row scrolls the window down into the Workers
	// section, so the pinned first row must name "Workers", not "Commands".
	mm.cursor[tabWork] = lastSelectable(rows)
	lines, _ := mm.renderWorkBody(96, 25)
	first := stripANSI(lines[0])
	if !strings.Contains(first, "Workers") || !strings.Contains(first, "more") {
		t.Errorf("scrolled-into-Workers first line = %q, want it to name Workers and show the more-marker", first)
	}
	if strings.Contains(first, "Commands") {
		t.Errorf("scrolled-into-Workers first line = %q, must not still say Commands", first)
	}
}

// TestWorkersHeaderCarriesCount is the Workers header's own copy: "Workers"
// bold plus its count, dim — matching swarm.txt:13's "Workers  3".
func TestWorkersHeaderCarriesCount(t *testing.T) {
	_, rows := workersFixtureModel(t)
	for _, r := range rows {
		if r.kind == rowHeader && r.label == "Workers" {
			if len(r.rightSegs) != 1 || r.rightSegs[0].t != "  3" {
				t.Errorf("Workers header rightSegs = %v, want a single \"  3\" count segment", r.rightSegs)
			}
			return
		}
	}
	t.Fatal("no Workers header found")
}

// TestWorkersEmptyStateVerbatim pins the empty-Workers copy exactly as the
// design specifies (docs/spikes/admin-tui-desktop/variants.go:1373).
func TestWorkersEmptyStateVerbatim(t *testing.T) {
	mm := workTestModel(&manifest.Manifest{
		Custom:  map[string]manifest.Command{"build": {Desc: "build it", Steps: []string{"build"}}},
		Actions: map[string]manifest.Action{"build": {"kind": "shell"}},
	})
	want := "no workers running — dispatch a bead from the tracker tab"
	found := false
	for _, r := range mm.workRows() {
		if r.kind == rowText && r.label == want {
			found = true
		}
	}
	if !found {
		t.Errorf("workRows() did not contain the verbatim empty-Workers line %q", want)
	}
}

// TestWorkerRowMatchesCommandsGrid is the Workers row's own visual
// acceptance: it renders on the identical column grid the Commands section
// uses (marker/name/state/elapsed), with the worker-only ◈ running glyph and
// a trailing where-column — verified against swarm.txt:14 at w=96 (lw=94).
func TestWorkerRowMatchesCommandsGrid(t *testing.T) {
	mm, rows := workersFixtureModel(t)
	var line string
	for i, r := range rows {
		if r.kind == rowWorker && r.work.worker.ID == "admin-3rgp.36" {
			line = stripANSI(mm.renderWorkRow(r, false, 94))
			_ = i
			break
		}
	}
	if line == "" {
		t.Fatal("worker row admin-3rgp.36 not found")
	}
	want := "◇ admin-3rgp.36   ◈ running 18m    writing internal/lua/host.go                        pane 3"
	// Compare the padded-to-94 forms so trailing-space differences (padding
	// added elsewhere) don't fail this on an otherwise-correct render.
	if padEnd(line, 94) != padEnd(want, 94) {
		t.Errorf("worker row =\n  %q\nwant\n  %q", padEnd(line, 94), padEnd(want, 94))
	}
}

// TestWorkerRowDropsWhereColumnWhenNarrow covers the design's own drop rule
// (docs/spikes/admin-tui-desktop/variants.go:1349-1351): the where-column
// disappears rather than crowding the activity column below 8 cells.
func TestWorkerRowDropsWhereColumnWhenNarrow(t *testing.T) {
	mm, rows := workersFixtureModel(t)
	var row rowItem
	for _, r := range rows {
		if r.kind == rowWorker && r.work.worker.ID == "admin-3rgp.36" {
			row = r
			break
		}
	}
	if row.work == nil {
		t.Fatal("worker row admin-3rgp.36 not found")
	}
	// wMarker+wName+wState+wElapsed+1 = 36; a width just above that leaves
	// under 8 cells for activity once the where-column's own width is
	// subtracted, so it must be dropped rather than squeezed in.
	line := stripANSI(mm.renderWorkRow(row, false, 40))
	if strings.Contains(line, "pane 3") {
		t.Errorf("narrow worker row still shows the where-column: %q", line)
	}
}
