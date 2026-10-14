// workworkers_test.go covers the Workers tab (admin-3rgp.72.16, split from the
// Commands tab by admin-spjp.9): one flat list of every live worker, rendered
// on the same column grid as a command row, under its own cursor.
package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// workersFixtureModel builds a Model on the Workers tab with 3 live workers
// (two running, one waiting) — enough rows
// to exercise the cursor without needing to scroll.
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
			StartedAt: time.Now().Add(-18 * time.Minute),
			Worker:    &livework.WorkerInfo{Branch: "b1", Worktree: "/tmp/b1"}},
		{ID: "admin-3rgp.7", Name: "admin-3rgp.7", Handle: "pane 4", Via: "terminal",
			StartedAt: time.Now().Add(-6 * time.Minute),
			Worker:    &livework.WorkerInfo{Branch: "b2", Worktree: "/tmp/b2"}},
		{ID: "admin-3rgp.33", Name: "admin-3rgp.33", Handle: "pane 5", Via: "terminal",
			StartedAt: time.Now().Add(-2 * time.Minute),
			Worker:    &livework.WorkerInfo{Branch: "b3", Worktree: "/tmp/b3"}},
	}
	for _, e := range entries {
		if err := livework.Default.Add(e); err != nil {
			t.Fatalf("Add(%s): %v", e.ID, err)
		}
	}
	livework.Default.SetState("admin-3rgp.7", livework.StateWaiting, nil)
	livework.Default.SetActivity("admin-3rgp.36", "writing internal/lua/host.go")
	livework.Default.SetActivity("admin-3rgp.7", "needs input: root command name")
	livework.Default.SetActivity("admin-3rgp.33", "running go test ./internal/detect")

	mm := workTestModel(m)
	mm.focus = focusBody
	mm.width, mm.height = 96, 25
	mm.active = tabWorkers
	return mm, mm.workersTab.rows(&mm.shell)
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

// TestDownMovesBetweenWorkerRows: ↓ steps through the worker rows in order and
// the band's content changes with the row.
func TestDownMovesBetweenWorkerRows(t *testing.T) {
	mm, rows := workersFixtureModel(t)
	mm.workersTab.cursor = firstSelectable(rows)
	first := mm.workersTab.cursor
	before := mm.renderWorkBand(rows[first], 90, 12)
	mm.moveCursor(1)
	if got := mm.workersTab.cursor; got != first+1 {
		t.Fatalf("↓ from the first worker landed on row %d, want %d", got, first+1)
	}
	after := mm.renderWorkBand(rows[mm.workersTab.cursor], 90, 12)
	if strings.Join(before, "\n") == strings.Join(after, "\n") {
		t.Errorf("band content did not change between the first and second worker")
	}
}

// TestUpFromTopWrapsToLastWorkerRow: ↑ from the first worker wraps to the last.
func TestUpFromTopWrapsToLastWorkerRow(t *testing.T) {
	mm, rows := workersFixtureModel(t)
	last := lastSelectable(rows)
	if rows[last].kind != rowWorker {
		t.Fatalf("fixture's last selectable row is %+v, want a worker row", rows[last])
	}
	mm.workersTab.cursor = firstSelectable(rows)
	mm.moveCursor(-1)
	if got := mm.workersTab.cursor; got != last {
		t.Errorf("up from the top row landed on %d (%+v), want %d, the last Workers row", got, rows[got], last)
	}
}

