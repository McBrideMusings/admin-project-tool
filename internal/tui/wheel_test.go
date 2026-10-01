package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

func wheelBeadsModel(n int) Model {
	m := seededModel()
	m.haveBeads, m.hasBeads, m.beadPrefix = true, true, "bdtest"
	for i := 0; i < n; i++ {
		m.beads = append(m.beads, bd.Bead{ID: fmt.Sprintf("bdtest-%d", i), Title: "t", Status: "open", Type: "task", Priority: 2})
	}
	m.active = tabBeads
	m.clampCursor()
	return m
}

func wheel(m Model, y int, down bool) (Model, tea.Cmd) {
	b := tea.MouseButtonWheelUp
	if down {
		b = tea.MouseButtonWheelDown
	}
	next, cmd := m.Update(tea.MouseMsg{Y: y, X: 5, Action: tea.MouseActionPress, Button: b})
	return next.(Model), cmd
}

// selectableAfter is the row index n selectable rows below from.
func selectableAfter(m Model, from, n int) int {
	rows := m.visibleRows()
	for i := from + 1; i < len(rows) && n > 0; i++ {
		if rows[i].selectable() {
			from, n = i, n-1
		}
	}
	return from
}

func TestWheelDownMovesCursorThreeRows(t *testing.T) {
	m := wheelBeadsModel(20)
	start := m.cursor[tabBeads]
	want := selectableAfter(m, start, 3)
	m, _ = wheel(m, 10, true)
	if got := m.cursor[tabBeads]; got != want {
		t.Fatalf("cursor after wheel down = %d, want %d (from %d)", got, want, start)
	}
	m, _ = wheel(m, 10, false)
	if got := m.cursor[tabBeads]; got != start {
		t.Fatalf("cursor after wheel up = %d, want back at %d", got, start)
	}
}

func TestWheelClampsAtFirstAndLastRow(t *testing.T) {
	m := wheelBeadsModel(5)
	first := m.cursor[tabBeads]
	m, _ = wheel(m, 10, false)
	if m.cursor[tabBeads] != first || m.focus != focusBody {
		t.Fatalf("wheel up on the first row: cursor %d focus %v, want %d and body focus", m.cursor[tabBeads], m.focus, first)
	}
	for i := 0; i < 10; i++ {
		m, _ = wheel(m, 10, true)
	}
	last := selectableAfter(m, m.cursor[tabBeads], 100)
	if m.cursor[tabBeads] != last {
		t.Fatalf("cursor after wheeling past the end = %d, want last row %d", m.cursor[tabBeads], last)
	}
}

func TestWheelOverOverlayLeavesListCursorAlone(t *testing.T) {
	m := wheelBeadsModel(20)
	start := m.cursor[tabBeads]
	m.errOpen = true
	m, _ = wheel(m, 10, true)
	if m.cursor[tabBeads] != start {
		t.Fatalf("wheel under the error overlay moved the cursor to %d", m.cursor[tabBeads])
	}
	m.errOpen = false
	m.beadOpen = true
	m, _ = wheel(m, 10, true)
	if m.cursor[tabBeads] != start || m.beadScroll != wheelRows {
		t.Fatalf("wheel in bead overlay: cursor %d scroll %d, want cursor %d scroll %d", m.cursor[tabBeads], m.beadScroll, start, wheelRows)
	}
}

func TestWheelDoesNotTouchSelectionWatchdog(t *testing.T) {
	m := wheelBeadsModel(20)
	m.selGen = 7
	m, cmd := wheel(m, 10, true)
	if cmd != nil || m.selGen != 7 || m.sel.inProgress() {
		t.Fatalf("wheel touched the selection gesture: cmd=%v selGen=%d", cmd != nil, m.selGen)
	}
}

func TestWheelMovesPaneUnderPointerOnRepoTab(t *testing.T) {
	m := seededModel()
	m.active = tabLocal
	m.pane = paneBranches
	for i := 0; i < 8; i++ {
		m.local.commits = append(m.local.commits, git.Commit{SHA: fmt.Sprintf("c%d", i), Subject: "s"})
	}
	lines, hits := m.frame()
	y := -1
	for i, h := range hits {
		if h.kind == hitRow && h.pane == paneCommits && i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			y = i
			break
		}
	}
	if y < 0 {
		t.Fatal("no Commits row in the hit index")
	}
	m, _ = wheel(m, y, true)
	if m.paneCursor[paneCommits] != 3 || m.paneCursor[paneBranches] != 0 || m.pane != paneBranches {
		t.Fatalf("commits cursor %d branches cursor %d active pane %v, want 3, 0, Branches", m.paneCursor[paneCommits], m.paneCursor[paneBranches], m.pane)
	}
}
