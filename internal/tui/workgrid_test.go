// workgrid_test.go covers admin-3rgp.72.11: the Work tab's Commands section
// renders every row on one fixed column grid (workview.go's wMarker/wName/
// wState/wElapsed), and a multi-target parent is a group the cursor can
// never land on. See docs/spikes/admin-tui-desktop/commands.txt and
// commands-running.txt, the reference frames this ticket is graded against.
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestCursorSkipsGroupRowMovingDown is half of the ticket's AC1: moving the
// cursor down must never land it on a multi-target parent's group row —
// multiTargetModel's "dev" (four targets) is exactly that row, at index 1.
func TestCursorSkipsGroupRowMovingDown(t *testing.T) {
	m := multiTargetModel(t)
	rows := m.workRows()
	if !rows[1].group {
		t.Fatalf("row 1 = %+v, want the group row (dev, four targets)", rows[1])
	}
	// Row 0 is the "Commands" header — pretending the cursor sits there (the
	// only place moveCursor(1) is ever asked to step off of, in practice) and
	// stepping down must skip straight over the group at row 1 onto its
	// first target at row 2.
	m.cursor[tabWork] = 0
	m.moveCursor(1)
	if got := m.cursor[tabWork]; got != 2 {
		t.Errorf("moveCursor(1) from the header landed on row %d (%+v), want row 2, the group's first target", got, rows[got])
	}
	if rows[m.cursor[tabWork]].group {
		t.Errorf("moveCursor(1) landed on a group row")
	}
}

// TestCursorSkipsGroupRowMovingUp is AC1's other direction: moving up must
// also never land on the group row.
func TestCursorSkipsGroupRowMovingUp(t *testing.T) {
	m := multiTargetModel(t)
	rows := m.workRows()
	// Row 2 is dev's first target (dev/ios); stepping up from there must
	// skip the group row at 1 and the header at 0, wrapping to the last
	// selectable row instead of ever landing on 1.
	m.cursor[tabWork] = 2
	m.moveCursor(-1)
	if rows[m.cursor[tabWork]].group {
		t.Errorf("moveCursor(-1) landed on a group row (index %d)", m.cursor[tabWork])
	}
	if !rows[m.cursor[tabWork]].selectable() {
		t.Errorf("moveCursor(-1) landed on an unselectable row (index %d, %+v)", m.cursor[tabWork], rows[m.cursor[tabWork]])
	}
}

// TestCursorWrapsPastGroupRow is AC1's "including when it is the first or
// last row": the group row sits immediately after the section header, the
// first candidate position moving down would otherwise reach, and wrapping
// from the last selectable row back to the top must skip over it rather than
// stopping there.
func TestCursorWrapsPastGroupRow(t *testing.T) {
	m := multiTargetModel(t)
	rows := m.workRows()

	// Find the last selectable row's index (test/cmd row) directly rather
	// than hard-coding it, so a fixture edit elsewhere in the package can't
	// silently desync this test from multiTargetModel's real shape.
	last := -1
	for i, r := range rows {
		if r.selectable() {
			last = i
		}
	}
	if last < 0 {
		t.Fatal("multiTargetModel produced no selectable rows at all")
	}

	m.cursor[tabWork] = last
	m.moveCursor(1)
	if got := m.cursor[tabWork]; rows[got].group {
		t.Errorf("wrapping down from the last row landed on the group row (index %d)", got)
	}
	if got := m.cursor[tabWork]; got != 2 {
		t.Errorf("wrapping down from the last row landed on index %d, want 2 (dev's first target, past the group at 1)", got)
	}
}

// TestRunningCommandRowShowsStateElapsedActivityOnItsOwnRow is AC2: a
// running command wears its state, elapsed and activity on the row it
// always was — no second row anywhere in the list carries them.
func TestRunningCommandRowShowsStateElapsedActivityOnItsOwnRow(t *testing.T) {
	freshLivework(t)
	entry := &livework.Entry{
		ID: "build", Name: "build", State: livework.StateRunning,
		Activity: "compiling host.go", Handle: "pty-1", StartedAt: time.Now().Add(-30 * time.Second),
	}
	if err := livework.Default.Add(entry); err != nil {
		t.Fatalf("Add(%s): %v", entry.ID, err)
	}
	wd := &workRowData{cmdName: "build", cmdDesc: "build it", cmdKind: "shell", live: entry}
	row := rowItem{kind: rowCommand, work: wd}

	line := stripANSI(workTestModel(nil).renderWorkRow(row, false, 60))
	for _, want := range []string{"running", "<1m", "compiling host.go"} {
		if !strings.Contains(line, want) {
			t.Errorf("running row = %q, want it to contain %q", line, want)
		}
	}
	if strings.Contains(line, "build it") {
		t.Errorf("running row = %q, must not also show the idle description", line)
	}

	mm := workTestModel(&manifest.Manifest{
		Custom:  map[string]manifest.Command{"build": {Desc: "build it", Steps: []string{"build"}}},
		Actions: map[string]manifest.Action{"build": {"kind": "shell"}},
	})
	seen := 0
	for _, r := range mm.workRows() {
		if r.kind == rowCommand && r.work != nil && r.work.cmdName == "build" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("workRows() produced %d rows for the running command %q, want exactly 1 (no second row)", seen, "build")
	}
}