// TestScrolledListNamesItsSection: a scrolled Workers list's first visible row
// names the section the window is inside, with the more-marker —
// "Commands   ↑ 49 more".
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
			ID: id, Name: id, Handle: "pane", Via: "terminal",
			StartedAt: time.Now(), Worker: &livework.WorkerInfo{Branch: "b", Worktree: "/tmp/w"},
		}
		if err := livework.Default.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	mm := workTestModel(m)
	mm.focus = focusBody
	mm.width, mm.height = 96, 25
	mm.active = tabWorkers
	rows := mm.workersTab.rows(&mm.shell)

	// Cursor on the last row scrolls the window down, so the pinned first row
	// must still name "Workers".
	mm.workersTab.cursor = lastSelectable(rows)
	lines, _ := mm.renderWorkBody(96, 25)
	first := stripANSI(lines[0])
	if !strings.Contains(first, "Workers") || !strings.Contains(first, "more") {
		t.Errorf("scrolled-into-Workers first line = %q, want it to name Workers and show the more-marker", first)
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

// TestWorkersEmptyHintNamesDeclaredSources pins the empty-Workers line: it
// names the places this manifest can start a worker from, and says so when
// there are none.
func TestWorkersEmptyHintNamesDeclaredSources(t *testing.T) {
	freshLivework(t)
	for _, tc := range []struct {
		name    string
		actions map[string]manifest.Action
		want    string
	}{
		{"no workflow", map[string]manifest.Action{"build": {"kind": "shell"}},
			"no workers running — this project declares no workflow"},
		{"query workflow", map[string]manifest.Action{"build": {"kind": "workflow", "over": map[string]any{"query": "label:stale"}}},
			"no workers running — start one: build (Commands)"},
	} {
		mm := workTestModel(&manifest.Manifest{
			Custom:  map[string]manifest.Command{"build": {Desc: "build it", Steps: []string{"build"}}},
			Actions: tc.actions,
		})
		found := false
		for _, r := range mm.workersTab.rows(&mm.shell) {
			if r.kind == rowText && r.label == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: Workers rows did not contain %q", tc.name, tc.want)
		}
	}
}

// TestWorkersEmptyKeepsBandAndOffersNoEnter pins the rest of workers-empty.txt:
// the band keeps its rule and last line, its right side counts what IS
// running, and the footer offers no ↵ over a list with no row.
func TestWorkersEmptyKeepsBandAndOffersNoEnter(t *testing.T) {
	freshLivework(t)
	if err := livework.Default.Add(&livework.Entry{ID: "build", Name: "build", Handle: "pane-build", Via: "terminal"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	mm := workTestModel(&manifest.Manifest{
		Custom:  map[string]manifest.Command{"build": {Desc: "build it", Steps: []string{"build"}}},
		Actions: map[string]manifest.Action{"build": {"kind": "shell"}},
	})
	mm.active = tabWorkers
	band := mm.renderWorkBand(rowItem{kind: rowText}, 80, 9)
	if len(band) != 9 {
		t.Fatalf("band has %d rows, want 9", len(band))
	}
	if got := stripANSI(band[0]); !strings.HasPrefix(got, "── no worker ─") || !strings.HasSuffix(got, " 1 command running ──") {
		t.Errorf("band rule = %q", got)
	}
	if got := strings.TrimSpace(stripANSI(band[8])); got != "a worker's log, branch, worktree and session appear here" {
		t.Errorf("band last row = %q", got)
	}
	for _, p := range mm.workFooterPairs() {
		if p[0] == "↵" {
			t.Errorf("footer offers %v on an empty Workers tab", p)
		}
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

// TestWorkerRowDropsWhereColumnWhenNarrow covers the drop rule: the where-column
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

// TestAttachKeyAttachesFromWorkersTab: `a` on the Workers tab reaches the
// worker's Ops.Attach, the same call the Commands tab's `a` makes.
func TestAttachKeyAttachesFromWorkersTab(t *testing.T) {
	mm, rows := workersFixtureModel(t)
	attached := false
	e, ok := livework.Default.Get("admin-3rgp.36")
	if !ok {
		t.Fatal("fixture worker missing from the registry")
	}
	e.Ops.Attach = func() error { attached = true; return nil }
	for i, r := range rows {
		if r.kind == rowWorker && r.work.worker.ID == "admin-3rgp.36" {
			mm.workersTab.cursor = i
		}
	}
	mm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if !attached {
		t.Error("a on the Workers tab did not invoke Ops.Attach")
	}
}

// TestTabBarDrawsCommandsThenWorkers: a project with a manifest draws two tabs
// where the one Work tab used to be, and the Workers badge counts live workers.
func TestTabBarDrawsCommandsThenWorkers(t *testing.T) {
	mm, _ := workersFixtureModel(t)
	tabs := mm.visibleTabs()
	if len(tabs) < 2 || tabs[0] != tabWork || tabs[1] != tabWorkers {
		t.Fatalf("visibleTabs = %v, want Commands then Workers first", tabs)
	}
	if tabDisplay(tabWork) != "Commands" || tabDisplay(tabWorkers) != "Workers" {
		t.Errorf("labels = %q, %q", tabDisplay(tabWork), tabDisplay(tabWorkers))
	}
	if got := mm.tabCount(tabWorkers); got != 3 {
		t.Errorf("Workers badge = %d, want 3", got)
	}
}

// TestAttachedWorkerShowsItsFieldBlock: the attached view's field block finds a
// worker through the Workers rows, so it carries the worker's branch row
// rather than falling back to the log path alone.
func TestAttachedWorkerShowsItsFieldBlock(t *testing.T) {
	mm, _ := workersFixtureModel(t)
	mm.termAttached = true
	mm.termEntryID = "admin-3rgp.36"
	got := stripANSI(strings.Join(mm.termFields(90), "\n"))
	if !strings.Contains(got, "b1") {
		t.Errorf("attached worker field block lacks its branch %q: %q", "b1", got)
	}
}
