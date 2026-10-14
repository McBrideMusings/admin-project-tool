package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/keymap"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// commandsTab is the Commands tab as its own model (ADR-0026): its rows, its
// cursor, the collapse mode, the in-flight dispatch bookkeeping, and the work
// surface it shares in kind with the Workers tab. It reads the manifest and
// its origins through the config handle, the live-work registry through the
// work handle, and — for a query workflow's targets — the beads list and the
// open issues through the beads and sync handles; its constructor asking for
// them is its declaration. work.go is its data half, workview.go draws it.
type commandsTab struct {
	declared []source
	work     workSource
	cfg      configSource
	beads    beadsSource
	sync     syncSource
	usage    usageSource
	surface  workSurface

	cursor int
	// recentPick is the invocation the Recent strip has picked, moved by
	// ←→ while the cursor is on the strip; "" picks the most recent
	// (commandsearch.go).
	recentPick string

	// collapsed is the display mode, toggled by `c` (admin-4zz9): false (the
	// zero value, and the launch default when no view.toml sets it) is
	// expanded — every command's targets render. true is collapsed — only the
	// command named by openCmd shows its targets; see rows' showTargets gate.
	// Persisted to view.toml's work.collapsed (admin-y5lc.1): New() loads it
	// through viewstate's cascade and toggleCollapse saves every flip to the
	// project-level file.
	collapsed bool
	// openCmd is which command's targets show while collapsed: the command
	// owning whatever row the cursor is on. toggleCollapse seeds it the instant
	// collapse turns on, moveCursorWork keeps it in sync as the cursor moves,
	// and seedOpen sets it when the dashboard launches already collapsed.
	// Ignored entirely while collapsed is false.
	openCmd string

	// pendingDispatch holds the command names whose dispatchCommandCmd is in
	// flight — the window between the enter keypress and the
	// workDispatchStartedMsg that reports either a livework.Entry now exists
	// or the spawn failed. commandRowItem reads it to set workRowData.pending
	// so the row renders "starting" instead of idle, and activateCommand
	// consults it to refuse a second dispatch of the same command during that
	// window (admin-qk77). A map, not a single string, because more than one
	// command's dispatch can be in flight at once. Mutate it only from Update,
	// never from View.
	pendingDispatch map[string]bool
	// pendingAttach holds the command names whose enter arrived WHILE their
	// dispatch was still pending — activateCommand records the intent to
	// attach here instead of dispatching a second run, and dispatchStarted
	// drains it once the entry the attach needs actually exists.
	pendingAttach map[string]bool
	// pendingTarget holds, for a command name currently in pendingDispatch,
	// WHICH target key that in-flight dispatch was for (admin-2y6p): a
	// multi-target command's own target rows must only mark the ONE row
	// actually being dispatched, not every target sharing that command name.
	// Absent or "" means no dispatch is pending for that command, or it was a
	// flat (non-target) dispatch.
	pendingTarget map[string]string
}

// newCommandsTab builds the tab, declaring the work, beads and sync sources by
// asking for their handles, and starting collapsed when view.toml says so.
func newCommandsTab(f feeds, collapsed bool) *commandsTab {
	d := sourceSet{f: f}
	t := &commandsTab{
		work:            d.work(),
		cfg:             d.config(),
		beads:           d.beads(),
		sync:            d.sync(),
		usage:           d.usage(),
		collapsed:       collapsed,
		pendingDispatch: map[string]bool{},
		pendingAttach:   map[string]bool{},
		pendingTarget:   map[string]string{},
	}
	t.surface = newWorkSurface(t.work, t.cfg, false)
	t.declared = d.declared
	return t
}

func (t *commandsTab) sources() []source { return t.declared }

func (t *commandsTab) cursorRef() *int { return &t.cursor }

// currentRow is the row under the cursor, or none while focus is on the tab
// bar.
func (t *commandsTab) currentRow(s *shell) (rowItem, bool) {
	return s.rowAt(t.visibleRows(s), t.cursor)
}

