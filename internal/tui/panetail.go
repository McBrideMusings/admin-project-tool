// panetail.go refreshes the Work tab band's pane snapshot for a
// launcher-dispatched worker (admin-spjp.6). A worker in a launcher pane is
// not a child of this process, so there is no Ring to read; when the project
// declares [terminal].read, the work tick shells it with ADMIN_HANDLE set and
// stores the printed text as livework.Entry.Snapshot. Only the row under the
// cursor is read — the band shows one entry — and each reply replaces the
// last whole, never appending.
package tui

import (
	"context"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// paneReadTimeout bounds one read so a wedged launcher command can't stall
// the tick.
const paneReadTimeout = 2 * time.Second

// paneReadFn is the test seam for the read shell-out.
var paneReadFn = kinds.ReadLauncherPane

// paneReadInflight makes an overlapping tick a no-op, like panePollInflight.
var paneReadInflight atomic.Bool

// paneTailMsg carries one completed read back into Update.
type paneTailMsg struct {
	id, text string
	err      error
}

// paneTailTarget is the launcher-dispatched live entry under the Work tab's
// cursor, or nil when the cursor is elsewhere or on a row with no such entry.
func (m Model) paneTailTarget() *livework.Entry {
	if !m.onWorkSurface() {
		return nil
	}
	rows := m.visibleRows()
	cur := m.cursor[m.active]
	if cur < 0 || cur >= len(rows) {
		return nil
	}
	e := bandEntry(rows[cur])
	if e == nil || e.Via == "" {
		return nil
	}
	st, ok := livework.Default.State(e.ID)
	if !ok || (st != livework.StateRunning && st != livework.StateWaiting) {
		return nil
	}
	return e
}

// pollPaneTailCmd reads the cursor row's pane once. readCmd == "" (no
// [terminal].read declared) yields no message.
func (m Model) pollPaneTailCmd(readCmd string) tea.Cmd {
	e := m.paneTailTarget()
	if readCmd == "" || e == nil {
		return nil
	}
	id, handle := e.ID, e.Handle
	return func() tea.Msg {
		if !paneReadInflight.CompareAndSwap(false, true) {
			return nil
		}
		defer paneReadInflight.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), paneReadTimeout)
		defer cancel()
		text, err := paneReadFn(ctx, readCmd, handle)
		return paneTailMsg{id: id, text: text, err: err}
	}
}

// applyPaneTail stores a successful read. A failed read leaves the last
// snapshot in place.
func applyPaneTail(msg paneTailMsg) {
	if msg.err != nil {
		return
	}
	livework.Default.SetSnapshot(msg.id, msg.text)
}
