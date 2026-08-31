// work_submenu_test.go pins admin-n5vg: on the Work tab, right opens a
// collapsed parent's submenu, left/esc close it from the parent or any of
// its target rows (cursor landing back on the parent), and h/l stay pure
// tab-switching everywhere — including on a target row, where left/esc
// diverge from it. Every other row kind, and every other tab, keeps the
// left/right-switches-tab behavior unchanged.
package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// rowIndex finds the row index matching pred in m's visible Work rows, or
// fails the test.
func rowIndex(t *testing.T, m Model, pred func(rowItem) bool) int {
	t.Helper()
	for i, r := range m.visibleRows() {
		if pred(r) {
			return i
		}
	}
	t.Fatalf("no matching row in %v", m.visibleRows())
	return -1
}

func isCmdRow(name string) func(rowItem) bool {
	return func(r rowItem) bool {
		return r.kind == rowCommand && r.work != nil && r.work.cmdName == name
	}
}

func isTargetRow(cmdName, key string) func(rowItem) bool {
	return func(r rowItem) bool {
		return r.kind == rowCommandTarget && r.work != nil && r.work.cmdName == cmdName && r.work.targetKey == key
	}
}

// TestRightExpandsCollapsedParentRow: right on a collapsed command-with-targets
// row opens its submenu and does not change tab.
func TestRightExpandsCollapsedParentRow(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("dev"))

	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRight})
	if cmd != nil {
		t.Errorf("expanding a submenu returned a non-nil Cmd, want nil")
	}
	nm := next.(Model)
	if nm.expandedCmd != "dev" {
		t.Errorf("expandedCmd = %q, want \"dev\"", nm.expandedCmd)
	}
	if nm.active != tabWork {
		t.Errorf("active tab changed to %v, want it to stay on Work", nm.active)
	}
}

// TestLeftCollapsesExpandedParentRow: left on an expanded parent row closes
// it and does not change tab.
func TestLeftCollapsesExpandedParentRow(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.expandedCmd = "dev"
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("dev"))

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyLeft})
	nm := next.(Model)
	if nm.expandedCmd != "" {
		t.Errorf("expandedCmd = %q, want empty after collapsing", nm.expandedCmd)
	}
	if nm.active != tabWork {
		t.Errorf("active tab changed to %v, want it to stay on Work", nm.active)
	}
}

// TestLeftOnTargetRowCollapsesAndFocusesParent: left on a target row closes
// the submenu and leaves the cursor on the parent row, since the target row
// the cursor was on no longer exists.
func TestLeftOnTargetRowCollapsesAndFocusesParent(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.expandedCmd = "dev"
	m.cursor[tabWork] = rowIndex(t, m, isTargetRow("dev", "mac"))

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyLeft})
	nm := next.(Model)
	if nm.expandedCmd != "" {
		t.Errorf("expandedCmd = %q, want empty after collapsing from a target row", nm.expandedCmd)
	}
	cur := nm.visibleRows()[nm.cursor[tabWork]]
	if cur.kind != rowCommand || cur.work.cmdName != "dev" {
		t.Fatalf("cursor landed on %v/%v, want the dev command row", cur.kind, cur.work)
	}
}

// TestEscOnTargetRowCollapsesAndFocusesParent proves esc does exactly what
// left does from a target row.
func TestEscOnTargetRowCollapsesAndFocusesParent(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.expandedCmd = "dev"
	m.cursor[tabWork] = rowIndex(t, m, isTargetRow("dev", "tv"))

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	nm := next.(Model)
	if nm.expandedCmd != "" {
		t.Errorf("expandedCmd = %q, want empty after esc from a target row", nm.expandedCmd)
	}
	cur := nm.visibleRows()[nm.cursor[tabWork]]
	if cur.kind != rowCommand || cur.work.cmdName != "dev" {
		t.Fatalf("cursor landed on %v/%v, want the dev command row", cur.kind, cur.work)
	}
}

