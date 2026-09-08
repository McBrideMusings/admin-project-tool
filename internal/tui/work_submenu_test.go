// work_submenu_test.go pins admin-0kxh: on the Work tab, a command's targets
// always render beneath it — there is no collapse/expand toggle — and
// left/right/h/l stay pure tab-switching everywhere, including on a target
// row. esc is a no-op on the Work tab, matching every other tab.
package tui

import (
	"strings"
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

// TestRightOnRowWithTargetsSwitchesTab: right on a multi-target command's
// parent row switches tab like right on any other row — its targets are
// already visible, so there is nothing left for right to open.
func TestRightOnRowWithTargetsSwitchesTab(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("dev"))

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRight})
	nm := next.(Model)
	if nm.active == tabWork {
		t.Errorf("active tab stayed on Work, want right on a multi-target row to switch tab")
	}
}

// TestLeftOnRowWithTargetsSwitchesTab mirrors the above for left.
func TestLeftOnRowWithTargetsSwitchesTab(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("dev"))

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyLeft})
	nm := next.(Model)
	if nm.active == tabWork {
		t.Errorf("active tab stayed on Work, want left on a multi-target row to switch tab")
	}
}

// TestEscOnTargetRowIsANoOp: esc has no binding on the Work tab now that
// there's no submenu to close — it must not switch tab.
func TestEscOnTargetRowIsANoOp(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = rowIndex(t, m, isTargetRow("dev", "mac"))

	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Errorf("esc on the Work tab returned a non-nil Cmd, want nil")
	}
	nm := next.(Model)
	if nm.active != tabWork {
		t.Errorf("active tab changed to %v, want esc to be a no-op", nm.active)
	}
}

// TestEscOnParentRowIsANoOp mirrors the above from the parent row.
func TestEscOnParentRowIsANoOp(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("dev"))

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	nm := next.(Model)
	if nm.active != tabWork {
		t.Errorf("active tab changed to %v, want esc to be a no-op", nm.active)
	}
}

// TestRightOnRowWithNoTargetsSwitchesTab: a plain command (no targets) has
// nothing to expand and never did — right switches tab.
func TestRightOnRowWithNoTargetsSwitchesTab(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("test"))

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRight})
	nm := next.(Model)
	if nm.active == tabWork {
		t.Errorf("active tab stayed on Work, want right on a plain command row to switch tab")
	}
}

// TestLeftOnRowWithNoTargetsSwitchesTab mirrors the above for left.
func TestLeftOnRowWithNoTargetsSwitchesTab(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("test"))

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyLeft})
	nm := next.(Model)
	if nm.active == tabWork {
		t.Errorf("active tab stayed on Work, want left on a plain command row to switch tab")
	}
}

// TestEscOnRowWithNoTargetsIsANoOp: esc has no binding anywhere on the Work
// tab, plain command row included.
func TestEscOnRowWithNoTargetsIsANoOp(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("test"))

	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Errorf("esc with no targets returned a non-nil Cmd, want nil")
	}
	nm := next.(Model)
	if nm.active != tabWork {
		t.Errorf("active tab changed to %v, want esc to be a no-op", nm.active)
	}
}

// TestHAndLStaySwitchingTabOnTargetRow: h/l are pure tab-switching on every
// row, target rows included.
func TestHAndLStaySwitchingTabOnTargetRow(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = rowIndex(t, m, isTargetRow("dev", "mac"))

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	nm := next.(Model)
	if nm.active == tabWork {
		t.Errorf("active tab stayed on Work, want h on a target row to switch tab")
	}

	m2 := multiTargetModel(t)
	m2.active = tabWork
	m2.cursor[tabWork] = rowIndex(t, m2, isTargetRow("dev", "mac"))
	next2, _ := m2.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	nm2 := next2.(Model)
	if nm2.active == tabWork {
		t.Errorf("active tab stayed on Work, want l on a target row to switch tab")
	}
}

// TestLeftRightStillSwitchTabOnEveryOtherTab proves left/right switch tab
// everywhere — the Beads tab stands in for every other tab.
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

// TestWorkKeybarHasNoSubmenuHint asserts the Work tab's rendered keybar no
// longer advertises a submenu binding, through the real headless frame
// renderer (View()) — right/left have no submenu meaning on the Work tab.
func TestWorkKeybarHasNoSubmenuHint(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	m.width, m.height = 120, 40

	out := stripANSI(m.View())
	if strings.Contains(out, "submenu") {
		t.Errorf("Work tab keybar still advertises a submenu binding:\n%s", out)
	}
}
