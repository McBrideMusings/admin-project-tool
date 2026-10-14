package tui

import (
	"errors"

	"github.com/charmbracelet/lipgloss"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// shell is the state every mounted tab shares (ADR-0026): the terminal size,
// which tab is up and whether the tab bar or the body holds focus, which tabs
// are mounted at all, the filter, the status line and its toast, the error,
// delete and quit popups, and the drag-to-copy gesture. A tab reads and
// writes these through the Model that embeds the shell; none of it belongs to
// any one tab.
type shell struct {
	width, height int

	active tab
	// focus is which zone on screen owns the keyboard — the tab bar or the
	// body. New() sets it to focusTabs after settling active, so a fresh
	// session opens on the tab bar; every other Model literal built directly
	// by a test gets focusBody for free from the zero value. See focusZone.
	focus focusZone
	// mounted is the set of tabs this dashboard holds. Nil mounts every tab
	// the project draws, which is bare `admin`. A tab outside the set is never
	// drawn, and a data source only that tab declares never starts
	// (sources.go).
	mounted []tab

	filtering bool
	filter    string

	status   string                 // transient action result line
	statusFg lipgloss.TerminalColor // its color (nil → dim)
	// statusHint is pinned flush-right on the status line and the message is
	// truncated to make room, so an affordance can't be the thing that falls off
	// the end — a long git refusal is exactly when "e for detail" has to be visible.
	statusHint string
	// toastGen counts the "copied to clipboard" toasts shown, so a delayed
	// expiry tick can tell whether the status line still holds the toast it was
	// scheduled for, or whether a newer action has already replaced it.
	toastGen int

	// Error detail: the status line holds one line, but a git refusal often carries
	// the part you actually need on the lines after its headline — the files a
	// checkout would overwrite, the worktree already holding a branch. Those lines
	// are parked here when an action fails and shown as a centered overlay on `e`,
	// rather than being dropped or smeared across the dashboard.
	errTitle  string // the action that failed, e.g. "pull failed"
	errDetail string // git's full output, headline included
	errOpen   bool   // the overlay is up

	// Delete modal: a centered overlay that steps through the batch one git call
	// at a time so the UI repaints between items and shows live per-item progress.
	deleting      bool
	deleteDone    bool // every item processed; modal shows the summary, any key dismisses
	deleteTargets []cleanupTarget
	deleteIdx     int
	deleteResults []deleteResult
	// deleteForce is set only on the `f` retry over conflict-blocked items, and
	// tells the stepper to remove those worktrees without the auto-stash that
	// cannot run on an unmerged index. It rides on the model rather than the
	// target because every step of that retry batch takes the same answer.
	deleteForce bool

	// Quit confirm overlay (admin-3rgp.63): quitting while the dashboard is
	// hosting a running process on its own pty must not kill it silently —
	// see requestQuit in quitconfirm.go. quitHosted is the snapshot taken
	// when the confirm opened, so accepting kills exactly what was shown
	// even if the registry changes under it.
	quitOpen   bool
	quitHosted []*livework.Entry

	// sel is the in-progress or just-finished drag-to-copy gesture — anchor on
	// mouse-down, drag on motion. It is reset to its zero value (selection{},
	// phase selNone) in the same Update that finishes a real drag, so the
	// highlight is gone on the very next frame; selection never persists.
	//
	// That invariant depends on a release event actually arriving. Cell-motion
	// mouse reporting (the mode this program enables) only emits escape
	// sequences for positions inside the terminal's grid — a button released
	// after the cursor has left the window generates no SGR sequence at all,
	// so finishMouse's tea.MouseActionRelease case never runs. selGen/the
	// selWatchdogMsg cycle below is the self-heal for exactly that: a gesture
	// with no fresh mouse event in selIdleTimeout is finished the way a real
	// release would have — see finishStaleSelection — so a lost release
	// can't leave the highlight stuck, and a lost motion frozen mid-drag
	// still resolves to a real copy instead of nothing.
	sel selection
	// selGen counts drag-to-copy presses/motions; a scheduled selWatchdogMsg
	// only cancels the gesture if its gen still matches, so an older
	// watchdog from before the most recent motion is a no-op.
	selGen int
}

// isMounted reports whether t is one of the shell's mounted tabs.
func (s shell) isMounted(t tab) bool {
	if s.mounted == nil {
		return true
	}
	for _, v := range s.mounted {
		if v == t {
			return true
		}
	}
	return false
}

// setStatus sets the transient status line and its color in one place.
// toastGen bumps on every call, error included, so Update can tell a status
// change happened and schedule that status's expiry itself — see Update's
// wrapper around update. A caller never needs to thread an expiry cmd of its
// own back out; setting m.status here is enough.
func (s *shell) setStatus(text string, fg lipgloss.TerminalColor) {
	s.status = text
	s.statusFg = fg
	s.statusHint = ""
	s.toastGen++
}

// stashErrDetail parks a failed action's full git output for the `e` overlay, and
// clears any older one. Only a *git.CmdError carrying more than its headline gets
// stashed — for every other error the status line already says everything there is
// to say, and offering a detail view that repeats it would be a dead end.
func (s *shell) stashErrDetail(title string, err error) {
	s.errTitle, s.errDetail, s.errOpen = "", "", false
	var ce *git.CmdError
	if errors.As(err, &ce) && ce.HasDetail() {
		s.errTitle, s.errDetail = title, ce.Output
	}
}
