package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// branchLine finds the rendered line carrying branch and its index in the frame.
func branchLine(t *testing.T, m Model, branch string) (string, int, []hitTarget) {
	t.Helper()
	lines, hits := m.frame()
	for i, l := range lines {
		if strings.Contains(l, branch) {
			return l, i, hits
		}
	}
	t.Fatalf("branch %q not in the rendered frame", branch)
	return "", 0, nil
}

// Under herdr, a branch whose worktree has a workspace open draws ▣, and a click
// on that mark focuses the workspace without moving the cursor.
func TestOpenHerdrSpaceMarksTheRowAndClickFocusesIt(t *testing.T) {
	m := freshWorktreeModel(t, time.Now(), true)
	m.active = tabLocal
	m.herdr = true
	line, y, hits := branchLine(t, m, "1378-fresh-work")
	if !strings.Contains(line, "▣") {
		t.Fatalf("row with an open space has no ▣ mark: %q", line)
	}
	row := hits[y].row
	cursor := m.paneCursor[paneBranches]
	next, cmd := m.Update(tea.MouseMsg{Y: y, X: 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(Model)
	next, cmd = m.Update(tea.MouseMsg{Y: y, X: 1, Action: tea.MouseActionRelease})
	m = next.(Model)
	if cmd == nil {
		t.Error("clicking the ▣ mark issued no focus command")
	}
	if m.paneCursor[paneBranches] != cursor || row < 0 {
		t.Errorf("focus click moved the cursor: %d -> %d", cursor, m.paneCursor[paneBranches])
	}
}

// Outside herdr none of the tooling appears, whatever the last poll held.
func TestNoHerdrSpaceMarkOutsideHerdr(t *testing.T) {
	m := freshWorktreeModel(t, time.Now(), true)
	m.active = tabLocal
	m.herdr = false
	line, _, _ := branchLine(t, m, "1378-fresh-work")
	if strings.Contains(line, "▣") {
		t.Fatalf("▣ drawn outside herdr: %q", line)
	}
}
