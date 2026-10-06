package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
)

// beadOverlayModel is a model sitting on the Beads tab with the cursor on one
// bead and the overlay up.
func beadOverlayModel(sel string, beads []bd.Bead) Model {
	m := seededModel()
	m.haveBeads = true
	m.hasBeads = true
	m.beadPrefix = "bdtest"
	m.beads = beads
	m.active = tabBeads
	m.beadOpen = true
	for i, r := range m.visibleRows() {
		if r.kind == rowBead && r.beadID == sel {
			m.cursor[tabBeads] = i
		}
	}
	return m
}

// chainBeads is a → b → c: c waits on b, b waits on a.
func chainBeads() []bd.Bead {
	return []bd.Bead{
		{ID: "bdtest-a", Title: "First", Status: "open", Type: "task", Priority: 1},
		{ID: "bdtest-b", Title: "Second", Status: "open", Type: "task", Priority: 1, Blockers: []string{"bdtest-a"}},
		{ID: "bdtest-c", Title: "Third", Status: "open", Type: "task", Priority: 1, Blockers: []string{"bdtest-b"}},
	}
}

// TestBeadNeighbourhoodWalksBothWays: the graph is what this bead waits on and
// what waits on it, both transitively. Selected in the middle of a chain, that is
// the whole chain; a bead with no links is a graph of one.
func TestBeadNeighbourhoodWalksBothWays(t *testing.T) {
	beads := chainBeads()
	got := beadNeighbourhood(beads, "bdtest-b")
	var ids []string
	for _, b := range got {
		ids = append(ids, b.ID)
	}
	if strings.Join(ids, ",") != "bdtest-a,bdtest-b,bdtest-c" {
		t.Errorf("neighbourhood of the middle of a chain is %v, want all three", ids)
	}

	lone := append(beads, bd.Bead{ID: "bdtest-z", Title: "Alone", Status: "open"})
	if got := beadNeighbourhood(lone, "bdtest-z"); len(got) != 1 || got[0].ID != "bdtest-z" {
		t.Errorf("an unlinked bead's neighbourhood is %v, want just itself", got)
	}

	// A blocker that has been closed is gone from the list, so it is gone from
	// the graph too — the same rule the Blocked section reads by.
	orphan := []bd.Bead{{ID: "bdtest-c", Title: "Third", Status: "open", Blockers: []string{"bdtest-gone"}}}
	if got := beadNeighbourhood(orphan, "bdtest-c"); len(got) != 1 {
		t.Errorf("a closed blocker put %d beads in the graph, want 1", len(got))
	}
}

// TestBeadGraphJunctions pins two drawing rules: two edges leaving the same
// box merge into the junction they form rather than overwriting each other,
// and the arrowheads survive the box paint because they are drawn after it.
func TestBeadGraphJunctions(t *testing.T) {
	beads := []bd.Bead{
		{ID: "bdtest-p", Title: "Parent", Status: "open", Type: "task"},
		{ID: "bdtest-a", Title: "Child A", Status: "open", Type: "task", Blockers: []string{"bdtest-p"}},
		{ID: "bdtest-b", Title: "Child B", Status: "open", Type: "task", Blockers: []string{"bdtest-p"}},
	}
	l := beadGraphLayoutFor(beadNeighbourhood(beads, "bdtest-p"), "bdtest-p", 120, beadGeomFull)
	if l.ranks != 2 || len(l.nodes) != 3 {
		t.Fatalf("fork placed as %d ranks over %d nodes, want 2 and 3", l.ranks, len(l.nodes))
	}
	var text string
	for _, row := range l.render(120, l.rows, "bdtest") {
		text += segsText(row) + "\n"
	}

	// One edge leaves the parent level and one turns down. Both claim the same
	// channel cell: merged they are a ┬, overwritten they are a ┐ or a ─, and a
	// stray vertical would make it a ┼ — an edge appearing to pass through a box.
	if !strings.Contains(text, "┬") {
		t.Errorf("the fork drew no ┬ — the second corner overwrote the first:\n%s", text)
	}
	if strings.Contains(text, "┼") {
		t.Errorf("the fork drew a ┼, which reads as an edge passing through a box:\n%s", text)
	}
	if n := strings.Count(text, "▸"); n != 2 {
		t.Errorf("the graph has %d arrowheads, want one per edge (2) — the box paint ate one:\n%s", n, text)
	}
}

