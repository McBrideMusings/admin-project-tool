package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/keymap"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// workSurface is what the Commands and Workers tabs share (ADR-0026): the row
// list with the live band pinned beneath it (workview.go's body and band),
// attach, kill, and the footer bindings that follow the cursor row's state.
// Each of the two tab models holds its own, built from the work and config
// handles its constructor asked for; the surface keeps no state of its own,
// so the two never see each other's cursor.
type workSurface struct {
	work workSource
	cfg  configSource
	// workers is true on the Workers tab: its empty band says no worker is
	// running, and its footer has no collapse key and no ↵ while it is empty.
	workers bool
}

func newWorkSurface(work workSource, cfg configSource, workers bool) workSurface {
	return workSurface{work: work, cfg: cfg, workers: workers}
}

// attach opens the live entry row r previews: a worker row's own session, or
// a command or target row's running entry. An idle row is a no-op.
func (w workSurface) attach(s *shell, r rowItem) tea.Cmd {
	return s.attach(bandEntry(r))
}

// kill stops the live entry row r previews, and says so on the status line. A
// row with nothing live, or an entry with no way to stop it, is a no-op.
func (w workSurface) kill(s *shell, r rowItem) {
	e := bandEntry(r)
	if !entryIsLive(e) || e.Ops.Kill == nil {
		return
	}
	id := e.ID
	if err := livework.Default.Kill(id); err != nil {
		s.setStatus("kill failed: "+err.Error(), cRed)
	} else {
		s.setStatus("killed "+id, cDim)
	}
}

// key takes the keys both work tabs share: `a` attaches and `x` kills the
// cursor row's live entry, and `r` is swallowed — a work tab renders the live
// registry and the resolved manifest, neither of which a refresh fetches, so
// `r` there would spend a network sync to redraw the same rows. `R` still
// syncs everything from any tab. A [keys.command]/[keys.worker] binding for
// any of them is dispatched before this runs and shadows it (reported by
// admin check).
func (w workSurface) key(s *shell, r rowItem, ok bool, k string) (bool, tea.Cmd) {
	switch k {
	case "a":
		if ok {
			return true, w.attach(s, r)
		}
		return true, nil
	case "x":
		if ok {
			w.kill(s, r)
		}
		return true, nil
	case "r":
		return true, nil
	}
	return false, nil
}

// bindings is a work tab's footer, drawn from the row kind's keymap.Builtins
// table (the shell merges the cursor row's [keys.<kind>] bindings over it),
// and computed from the SAME row-state resolution attach and kill act on — so
// the footer can never advertise an action the key does not take (admin-3rgp.
// 72.12). It follows the cursor row r (ok false: no row under the cursor):
//
//   - idle command/target → "↵ dispatch", no kill.
//   - live command/target or live worker → "↵ attach · x kill".
//   - finished worker → "↵ transcript", no kill (nothing left to stop).
//   - no actionable row under the cursor → the idle pairs, matching what a
//     fresh Commands tab (cursor on the first idle command) already shows —
//     except on a Workers tab with no workers, where ↵ has nothing to act
//     on and is not offered.
//
// `c` (admin-4zz9) appears on the Commands tab only, labelled collapse — what
// pressing it would do next, "expand" once collapsed. `a attach` and `/
// filter` are deliberately not advertised even though both keys work — the
// footer's job is to say only what the current row can actually do.
func (w workSurface) bindings(r rowItem, ok bool, collapse string) (keymap.RowKind, []keymap.Binding) {
	kind, enter, kill := keymap.Command, "dispatch", false
	if w.workers {
		kind = keymap.Worker
		if len(w.work.workers()) == 0 {
			enter = ""
		}
	}
	if ok && r.work != nil {
		if k, bindable := rowKindOf(r); bindable {
			kind = k
		}
		switch resolveRowAction(r.kind, r.work) {
		case actionAttach:
			enter, kill = "attach", true
		case actionTranscript:
			// A stopped worker still holds its worktree; kill retires it.
			enter, kill = "transcript", r.kind == rowWorker && entryIsLive(r.work.worker)
		}
	}

	var shown []keymap.Binding
	for _, b := range keymap.Builtins(kind) {
		switch b.Key {
		case "enter":
			if enter == "" {
				continue
			}
			b.Label = enter
		case "a":
			continue
		case "x":
			if !kill {
				continue
			}
		case "c":
			if collapse == "" {
				continue
			}
			b.Label = collapse
		}
		shown = append(shown, b)
	}
	return kind, shown
}

// keyBinder is a tab whose footer is a row kind's bindings it computes itself;
// the shell merges the cursor row's [keys.<kind>] bindings over them.
type keyBinder interface {
	bindings(s *shell) (keymap.RowKind, []keymap.Binding)
}

// cursorMover is a tab whose row list depends on where its cursor is, so the
// shell's fixed-list walk cannot move it.
type cursorMover interface {
	moveCursor(s *shell, delta int)
	// cursorMark returns a func that puts the cursor back where it is now.
	cursorMark() func()
}