// seedOpen opens the command under the cursor when the dashboard launches
// already collapsed (view.toml's work.collapsed = true): there is no `c`
// keypress to seed openCmd the way toggleCollapse does.
func (t *commandsTab) seedOpen(s *shell) {
	if !t.collapsed {
		return
	}
	if r, ok := t.currentRow(s); ok {
		if name := ownerCmdName(r); name != "" {
			t.openCmd = name
		}
	}
}

// clamp brings the cursor back into the row list and off a row it cannot sit
// on, the way the shell's clampCursor does for every list tab.
func (t *commandsTab) clamp(s *shell) {
	rows := t.visibleRows(s)
	if len(rows) == 0 {
		t.cursor = 0
		return
	}
	t.cursor = clampi(0, t.cursor, len(rows)-1)
	if !rows[t.cursor].selectable() {
		t.moveCursor(s, 1)
	}
}

// moveCursor walks the cursor delta rows. Expanded, the row list is fixed for
// the walk and the shell's skip-and-wrap walk applies; collapsed, the list
// depends on where the cursor goes (moveCursorWork).
func (t *commandsTab) moveCursor(s *shell, delta int) {
	if t.collapsed && s.filter == "" {
		t.moveCursorWork(s, delta)
		return
	}
	stepCursor(t.visibleRows(s), &t.cursor, delta)
}

// cursorMark returns a func that restores the cursor and the open command.
func (t *commandsTab) cursorMark() func() {
	cursor, open := t.cursor, t.openCmd
	return func() { t.cursor, t.openCmd = cursor, open }
}

// moveCursorWork is moveCursor's collapsed variant. The generic walk assumes
// the row list is fixed for the whole walk; here it isn't, in two ways:
//
//   - Stepping onto a closed command's group header (unselectable, since a
//     header never gets a cursor marker) is the signal to open that
//     command, which inserts its target rows into the list right where the
//     header sits. This lands directly on the first selectable row after
//     the header (moving down) or the last one before it (moving up) —
//     never on the header itself, and never by continuing to add delta to
//     an index whose meaning changed out from under it once the rebuild ran.
//   - Stepping onto an ALREADY-selectable row that belongs to a different
//     command than the one currently open also changes which command is
//     open (to that row's own owner) — and opening or closing a command
//     resizes the list by however many target rows it has. The row just
//     landed on is still in the rebuilt list (opening/closing some OTHER
//     command never removes it), so it's relocated there by identity
//     (sameRowIdentity) rather than by reusing the old index i, which was
//     computed against the list from before the resize.
//
// This is the reason the row list can be treated as "a pure function of
// (mode, cursor)": the cursor here is openCmd (which command is open) plus the
// row index, and moveCursorWork is what keeps the two in lockstep as the index
// moves across a command boundary.
func (t *commandsTab) moveCursorWork(s *shell, delta int) {
	rows := t.visibleRows(s)
	n := len(rows)
	if n == 0 {
		return
	}
	i := t.cursor
	for steps := 0; steps < n; steps++ {
		i += delta
		if i < 0 {
			i = n - 1
		}
		if i >= n {
			i = 0
		}
		r := rows[i]
		if r.recent != nil {
			// The Recent strip previews its pick in the band; it opens
			// nothing in the list below it.
			t.cursor = i
			return
		}
		if r.selectable() {
			owner := ownerCmdName(r)
			if owner == t.openCmd {
				t.cursor = i
				return
			}
			t.openCmd = owner
			for j, rr := range t.visibleRows(s) {
				if sameRowIdentity(rr, r) {
					t.cursor = j
					return
				}
			}
			t.cursor = i // defensive fallback; should be unreachable
			return
		}
		if r.kind == rowCommand && r.group && r.work != nil && r.work.cmdName != t.openCmd {
			name := r.work.cmdName
			t.openCmd = name
			newRows := t.visibleRows(s)
			idx := indexOfCommandRow(newRows, name)
			if idx < 0 {
				return
			}
			// A command's target rows always sit immediately after its own
			// header, whichever direction opened it — targetRows never
			// inserts anything before the header it belongs to. So the
			// block to land in is the contiguous run starting right after
			// idx, and the direction only picks which end of that run: the
			// first row (moving down, arriving from above) or the last
			// (moving up, arriving from below).
			start := idx + 1
			end := start
			for end < len(newRows) && newRows[end].kind == rowCommandTarget &&
				newRows[end].work != nil && newRows[end].work.cmdName == name {
				end++
			}
			if end == start {
				// A query workflow that matched nothing: its only row
				// under the group is text, so walk on to the next
				// selectable row rather than stall on this keypress.
				continue
			}
			if delta > 0 {
				t.cursor = start
			} else {
				t.cursor = end - 1
			}
			return
		}
	}
}