// runeIndex is strings.Index in rune offsets rather than byte offsets — the
// rows this file renders mix multi-byte glyphs (◆, ◇, ●, ├, └) with plain
// ASCII, so a byte offset would disagree with the cell-based column math
// (wMarker, wName, ...) the moment a wide-in-bytes-but-single-cell glyph
// appears before the substring being located.
func runeIndex(s, substr string) int {
	b := strings.Index(s, substr)
	if b < 0 {
		return -1
	}
	return len([]rune(s[:b]))
}

// listContentWidth replicates renderWorkBody's own lw computation
// (workview.go) so this test drives the exact list width the real Work tab
// would hand renderWorkRow at a given terminal size, rather than an
// arbitrary width that happens to be wide enough. Duplicated rather than
// exported from workview.go since this test only needs the resulting
// number, not a shared seam into it. height is unused now that the row
// list's content width no longer depends on the terminal's aspect ratio —
// kept as a parameter so every call site here (which always passes both
// dimensions of a geometry) doesn't need to change.
func listContentWidth(width, _ int) int {
	return maxi(width-2, 20)
}

// TestNameColumnAlignsAcrossRowKindsAndGeometries is AC4: at every terminal
// size the ticket names, a group row's name column and a running row's
// state column begin at the identical screen column — the grid's whole
// point is that a group's blank marker and a runnable row's real marker
// both occupy exactly wMarker cells, so the wName-wide field after them
// lines up regardless of row kind.
func TestNameColumnAlignsAcrossRowKindsAndGeometries(t *testing.T) {
	freshLivework(t)
	entry := &livework.Entry{
		ID: "target-live", Name: "target-live", State: livework.StateRunning,
		Activity: "doing the thing", Handle: "pty-align", StartedAt: time.Now(),
	}
	if err := livework.Default.Add(entry); err != nil {
		t.Fatalf("Add(%s): %v", entry.ID, err)
	}

	groupWD := &workRowData{cmdName: "deploy", cmdDesc: "ship it", cmdKind: "widgetlabel", targets: []string{"a", "b"}}
	flatWD := &workRowData{cmdName: "build", cmdDesc: "build it", cmdKind: "widgetlabel"}
	targetWD := &workRowData{cmdName: "dev", targetKey: "cf", targetRun: "vitepress dev", live: entry}

	mm := workTestModel(nil)
	for _, geo := range []struct{ w, h int }{{80, 24}, {96, 30}, {178, 46}} {
		lw := listContentWidth(geo.w, geo.h)

		groupLine := stripANSI(renderGroupRow(groupWD, lw))
		flatLine := stripANSI(mm.renderWorkRow(rowItem{kind: rowCommand, work: flatWD}, false, lw))
		targetLine := stripANSI(mm.renderWorkRow(rowItem{kind: rowCommandTarget, work: targetWD}, false, lw))

		// The group and flat rows both place their kindLabel text at the
		// state column's start; the target row (given a live entry above)
		// places its glyph+state word ("● running") there instead — the
		// glyph itself is the state column's first cell, so it (not the bare
		// word, which sits two cells further in, after the glyph and its
		// separating space) is the fair comparison. All three must land at
		// the same rune offset: wMarker + wName.
		want := wMarker + wName
		groupIdx := runeIndex(groupLine, "widgetlabel")
		flatIdx := runeIndex(flatLine, "widgetlabel")
		targetIdx := runeIndex(targetLine, "● running")

		if groupIdx != want {
			t.Errorf("%dx%d (lw=%d): group row's state column starts at rune %d, want %d\n  line: %q", geo.w, geo.h, lw, groupIdx, want, groupLine)
		}
		if flatIdx != want {
			t.Errorf("%dx%d (lw=%d): flat row's state column starts at rune %d, want %d\n  line: %q", geo.w, geo.h, lw, flatIdx, want, flatLine)
		}
		if targetIdx != want {
			t.Errorf("%dx%d (lw=%d): target row's state column starts at rune %d, want %d\n  line: %q", geo.w, geo.h, lw, targetIdx, want, targetLine)
		}
	}
}

