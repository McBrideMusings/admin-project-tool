package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
)

// --- selection state machine ---

func TestSelectionForwardDrag(t *testing.T) {
	s := anchorAt(2, 5)
	s.drag(4, 10)
	if !s.active() {
		t.Fatal("selection did not become active after a drag")
	}
	start, end := s.ordered()
	if start != (cell{2, 5}) || end != (cell{4, 10}) {
		t.Errorf("ordered() = %v,%v, want (2,5),(4,10)", start, end)
	}
}

func TestSelectionBackwardDrag(t *testing.T) {
	s := anchorAt(4, 10)
	s.drag(2, 5)
	start, end := s.ordered()
	if start != (cell{2, 5}) || end != (cell{4, 10}) {
		t.Errorf("ordered() = %v,%v, want (2,5),(4,10) even dragged backward", start, end)
	}
}

func TestSelectionClickWithoutDragDoesNotFinish(t *testing.T) {
	s := anchorAt(5, 10)
	if s.finish() {
		t.Error("finish() true for a bare click — the anchor never moved")
	}
	if s.active() {
		t.Error("a bare click must not be active/visible")
	}
}

func TestSelectionDragThenFinish(t *testing.T) {
	s := anchorAt(5, 10)
	s.drag(5, 20)
	if !s.finish() {
		t.Error("finish() false after a real drag")
	}
}

func TestExtendToEdgePushesTowardTheLastMotionDirection(t *testing.T) {
	cases := []struct {
		name             string
		anchor, m1, m2   cell // press, then two motions
		maxRow           int
		wantRow, wantCol int
	}{
		{"down", cell{1, 3}, cell{2, 3}, cell{4, 3}, 39, 39, 3},
		{"up", cell{20, 3}, cell{19, 3}, cell{17, 3}, 39, 0, 3},
		{"right", cell{5, 3}, cell{5, 6}, cell{5, 9}, 39, 5, colEdge},
		{"left", cell{5, 20}, cell{5, 17}, cell{5, 12}, 39, 5, 0},
		{"diagonal", cell{1, 3}, cell{2, 5}, cell{4, 8}, 39, 39, colEdge},
		{"no motion since anchor", cell{5, 5}, cell{5, 5}, cell{5, 5}, 39, 5, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := anchorAt(c.anchor.row, c.anchor.col)
			s.drag(c.m1.row, c.m1.col)
			s.drag(c.m2.row, c.m2.col)
			s.extendToEdge(c.maxRow)
			if s.cursor.row != c.wantRow || s.cursor.col != c.wantCol {
				t.Errorf("extendToEdge -> cursor %v, want {%d %d}", s.cursor, c.wantRow, c.wantCol)
			}
			// The anchor is where the drag actually started; only the
			// cursor end should move.
			if s.anchor != c.anchor {
				t.Errorf("extendToEdge moved the anchor to %v, want it to stay at %v", s.anchor, c.anchor)
			}
		})
	}
}

func TestSelectionContainsMultiLine(t *testing.T) {
	s := anchorAt(2, 5)
	s.drag(4, 10)
	cases := []struct {
		row, col int
		want     bool
	}{
		{2, 4, false},
		{2, 5, true},
		{2, 79, true},
		{3, 0, true},
		{3, 79, true},
		{4, 0, true},
		{4, 10, true},
		{4, 11, false},
		{1, 10, false},
		{5, 10, false},
	}
	for _, c := range cases {
		if got := s.contains(c.row, c.col); got != c.want {
			t.Errorf("contains(%d,%d) = %v, want %v", c.row, c.col, got, c.want)
		}
	}
}

// --- frame()'s hit map ---

// TestFrameHitsMatchLinesLength pins the one invariant Update's mouse handling
// depends on: hits is exactly as long as lines, on every tab and at several
// widths and heights, so indexing hits by mouse Y is always safe.
func TestFrameHitsMatchLinesLength(t *testing.T) {
	for _, w := range []int{60, 80, 120, 200} {
		for _, h := range []int{16, 24, 40} {
			for _, tb := range []tab{tabLocal, tabPRs, tabIssues, tabConfig} {
				m := seededModel()
				m.width, m.height = w, h
				m.switchTab(tb)
				lines, hits := m.frame()
				if len(hits) != len(lines) {
					t.Errorf("tab=%d w=%d h=%d: %d hits, want %d (one per line)", tb, w, h, len(hits), len(lines))
				}
			}

			mb := seededModel()
			mb.width, mb.height = w, h
			mb.hasBeads = true
			mb.beads = []bd.Bead{{ID: "bdtest-1", Title: "a bead", Status: "open"}}
			mb.switchTab(tabBeads)
			lines, hits := mb.frame()
			if len(hits) != len(lines) {
				t.Errorf("tab=Beads w=%d h=%d: %d hits, want %d", w, h, len(hits), len(lines))
			}
		}
	}
}

