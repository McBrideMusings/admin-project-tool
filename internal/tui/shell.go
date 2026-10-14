package tui

import (
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// shell is the state every mounted tab shares (ADR-0026): the terminal size,
// the repository and the directory its manifest lives in, which tab is up and
// whether the tab bar or the body holds focus, which tabs are mounted at all,
// the filter, the tracker sort order, the status line and its toast, the
// error, delete and quit popups, and the drag-to-copy gesture. A tab reads and
// writes these through the Model that embeds the shell; none of it belongs to
// any one tab.
type shell struct {
	width, height int

	// repoRoot is the repository every tab reads — git.RepoRoot's answer,
	// the MAIN worktree even from inside a linked one.
	repoRoot string
	// manifestRoot is the directory whose admin.toml this dashboard reads, and
	// it is not always repoRoot. git.RepoRoot deliberately returns the MAIN
	// worktree from inside a linked one (internal/git/git.go), which is right
	// for branches, worktrees and PRs — they are repo-wide — but wrong for the
	// manifest: admin.toml is per-checkout and never committed, so a worktree
	// that declares one had its Work tab silently dropped while `admin <cmd>`
	// in that same directory ran the commands fine. cmd.resolveProjectManifest
	// walks up from the working directory (manifest.FindRoot); this mirrors it,
	// so the Work tab lists exactly the commands `admin <name>` would run here.
	// Empty means "fall back to repoRoot" — see manifestDir.
	manifestRoot string

	// checkout is the top level of the checkout the dashboard was opened in,
	// which differs from repoRoot inside a linked worktree for the same reason
	// manifestRoot does. Every git read and write runs there except the main
	// checkout's own working status: the current branch, its commit list, pull
	// and checkout all belong to the checkout the user is standing in. Empty
	// means "fall back to repoRoot" — see checkoutDir.
	checkout string

	// herdr mode: when running inside a herdr pane, enter on a worktree action
	// creates/opens it inline (opening a focused herdr workspace) and the
	// dashboard stays up, instead of quitting to cd the parent shell.
	herdr bool

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
	// alone is set when the dashboard was opened on one tab by its own verb
	// (`admin prs`): no tab bar is drawn, no key switches tabs, and focus stays
	// on the body. It is set only through WithMountedAlone, never inferred from
	// len(mounted), so a Model built with a one-element mount set stays a shell.
	alone bool

	filtering bool
	filter    string

	// trackSort is the active order for the tracker list — the Beads tab's flat
	// list and the Issues tab — cycled by `s` (see tracksort.go). It is shared
	// across both tabs rather than kept per-tab: one key, one order, named in
	// the header. Persisted to view.toml's tracker.sort (admin-y5lc.1): New()
	// loads it through viewstate's cascade and every `s` press saves the new
	// order to the project-level file. It does not reach the bead trees — the
	// dependency tree's order already comes from the graph itself, and an
	// arbitrary sort would scramble the blocker-above-blocked shape that view
	// exists to show.
	trackSort trackSort

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

	// confirming is the delete modal's confirm phase: awaiting y/n on a batch
	// of confirmTargets, staged by the Repo tab's x and ⇧X.
	confirming     bool
	confirmTargets []cleanupTarget

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

	// Terminal view (admin-3rgp.65): attaching to a pty-addressed live-work
	// entry replaces the tab bar and the body with that entry's Screen — see
	// terminalview.go. It is the shell's because any tab can open it: a
	// Commands or Workers row, a [keys.*] binding on any row, or a dispatch
	// whose enter arrived while it was still starting. termAttached is the
	// switch view.go and handleKey both read; termRows/termCols are the size
	// the child's pty was last given (Model.fitTerminal). All four are zeroed
	// together by detachTerminal.
	termAttached bool
	termEntryID  string
	termRows     int
	termCols     int
}

// attach resolves e's attach through the registry — the terminal view for an
// entry on the dashboard's own hosted pty (Via empty), the project's declared
// [terminal] attach command for one dispatched through a launcher (Via set) —
// see livework.Registry.Attach. A nil e is a silent no-op, matching every
// other action that requires a live row to act on.
//
// A hosted-pty entry's Attach call reaches installTerminalOpener's callback
// synchronously (terminalview.go), which can only record the requested
// handle. Draining that recording right after Attach succeeds is what opens
// the terminal view; a launcher-dispatched entry's Attach shells out instead
// and records nothing, so takePendingTerminal reports false. The view opens
// unsized: Model.fitTerminal sizes the child's pty to the body region at the
// end of the same Update.
func (s *shell) attach(e *livework.Entry) tea.Cmd {
	if e == nil {
		return nil
	}
	if err := livework.Default.Attach(e.ID); err != nil {
		s.setStatus("attach failed: "+err.Error(), cRed)
		return nil
	}
	if _, ok := takePendingTerminal(); ok {
		s.termAttached, s.termEntryID = true, e.ID
		s.termRows, s.termCols = 0, 0
		return termTickCmd()
	}
	return nil
}

// detachTerminal returns to the list. It never touches the child — the
// process keeps running, exactly as attaching to a pane never suspends it.
func (s *shell) detachTerminal() {
	s.termAttached = false
	s.termEntryID = ""
	s.termRows, s.termCols = 0, 0
}

// manifestDir is the directory to read admin.toml, and anything co-located with
// it, out of. Zero-value Models (every table-driven test that sets only
// repoRoot) keep their old behaviour.
func (s shell) manifestDir() string {
	if s.manifestRoot != "" {
		return s.manifestRoot
	}
	return s.repoRoot
}

// checkoutDir is the directory the dashboard's git commands run in — see the
// checkout field.
func (s shell) checkoutDir() string {
	if s.checkout != "" {
		return s.checkout
	}
	return s.repoRoot
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

// cols returns the layout width for list rows.
func (s shell) cols() int {
	if s.width > 0 {
		return s.width
	}
	return 80
}

// filterRows applies the active filter (substring on a row's text), always
// keeping section headers so the structure stays legible.
func (s shell) filterRows(all []rowItem) []rowItem {
	if s.filter == "" {
		return all
	}
	q := strings.ToLower(s.filter)
	var out []rowItem
	for _, r := range all {
		switch r.kind {
		case rowHeader, rowRule:
			out = append(out, r) // keep section structure
		default:
			if strings.Contains(strings.ToLower(r.searchText()), q) {
				out = append(out, r)
			}
		}
	}
	return out
}

// rowAt is the row under cursor i in rows, or none while focus is on the tab
// bar — the single lever admin-70hb uses to make every row-scoped key an
// automatic no-op from there.
func (s shell) rowAt(rows []rowItem, i int) (rowItem, bool) {
	if s.focus == focusTabs {
		return rowItem{}, false
	}
	if i >= 0 && i < len(rows) && rows[i].selectable() {
		return rows[i], true
	}
	return rowItem{}, false
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