// TestEscOnParentRowCollapses proves esc from the parent row itself also
// collapses, matching left.
func TestEscOnParentRowCollapses(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.expandedCmd = "dev"
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("dev"))

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	nm := next.(Model)
	if nm.expandedCmd != "" {
		t.Errorf("expandedCmd = %q, want empty after esc on the parent row", nm.expandedCmd)
	}
}

// TestRightOnRowWithNoSubmenuSwitchesTab: a plain command (no targets) has
// no submenu, so right falls through to ordinary tab-switching.
func TestRightOnRowWithNoSubmenuSwitchesTab(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("test"))

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRight})
	nm := next.(Model)
	if nm.active == tabWork {
		t.Errorf("active tab stayed on Work, want right on a plain command row to switch tab")
	}
}

// TestLeftOnRowWithNoSubmenuSwitchesTab mirrors the above for left.
func TestLeftOnRowWithNoSubmenuSwitchesTab(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("test"))

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyLeft})
	nm := next.(Model)
	if nm.active == tabWork {
		t.Errorf("active tab stayed on Work, want left on a plain command row to switch tab")
	}
}

// TestEscOnRowWithNoSubmenuIsANoOp: esc has no binding outside a submenu, so
// it must neither switch tab nor touch expandedCmd.
func TestEscOnRowWithNoSubmenuIsANoOp(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("test"))

	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Errorf("esc with no submenu returned a non-nil Cmd, want nil")
	}
	nm := next.(Model)
	if nm.active != tabWork {
		t.Errorf("active tab changed to %v, want esc to be a no-op off a submenu", nm.active)
	}
}

// TestHAndLStaySwitchingTabOnTargetRow: h/l are pure tab-switching on every
// row, including a target row where the arrow keys instead collapse.
func TestHAndLStaySwitchingTabOnTargetRow(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.expandedCmd = "dev"
	m.cursor[tabWork] = rowIndex(t, m, isTargetRow("dev", "mac"))

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	nm := next.(Model)
	if nm.active == tabWork {
		t.Errorf("active tab stayed on Work, want h on a target row to switch tab")
	}
	if nm.expandedCmd != "dev" {
		t.Errorf("expandedCmd = %q, want it left untouched by h", nm.expandedCmd)
	}

	m2 := multiTargetModel(t)
	m2.active = tabWork
	m2.expandedCmd = "dev"
	m2.cursor[tabWork] = rowIndex(t, m2, isTargetRow("dev", "mac"))
	next2, _ := m2.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	nm2 := next2.(Model)
	if nm2.active == tabWork {
		t.Errorf("active tab stayed on Work, want l on a target row to switch tab")
	}
	if nm2.expandedCmd != "dev" {
		t.Errorf("expandedCmd = %q, want it left untouched by l", nm2.expandedCmd)
	}
}

// TestLeftRightStillSwitchTabOnEveryOtherTab proves the Work-tab-only carve
// out does not touch left/right anywhere else — the Beads tab stands in for
// every other tab.
func TestLeftRightStillSwitchTabOnEveryOtherTab(t *testing.T) {
	m := workTestModel(&manifest.Manifest{})
	m.active = tabBeads

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRight})
	if next.(Model).active == tabBeads {
		t.Errorf("right on the Beads tab left active unchanged, want it to switch tab")
	}

	m2 := workTestModel(&manifest.Manifest{})
	m2.active = tabBeads
	next2, _ := m2.handleKey(tea.KeyMsg{Type: tea.KeyLeft})
	if next2.(Model).active == tabBeads {
		t.Errorf("left on the Beads tab left active unchanged, want it to switch tab")
	}
}

// TestWorkKeybarShowsSubmenuHint asserts the Work tab's rendered keybar
// carries the submenu hint the prototype adds, through the real headless
// frame renderer (View()).
func TestWorkKeybarShowsSubmenuHint(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.width, m.height = 120, 40

	out := stripANSI(m.View())
	mustContainAll(t, "Work tab keybar", out, "submenu")
}