// TestFrameHitsMatchLinesLengthWithOverlaysOpen covers the paths renderBody
// dispatches to besides the plain tab bodies: every one of them must still
// hand back a hits slice the same length as its lines, even though every
// entry in it is hitNone.
func TestFrameHitsMatchLinesLengthWithOverlaysOpen(t *testing.T) {
	base := func() Model {
		m := seededModel()
		m.width, m.height = 120, 40
		return m
	}
	check := func(label string, m Model) {
		t.Helper()
		lines, hits := m.frame()
		if len(hits) != len(lines) {
			t.Errorf("%s: %d hits, want %d", label, len(hits), len(lines))
		}
	}

	m := base()
	m.errOpen, m.errTitle, m.errDetail = true, "refresh failed", "some detail\nmore detail"
	check("errOpen", m)

	m = base()
	m.hasBeads = true
	m.beads = []bd.Bead{{ID: "bdtest-1", Title: "a bead", Status: "open"}}
	m.active = tabBeads
	m.beadOpen = true
	check("beadOpen", m)

	m = base()
	m.confirming = true
	m.confirmTargets = []cleanupTarget{{id: "br:old-merged", kind: cleanupBranch, branch: "old-merged"}}
	check("confirming", m)

	m = base()
	m.deleting = true
	target := cleanupTarget{id: "br:old-merged", kind: cleanupBranch, branch: "old-merged"}
	m.deleteTargets = []cleanupTarget{target}
	m.deleteResults = []deleteResult{{target: target, status: stRunning}}
	check("deleting", m)
}

// TestClickResolvesToRenderedRow pins the hit map against the real renderer on
// a flat-list tab: whatever line PR #10 actually drew on, the hit map for that
// line resolves to PR #10's row — not to a row index recomputed independently.
func TestClickResolvesToRenderedRow(t *testing.T) {
	m := seededModel()
	m.active = tabPRs
	lines, hits := m.frame()
	y := -1
	for i, l := range lines {
		if strings.Contains(l, "#10") {
			y = i
			break
		}
	}
	if y == -1 {
		t.Fatal("PR #10's row not found in the rendered frame")
	}
	h := hits[y]
	if h.kind != hitRow || h.row < 0 {
		t.Fatalf("hit at PR #10's line is %+v, want a resolved row", h)
	}
	rows := m.visibleRows()
	if h.row >= len(rows) || rows[h.row].prNum != 10 {
		t.Fatalf("hit resolved to row %d, want PR #10 (got %+v)", h.row, rows[h.row])
	}
}

// TestClickResolvesToRenderedLocalRow is the same guarantee on the Local tab's
// split body: the line old-merged actually drew on resolves, on its left
// (Branches) side, to old-merged's own row in branchRows().
func TestClickResolvesToRenderedLocalRow(t *testing.T) {
	m := seededModel()
	m.active = tabLocal
	lines, hits := m.frame()
	y := -1
	for i, l := range lines {
		if strings.Contains(l, "old-merged") {
			y = i
			break
		}
	}
	if y == -1 {
		t.Fatal("old-merged not found in the rendered frame")
	}
	h := hits[y]
	if h.kind != hitRow || !h.split {
		t.Fatalf("hit at old-merged's line is %+v, want a split Local-tab row", h)
	}
	if h.pane != paneBranches || h.row < 0 {
		t.Fatalf("hit's left side is %+v, want Branches with a resolved row", h)
	}
	rows := m.branchRows()
	if rows[h.row].name != "old-merged" {
		t.Fatalf("hit resolved to row %d (%q), want old-merged", h.row, rows[h.row].name)
	}
}

