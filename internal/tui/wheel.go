package tui

import tea "github.com/charmbracelet/bubbletea"

// wheelRows is how many rows one wheel notch moves the cursor.
const wheelRows = 3

// handleWheel scrolls the list under the pointer: it moves that pane's cursor
// by wheelRows through moveCursor, the mover ↑/↓ use, and the render's
// existing cursor-follows-viewport scroll does the rest. It touches no
// selection state, so a wheel notch never starts, extends or ends a
// drag-to-copy gesture and never rearms its watchdog.
func (m Model) handleWheel(y int, down bool) (tea.Model, tea.Cmd) {
	delta := -1
	if down {
		delta = 1
	}

	// An attached terminal view forwards input to its child; nothing here
	// forwards or consumes wheel events for it.
	if m.termAttached {
		return m, nil
	}
	// A tab's own overlay (the bead box) scrolls on ↑/↓: reuse its keys. The
	// Repo tab's name prompt is text entry, not a scroller, so the wheel does
	// nothing over it.
	if tm := m.tabModel(m.active); tm != nil && tm.modal() {
		if m.active == tabLocal {
			return m, nil
		}
		key := tea.KeyMsg{Type: tea.KeyUp}
		if down {
			key.Type = tea.KeyDown
		}
		for i := 0; i < wheelRows; i++ {
			tm.key(&m.shell, key)
		}
		return m, nil
	}
	if m.wheelCaptured() {
		return m, nil
	}
	if m.focus == focusTabs {
		if !down {
			return m, nil
		}
		m.enterBody()
	}

	activePane := m.repoTab.pane
	m.repoTab.pane = m.wheelPane(y)
	for i := 0; i < wheelRows; i++ {
		if !m.wheelStep(delta) {
			break
		}
	}
	m.repoTab.pane = activePane
	return m, nil
}

// wheelCaptured reports whether a modal or input mode owns the keyboard, the
// same set handleKey returns early for before it reaches ↑/↓. The list under
// any of them must not move.
func (m Model) wheelCaptured() bool {
	return m.quitOpen || m.confirming || m.wtPickerOpen ||
		m.errOpen || m.deleting || m.filtering
}

// wheelPane resolves the pane under row y on the Repo tab from the last
// frame's hit index, as click does. A y with no pane under it, or any other
// tab, gives the active pane.
func (m Model) wheelPane(y int) localPane {
	if m.active != tabLocal {
		return m.repoTab.pane
	}
	_, hits := m.frameRaw()
	if y < 0 || y >= len(hits) {
		return m.repoTab.pane
	}
	if h := hits[y]; h.kind == hitRow && m.repo().paneCount(h.pane) > 0 {
		return h.pane
	}
	return m.repoTab.pane
}

// wheelStep moves the cursor one row through moveCursor and reports whether it
// moved. moveCursor wraps past either end on the list tabs, which ↑/↓ want and
// the wheel does not, so a step that wrapped is undone; going up from the first
// row stops instead of handing focus to the tab bar the way ↑ does.
func (m *Model) wheelStep(delta int) bool {
	if delta < 0 && m.atBodyTop() {
		return false
	}
	before := *m.cur(m.active)
	restore := func() { *m.cur(m.active) = before }
	if mv, ok := m.tabModel(m.active).(cursorMover); ok {
		restore = mv.cursorMark()
	}
	m.moveCursor(delta)
	if m.active != tabLocal && (*m.cur(m.active)-before)*delta < 0 {
		restore()
		return false
	}
	return true
}