// TestBeadGraphCompactFallback pins the fallback this issue exists for: a
// neighbourhood with a rank too tall to fit at full geometry still draws once
// retried at compact, and the arrowhead lands on the row the target's bare id
// is actually printed on.
func TestBeadGraphCompactFallback(t *testing.T) {
	beads := []bd.Bead{
		{ID: "bdtest-p", Title: "Parent", Status: "open", Type: "task"},
	}
	for i := 0; i < 5; i++ {
		beads = append(beads, bd.Bead{
			ID: "bdtest-c" + string(rune('a'+i)), Title: "Child", Status: "open",
			Type: "task", Blockers: []string{"bdtest-p"},
		})
	}
	nodes := beadNeighbourhood(beads, "bdtest-p")
	w, h := 120, 15

	full := beadGraphLayoutFor(nodes, "bdtest-p", w, beadGeomFull)
	if why := full.refusal(w, h); why == "" {
		t.Fatalf("full geometry fit a 5-child fan in a %d-row box, want a refusal to force the compact retry", h)
	}

	compact := beadGraphLayoutFor(nodes, "bdtest-p", w, beadGeomCompact)
	if why := compact.refusal(w, h); why != "" {
		t.Fatalf("compact geometry still refused the same fan: %s", why)
	}

	var lines []string
	for _, row := range compact.render(w, compact.rows, "bdtest") {
		lines = append(lines, segsText(row))
	}
	child := compact.byID["bdtest-ca"]
	line := lines[child.y]
	if !strings.Contains(line, "▸") {
		t.Errorf("compact node row %d is %q, want the incoming arrowhead on the same row as the id", child.y, line)
	}
	if !strings.Contains(line, shortBeadID("bdtest-ca", "bdtest")) {
		t.Errorf("compact node row %d is %q, want the bare id on it", child.y, line)
	}
}

// TestBeadOverlayHoldsItsSize is the whole point of the feature: the box is sized
// from the terminal, never from what it holds, so switching between any of the
// three tabs does not move the screen.
func TestBeadOverlayHoldsItsSize(t *testing.T) {
	for _, size := range [][2]int{{200, 50}, {120, 40}, {80, 24}} {
		w, h := size[0], size[1]
		m := beadOverlayModel("bdtest-b", chainBeads())
		m.width, m.height = w, h
		m.beads[1].Description = strings.Repeat("a long description that wraps. ", 40)

		bead := beadBox(m.renderBeadOverlay(w, h))
		m.beadTab = beadTabGraph
		graph := beadBox(m.renderBeadOverlay(w, h))
		m.beadTab = beadTabBlocking
		blocking := beadBox(m.renderBeadOverlay(w, h))

		if len(bead) != len(graph) || len(bead) != len(blocking) {
			t.Errorf("%dx%d: the box is %d rows on the bead, %d on the graph, %d on blocking", w, h, len(bead), len(graph), len(blocking))
			continue
		}
		if len(bead) == 0 {
			t.Fatalf("%dx%d: the overlay drew nothing", w, h)
		}
		if bw, gw, kw := vlen(bead[0]), vlen(graph[0]), vlen(blocking[0]); bw != gw || bw != kw {
			t.Errorf("%dx%d: the box is %d cells on the bead, %d on the graph, %d on blocking", w, h, bw, gw, kw)
		}
	}
}