// TestClickOnTabBarSwitchesTab exercises the tab bar's per-tab x ranges
// end to end through Update, not just the hit map.
func TestClickOnTabBarSwitchesTab(t *testing.T) {
	m := seededModel()
	_, hits := m.frame()
	h := hits[1] // header, then the tab bar
	if h.kind != hitTabBar {
		t.Fatalf("line 1 is %+v, want the tab bar", h)
	}
	var prTab tabHit
	found := false
	for _, th := range h.tabs {
		if th.tab == tabPRs {
			prTab, found = th, true
		}
	}
	if !found {
		t.Fatal("PRs tab not on the bar in the seeded fixture")
	}

	next, _ := m.Update(tea.MouseMsg{Y: 1, X: prTab.x0, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMsg{Y: 1, X: prTab.x0, Action: tea.MouseActionRelease})
	m = next.(Model)
	if m.active != tabPRs {
		t.Errorf("active tab = %v, want tabPRs after clicking its tab-bar segment", m.active)
	}
}

// TestClickToActMovesThenActivatesOnSecondClick is the click-to-act contract
// end to end: the first click on a row only moves the cursor/focus there, and
// only a second click on the row already under the cursor activates it.
func TestClickToActMovesThenActivatesOnSecondClick(t *testing.T) {
	m := seededModel()
	m.active = tabLocal
	lines, hits := m.frame()
	y := -1
	for i, l := range lines {
		if strings.Contains(l, "abc123") { // the seeded fixture's one commit
			y = i
			break
		}
	}
	if y == -1 {
		t.Fatal("commit row not found in the rendered frame")
	}
	h := hits[y]
	if !h.split || h.pane2 != paneCommits || h.row2 != 0 {
		t.Fatalf("hit at the commit row is %+v, want Commits row 0 on its right side", h)
	}
	x := h.splitX + 3 // past the gap and the box's own left border

	next, cmd := m.Update(tea.MouseMsg{Y: y, X: x, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(Model)
	next, cmd = m.Update(tea.MouseMsg{Y: y, X: x, Action: tea.MouseActionRelease})
	m = next.(Model)
	if cmd != nil {
		t.Error("the first click activated the row instead of just moving focus onto it")
	}
	if m.pane != paneCommits || m.paneCursor[paneCommits] != 0 {
		t.Fatalf("first click did not focus Commits row 0: pane=%v cursor=%d", m.pane, m.paneCursor[paneCommits])
	}

	next, cmd = m.Update(tea.MouseMsg{Y: y, X: x, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(Model)
	next, cmd = m.Update(tea.MouseMsg{Y: y, X: x, Action: tea.MouseActionRelease})
	m = next.(Model)
	if cmd == nil {
		t.Error("a second click on the row already under the cursor did not activate it")
	}
}

// TestClickOnLocalGutterColumnIsNoOp pins the one-cell gap zipColumns joins
// Branches to Commits/Stashes with: a click exactly on it must move nothing,
// not fall through to whichever pane happens to sit on its right.
func TestClickOnLocalGutterColumnIsNoOp(t *testing.T) {
	m := seededModel()
	m.active = tabLocal
	_, hits := m.frame()
	y := -1
	var h hitTarget
	for i, hh := range hits {
		// Pick a row with a real row on its right side (row2 >= 0) — the case
		// the bug actually mismisclicked into; a border/blank right side would
		// pass the test whether or not the split resolved correctly.
		if hh.kind == hitRow && hh.split && hh.row2 >= 0 {
			y, h = i, hh
			break
		}
	}
	if y == -1 {
		t.Fatal("no split Local-tab row with a resolvable right-side row found in the rendered frame")
	}
	beforePane, beforeCursor := m.pane, m.paneCursor

	next, _ := m.Update(tea.MouseMsg{Y: y, X: h.splitX, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMsg{Y: y, X: h.splitX, Action: tea.MouseActionRelease})
	m = next.(Model)

	if m.pane != beforePane || m.paneCursor != beforeCursor {
		t.Errorf("click at the gutter column x=%d moved focus (pane=%v cursor=%v), want no-op",
			h.splitX, m.pane, m.paneCursor)
	}
}

// TestSwitchTabClearsInProgressSelection pins the fix for a drag surviving a
// tab switch: a held mouse button and a keypress are independent input
// sources, so pressing → mid-drag must not leave a selection whose row/col
// address the tab that was just left.
func TestSwitchTabClearsInProgressSelection(t *testing.T) {
	m := seededModel()
	m.active = tabLocal
	m.sel = anchorAt(5, 5)
	m.sel.drag(8, 20)
	if !m.sel.active() {
		t.Fatal("fixture drag never became active")
	}

	m.switchTab(tabPRs)

	if m.sel.active() {
		t.Errorf("selection still active after switchTab: %+v", m.sel)
	}
}

// TestSelWatchdogFinishesAGestureThatNeverGotAReleaseEvent pins the fix for
// the drag-to-copy freeze/stuck-highlight bug: cell-motion mouse reporting
// stops emitting events entirely once the cursor leaves the terminal's grid,
// so a drag that walks off-window gets no more motion and no eventual
// release — nothing in the input stream ever tells the app the gesture
// ended. The watchdog is the only thing that can end it, and it ends it the
// way a real release would: sel drops back to its zero value and the
// covered text reaches pbcopy.
func TestSelWatchdogFinishesAGestureThatNeverGotAReleaseEvent(t *testing.T) {
	m := seededModel()
	m.active = tabLocal

	var gotText string
	old := pbcopy
	pbcopy = func(text string) error { gotText = text; return nil }
	defer func() { pbcopy = old }()

	next, _ := m.Update(tea.MouseMsg{Y: 5, X: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMsg{Y: 8, X: 20, Action: tea.MouseActionMotion})
	m = next.(Model)
	if !m.sel.active() {
		t.Fatal("fixture drag never became active")
	}
	gen := m.selGen

	// No further mouse event ever arrives — the cursor left the window. The
	// watchdog scheduled by the last motion is the only cmd left in flight;
	// simulate it firing.
	next, _ = m.Update(selWatchdogMsg{gen: gen})
	m = next.(Model)

	if m.sel.inProgress() {
		t.Errorf("selection still in progress after its watchdog fired: %+v", m.sel)
	}
	if gotText == "" {
		t.Error("watchdog resolved the gesture but never called pbcopy")
	}
}

// TestSelWatchdogIgnoresAStaleGenAndLeavesALiveSelectionAlone makes sure the
// self-heal doesn't cut off a selection that's still genuinely being
// dragged: an earlier motion's watchdog firing after a newer motion has
// already rearmed it must be a no-op.
func TestSelWatchdogIgnoresAStaleGenAndLeavesALiveSelectionAlone(t *testing.T) {
	m := seededModel()
	m.active = tabLocal

	next, _ := m.Update(tea.MouseMsg{Y: 5, X: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(Model)
	staleGen := m.selGen

	next, _ = m.Update(tea.MouseMsg{Y: 6, X: 6, Action: tea.MouseActionMotion})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMsg{Y: 8, X: 20, Action: tea.MouseActionMotion})
	m = next.(Model)
	if !m.sel.active() {
		t.Fatal("fixture drag never became active")
	}

	// The watchdog from the very first (now superseded) motion fires late.
	next, _ = m.Update(selWatchdogMsg{gen: staleGen})
	m = next.(Model)

	if !m.sel.active() {
		t.Error("a stale watchdog cleared a selection that is still being actively dragged")
	}
}

// TestSelWatchdogExtendsToTheFrameEdgeInTheLastDragDirection covers the part
// of the self-heal that goes beyond just not losing the gesture: since the
// real cursor position when the drag walked off-window is unknowable, the
// watchdog's best guess is "wherever the last motion sample was heading",
// pushed all the way to that edge of the frame — not just wherever the last
// reported cell happened to be.
func TestSelWatchdogExtendsToTheFrameEdgeInTheLastDragDirection(t *testing.T) {
	m := seededModel()
	m.active = tabLocal
	lines, _ := m.frameRaw()
	lastRow := len(lines) - 1
	if lastRow < 2 {
		t.Fatal("fixture frame too short for this test")
	}

	var gotText string
	old := pbcopy
	pbcopy = func(text string) error { gotText = text; return nil }
	defer func() { pbcopy = old }()

	// Drag starts at row 1 col 0 and its last two motion samples move both
	// down and right (row 1 -> row 2, col 0 -> col 3), heading toward the
	// bottom-right corner of the frame.
	next, _ := m.Update(tea.MouseMsg{Y: 1, X: 0, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMsg{Y: 2, X: 3, Action: tea.MouseActionMotion})
	m = next.(Model)
	gen := m.selGen

	next, _ = m.Update(selWatchdogMsg{gen: gen})
	m = next.(Model)

	if gotText == "" {
		t.Fatal("watchdog never resolved the gesture; nothing was copied")
	}
	// The expected final reach — anchor where the drag started, cursor pinned
	// to the frame's last row at the far-right sentinel column — is asserted
	// directly against extractSelection rather than against extendToEdge, so
	// this stays a check on where the gesture actually landed rather than a
	// restatement of extendToEdge's own logic. Extracted against a fresh,
	// untouched model rather than the mutated m above: the real gesture's
	// copy already set the "copied to clipboard" status, which changes the
	// rendered frame (an extra status/keybar line) and would otherwise shift
	// what row lastRow lands on out from under this comparison.
	fresh := seededModel()
	fresh.active = tabLocal
	want := fresh.extractSelection(selection{
		anchor: cell{row: 1, col: 0},
		cursor: cell{row: lastRow, col: colEdge},
		phase:  selDragging,
	})
	if gotText != want {
		t.Errorf("copied text stopped at the last reported row instead of extending to the frame's bottom edge (row %d)\ngot:  %q\nwant: %q", lastRow, gotText, want)
	}
}

// --- highlight and extraction ---

// TestHighlightSplicePreservesWidth pins the cell-width guarantee: painting a
// selected range must never change how many cells the line occupies, since
// ansi.Cut/StringWidth is what the whole splice is built on.
func TestHighlightSplicePreservesWidth(t *testing.T) {
	m := seededModel()
	m.active = tabPRs
	lines, _ := m.frameRaw()
	y := -1
	for i, l := range lines {
		if strings.Contains(l, "#10") {
			y = i
			break
		}
	}
	if y == -1 {
		t.Fatal("fixture line not found")
	}
	before := ansi.StringWidth(lines[y])
	m.sel = selection{anchor: cell{row: y, col: 2}, cursor: cell{row: y, col: 10}, phase: selDragging}

	highlighted := m.applyHighlight(lines)
	after := ansi.StringWidth(highlighted[y])
	if after != before {
		t.Errorf("highlighted line is %d cells wide, want %d (the unhighlighted width)", after, before)
	}
	if highlighted[y] == lines[y] {
		t.Error("applyHighlight left the selected line byte-for-byte unchanged")
	}
}

// TestExtractSelectionStripsEscapes pins the copy payload: a selection over a
// styled (colored) row extracts as plain text with no escape bytes at all.
func TestExtractSelectionStripsEscapes(t *testing.T) {
	m := seededModel()
	m.active = tabPRs
	lines, _ := m.frameRaw()
	y := -1
	for i, l := range lines {
		if strings.Contains(l, "#10") {
			y = i
			break
		}
	}
	if y == -1 {
		t.Fatal("fixture line not found")
	}
	w := ansi.StringWidth(lines[y])
	sel := selection{anchor: cell{row: y, col: 0}, cursor: cell{row: y, col: w - 1}, phase: selDragging}

	got := m.extractSelection(sel)
	if strings.Contains(got, "\x1b") {
		t.Errorf("extracted text still carries an escape byte: %q", got)
	}
	if !strings.Contains(got, "#10") {
		t.Errorf("extracted text is %q, want it to contain #10", got)
	}
}

// TestDragReleaseCopiesViaPbcopyVar is the drag-to-copy gesture end to end,
// with pbcopy swapped so the test never shells out: press, drag onto a second
// cell, release copies the covered text, shows the toast, and drops the
// selection in the same Update so nothing persists into the next frame.
func TestDragReleaseCopiesViaPbcopyVar(t *testing.T) {
	m := seededModel()
	m.active = tabPRs
	lines, _ := m.frame()
	y := -1
	for i, l := range lines {
		if strings.Contains(l, "#10") {
			y = i
			break
		}
	}
	if y == -1 {
		t.Fatal("fixture line not found")
	}

	var gotText string
	old := pbcopy
	pbcopy = func(text string) error { gotText = text; return nil }
	defer func() { pbcopy = old }()

	next, _ := m.Update(tea.MouseMsg{Y: y, X: 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMsg{Y: y, X: 10, Action: tea.MouseActionMotion})
	m = next.(Model)
	next, cmd := m.Update(tea.MouseMsg{Y: y, X: 10, Action: tea.MouseActionRelease})
	m = next.(Model)

	if gotText == "" {
		t.Fatalf("pbcopy was never called; status=%q", m.status)
	}
	if !strings.Contains(gotText, "#10") {
		t.Errorf("copied text is %q, want it to contain #10", gotText)
	}
	if m.status != "copied to clipboard" {
		t.Errorf("status = %q, want the copy toast", m.status)
	}
	if m.sel.active() {
		t.Error("the selection is still active after mouse-up — the highlight must not persist")
	}
	if cmd == nil {
		t.Error("no toast-expiry command was scheduled")
	}
}