// TestCommandsHeaderCountsInvocationsNotGroups is AC7: the "Commands" header
// counts every resolved invocation — a target or a flat command — and never
// counts a group parent itself, matching commands.txt:4's "Commands  47".
func TestCommandsHeaderCountsInvocationsNotGroups(t *testing.T) {
	m := multiTargetModel(t)
	rows := m.workRows()
	if rows[0].kind != rowHeader || rows[0].label != "Commands" {
		t.Fatalf("rows[0] = %+v, want the Commands header", rows[0])
	}
	if len(rows[0].rightSegs) != 1 {
		t.Fatalf("Commands header rightSegs = %v, want exactly one count segment", rows[0].rightSegs)
	}
	// multiTargetModel declares "dev" (a group, 4 targets), "solo" (a single-
	// target multi-target — never a group, per commandRowItem) and "test": 4
	// targets + solo + test = 6 resolved invocations, dev's own group row
	// excluded.
	want := "  6"
	if got := rows[0].rightSegs[0].t; got != want {
		t.Errorf("Commands header count segment = %q, want %q", got, want)
	}
}

// TestGroupRowIsNotClickable is the mouse half of AC1: renderWorkBody must
// never hand a group row's screen line a hitRow target, matching the header/
// text rows it already treats the same way — clicking a group row must be
// as inert as clicking blank space, not silently move the cursor onto it.
func TestGroupRowIsNotClickable(t *testing.T) {
	m := multiTargetModel(t)
	m.width, m.height = 100, 30
	rows := m.workRows()
	groupIdx := -1
	for i, r := range rows {
		if r.group {
			groupIdx = i
			break
		}
	}
	if groupIdx < 0 {
		t.Fatal("multiTargetModel produced no group row")
	}

	_, hits := m.renderWorkBody(m.width, m.height)
	if groupIdx >= len(hits) {
		t.Fatalf("renderWorkBody returned %d hit rows, want at least %d", len(hits), groupIdx+1)
	}
	if hits[groupIdx].kind == hitRow {
		t.Errorf("group row at index %d is clickable (hitRow), want hitNone", groupIdx)
	}
}

// TestNameColumnLeavesGapBeforeKindColumn is admin-3rgp.72.19: a name that
// exactly fills (or overflows) the wName-wide name column must still leave
// at least one blank cell before the kind column starts, on both a flat
// command row and a group row — otherwise the elision mark and the kind
// word ("shell") read as one token, as they did for the real
// "tampermonkey-status" command. A short name is unaffected: its own
// padding already supplies the gap.
func TestNameColumnLeavesGapBeforeKindColumn(t *testing.T) {
	mm := workTestModel(nil)
	lw := listContentWidth(96, 30)

	long := "tampermonkey-status" // 20 chars: longer than wName (16)
	short := "build"

	cases := []struct {
		name string
		line string
	}{
		{"flat/long", stripANSI(mm.renderWorkRow(rowItem{kind: rowCommand, work: &workRowData{cmdName: long, cmdDesc: "d", cmdKind: "shell"}}, false, lw))},
		{"flat/short", stripANSI(mm.renderWorkRow(rowItem{kind: rowCommand, work: &workRowData{cmdName: short, cmdDesc: "d", cmdKind: "shell"}}, false, lw))},
		{"group/long", stripANSI(renderGroupRow(&workRowData{cmdName: long, cmdDesc: "d", cmdKind: "shell", targets: []string{"a", "b"}}, lw))},
		{"group/short", stripANSI(renderGroupRow(&workRowData{cmdName: short, cmdDesc: "d", cmdKind: "shell", targets: []string{"a", "b"}}, lw))},
	}

	for _, c := range cases {
		r := []rune(c.line)
		nameColEnd := wMarker + wName
		if nameColEnd > len(r) {
			t.Fatalf("%s: line too short: %q", c.name, c.line)
		}
		if r[nameColEnd-1] != ' ' {
			t.Errorf("%s: cell just before the kind column (rune %d) = %q, want a blank — got %q", c.name, nameColEnd-1, string(r[nameColEnd-1]), c.line)
		}
		kindIdx := runeIndex(c.line, "shell")
		if kindIdx != nameColEnd {
			t.Errorf("%s: kind column starts at rune %d, want %d (must not move) — line: %q", c.name, kindIdx, nameColEnd, c.line)
		}
	}
}