// entryRow is the Commands row whose live entry has id, with collapse off so
// a hidden target row still counts.
func (t *commandsTab) entryRow(s *shell, id string) (rowItem, *livework.Entry, bool) {
	all := *t
	all.collapsed = false
	for _, r := range all.rows(s) {
		if e := bandEntry(r); e != nil && e.ID == id && r.work != nil {
			return r, e, true
		}
	}
	return rowItem{}, nil, false
}

// activate is ↵ on a command or target row.
func (t *commandsTab) activate(s *shell, r rowItem) tea.Cmd {
	switch r.kind {
	case rowCommand:
		return t.activateCommand(s, r.work)
	case rowCommandTarget:
		return t.activateCommandTarget(s, r.work)
	}
	return nil
}

// modal is false: the tab has no overlay of its own.
func (t *commandsTab) modal() bool { return false }

// key takes `c`, which toggles collapse, and the work surface's keys.
func (t *commandsTab) key(s *shell, msg tea.KeyMsg) (bool, tea.Cmd) {
	if msg.String() == "c" {
		t.toggleCollapse(s)
		return true, nil
	}
	r, ok := t.currentRow(s)
	if ok && r.recent != nil {
		switch msg.String() {
		case "left", "h":
			t.stepRecent(r, -1)
			return true, nil
		case "right", "l":
			t.stepRecent(r, 1)
			return true, nil
		}
	}
	return t.surface.key(s, r, ok, msg.String())
}

// body is the work surface's list and band over this tab's rows.
func (t *commandsTab) body(s *shell, width, height int) ([]string, []hitTarget) {
	return t.surface.body(s, t.visibleRows(s), t.cursor, width, height)
}

// bindings is the work surface's footer for the cursor row, with `c` named
// for what pressing it does next.
func (t *commandsTab) bindings(s *shell) (keymap.RowKind, []keymap.Binding) {
	collapse := "collapse"
	if t.collapsed {
		collapse = "expand"
	}
	r, ok := t.currentRow(s)
	kind, shown := t.surface.bindings(r, ok, collapse)
	if !ok || r.recent == nil {
		return kind, shown
	}
	// ←→ sits beside ↵, the key it chooses the target of.
	out := make([]keymap.Binding, 0, len(shown)+1)
	for _, b := range shown {
		out = append(out, b)
		if b.Key == "enter" {
			out = append(out, keymap.Binding{Display: "←→", Label: "recent"})
		}
	}
	return kind, out
}

func (t *commandsTab) footer(int) (string, bool) { return "", false }

// loaded is always true: the manifest and the registry are read fresh on
// every frame.
func (t *commandsTab) loaded() bool { return true }

// freshness reads "live": the manifest and the live-work registry are both
// read fresh on every frame — no fetch, no cache.
func (t *commandsTab) freshness() seg { return seg{t: "live", fg: cFaint} }

// update takes a dispatch's start report, which ends that command's
// "starting" window.
func (t *commandsTab) update(s *shell, msg tea.Msg) tea.Cmd {
	if msg, ok := msg.(workDispatchStartedMsg); ok {
		return t.dispatchStarted(s, msg)
	}
	return nil
}