// beadBox is the non-empty rows of a rendered overlay frame — the box itself,
// with the blank rows above and below it dropped.
func beadBox(frame []string) []string {
	var out []string
	for _, line := range frame {
		if strings.TrimSpace(line) != "" {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// TestBeadOverlaySwallowsKeys: while the box is up it owns the keyboard. A key
// that reached the list would move the cursor off the bead the box is describing.
func TestBeadOverlaySwallowsKeys(t *testing.T) {
	m := beadOverlayModel("bdtest-b", chainBeads())
	before := m.cursor[tabBeads]

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	after := next.(Model)
	if after.cursor[tabBeads] != before {
		t.Errorf("j moved the list cursor from %d to %d while the overlay was up", before, after.cursor[tabBeads])
	}
	if !after.beadOpen {
		t.Error("j closed the overlay")
	}

	// ← → cycle the three tabs, esc closes; both are the overlay's, not the
	// dashboard's.
	switched, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRight})
	if sm := switched.(Model); sm.beadTab != beadTabGraph || sm.active != tabBeads {
		t.Errorf("→ left tab=%v active=%v, want the graph on the Beads tab", sm.beadTab, sm.active)
	}
	twice, _ := switched.(Model).handleKey(tea.KeyMsg{Type: tea.KeyRight})
	if sm := twice.(Model); sm.beadTab != beadTabBlocking {
		t.Errorf("→ → left tab=%v, want Blocking", sm.beadTab)
	}
	wrapped, _ := twice.(Model).handleKey(tea.KeyMsg{Type: tea.KeyRight})
	if sm := wrapped.(Model); sm.beadTab != beadTabDetail {
		t.Errorf("→ → → left tab=%v, want the cycle to wrap back to Detail", sm.beadTab)
	}
	back, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyLeft})
	if sm := back.(Model); sm.beadTab != beadTabBlocking {
		t.Errorf("← from Detail left tab=%v, want it to land on Blocking", sm.beadTab)
	}
	closed, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if closed.(Model).beadOpen {
		t.Error("esc did not close the overlay")
	}
}

// TestBeadOverlayTabResetsScroll: switching tabs is switching content, so a
// scroll position from one tab means nothing on another.
func TestBeadOverlayTabResetsScroll(t *testing.T) {
	m := beadOverlayModel("bdtest-b", chainBeads())
	m.beadScroll = 9

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRight})
	if got := next.(Model).beadScroll; got != 0 {
		t.Errorf("→ left beadScroll=%d, want 0", got)
	}
}

// TestBeadBlockingLinesDerivesDependents: the Blocking tab lists exactly the
// beads whose Blockers contain the selected bead's id, not the whole workspace.
func TestBeadBlockingLinesDerivesDependents(t *testing.T) {
	m := beadOverlayModel("bdtest-b", chainBeads())
	b, ok := m.beadUnderCursor()
	if !ok {
		t.Fatal("no bead under the cursor")
	}
	lines := m.beadBlockingLines(b, 80)
	var text string
	for _, l := range lines {
		text += segsText(l) + "\n"
	}
	if !strings.Contains(text, "Third") {
		t.Errorf("blocking lines for bdtest-b are %q, want bdtest-c's title Third (it waits on b)", text)
	}
	if strings.Contains(text, "First") {
		t.Errorf("blocking lines for bdtest-b are %q, want bdtest-a's title First excluded (it is what b waits on, not what waits on b)", text)
	}
}

// TestBeadBlockingLinesEmpty: a bead nothing waits on says so rather than
// drawing an empty box.
func TestBeadBlockingLinesEmpty(t *testing.T) {
	m := beadOverlayModel("bdtest-c", chainBeads())
	b, ok := m.beadUnderCursor()
	if !ok {
		t.Fatal("no bead under the cursor")
	}
	lines := m.beadBlockingLines(b, 80)
	var text string
	for _, l := range lines {
		text += segsText(l) + "\n"
	}
	if !strings.Contains(text, "nothing waits on this bead") {
		t.Errorf("blocking lines for bdtest-c (nothing waits on it) are %q, want the empty-state message", text)
	}
}

// TestBeadOverlayRefusesTheGraphItCannotDraw: a neighbourhood too tall for the
// box is refused with the numbers on both sides, not drawn half-way. This state
// is reachable in a real workspace and is expected to appear.
func TestBeadOverlayRefusesTheGraphItCannotDraw(t *testing.T) {
	beads := []bd.Bead{{ID: "bdtest-p", Title: "Parent", Status: "open"}}
	for i := 0; i < 20; i++ {
		beads = append(beads, bd.Bead{
			ID: "bdtest-c" + string(rune('a'+i)), Title: "Child", Status: "open",
			Blockers: []string{"bdtest-p"},
		})
	}
	m := beadOverlayModel("bdtest-p", beads)
	m.beadTab = beadTabGraph
	m.width, m.height = 200, 40
	text := strings.Join(m.renderBeadOverlay(200, 40), "\n")
	if !strings.Contains(text, "21 beads over 2 ranks") || !strings.Contains(text, "rows, the box has") {
		t.Errorf("the refusal does not name the dimension that ran out:\n%s", text)
	}
	if strings.Contains(text, "┌───") {
		t.Errorf("a refused graph still drew node boxes:\n%s", text)
	}
}
