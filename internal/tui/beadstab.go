package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/viewstate"
)

// beadsTab is the Beads tab as its own model (ADR-0026): its list shape, its
// columns, its cursor and its bead overlay. Everything it knows about the
// workspace comes through src, the one handle its constructor asked for.
type beadsTab struct {
	declared []source
	src      beadsSource

	cursor int
	// tree is which of the three list shapes `g` has cycled to — flat,
	// dependency tree, or family tree. See beadTreeMode in beadtree.go.
	tree beadTreeMode
	// cols is [beads] columns from the merged config — which columns the pane
	// draws and in what order. Empty means DefaultBeadColumns.
	cols []string

	// The bead overlay: `d` opens a fixed-size box over the pane holding one
	// of three tabs — the selected bead's own text, its dependency
	// neighbourhood, or the beads that wait on it. The box is sized from the
	// terminal and never from its content, so cycling between tabs does not
	// move the screen.
	overlay    bool           // the overlay is up
	overlayTab beadOverlayTab // which of the three tabs is up
	scroll     int            // rows scrolled inside the box
}

// newBeadsTab builds the tab, declaring the beads source by asking for its
// handle. tree is the list shape view.toml remembered.
func newBeadsTab(f feeds, tree beadTreeMode) beadsTab {
	d := sourceSet{f: f}
	t := beadsTab{src: d.beads(), tree: tree}
	t.declared = d.declared
	return t
}

func (t *beadsTab) sources() []source { return t.declared }

func (t *beadsTab) cursorRef() *int { return &t.cursor }

func (t *beadsTab) rows(sh *shell) []rowItem { return t.beadRows(sh) }

func (t *beadsTab) visibleRows(sh *shell) []rowItem { return sh.filterRows(t.rows(sh)) }

// modal reports whether the overlay is up: it owns the keyboard and the body.
func (t *beadsTab) modal() bool { return t.overlay }

// key handles the keys the Beads tab owns. While the overlay is up it takes
// every key — ← → switch mode, ↑↓ scroll, esc closes, and anything else is
// swallowed rather than moving the list it is drawn over. Otherwise it owns
// `d` (open the overlay on the bead under the cursor), `g` (cycle the list
// shape) and `r` (re-read the workspace); every other key, and `d` with no
// bead under the cursor, reports unhandled so the shell's built-ins run.
func (t *beadsTab) key(sh *shell, msg tea.KeyMsg) (bool, tea.Cmd) {
	k := msg.String()
	if t.overlay {
		t.beadOverlayKey(k)
		return true, nil
	}
	switch k {
	case "d":
		// The bead's own text and its dependency neighbourhood are the two
		// things the row cannot carry: 883 characters of description and a
		// graph do not fit in a table cell.
		if _, ok := t.beadUnderCursor(sh); ok {
			t.overlay, t.overlayTab, t.scroll = true, beadTabDetail, 0
			return true, nil
		}
	case "g":
		t.tree = t.tree.next()
		t.cursor = 0
		switch t.tree {
		case beadListBlocksTree:
			sh.setStatus("dependency tree — blockers sit above what they block", cDim)
		case beadListFamilyTree:
			sh.setStatus("family tree — parents sit above their children", cDim)
		default:
			sh.setStatus("flat list, grouped by what's ready", cDim)
		}
		if err := viewstate.SaveBeadTree(sh.manifestDir(), t.tree.viewString()); err != nil {
			sh.setStatus("view.toml: "+err.Error(), cRed)
		}
		return true, nil
	case "r":
		// The Beads tab has no per-row refresh: re-reading the whole workspace
		// is one local query, so there is nothing a single bead would save.
		// Routed through the feed's poll like the tick and the launch load, so
		// a manual refresh can't stack a second `bd list` on one in flight.
		return true, t.src.poll()
	}
	return false, nil
}

// body draws the tab: the overlay when it is up, otherwise the list with the
// strip for the bead under the cursor when there is room for it.
func (t *beadsTab) body(sh *shell, width, height int) ([]string, []hitTarget) {
	if t.overlay {
		content := t.renderBeadOverlay(sh, width, height)
		return content, noneHits(len(content))
	}
	rows := t.visibleRows(sh)
	if !t.showBeadStrip(rows, height) {
		return sh.renderRows(rows, t.cursor, width, height)
	}
	content, hits := sh.renderRows(rows, t.cursor, width, height-beadStripRows)
	for _, l := range t.beadStrip(sh, width) {
		content = append(content, l)
		hits = append(hits, hitTarget{kind: hitNone})
	}
	return content, hits
}

// footer replaces the dashboard's keybar while the overlay is up.
func (t *beadsTab) footer(int) (string, bool) {
	if t.overlay {
		return beadOverlayFooter(), true
	}
	return "", false
}

// count is the tab-bar badge: every bead the list can show.
func (t *beadsTab) count() int {
	human, ready, inProgress, blocked := groupBeads(t.src.list())
	return len(human) + len(ready) + len(inProgress) + len(blocked)
}

// loaded reports whether the tab has what it draws, so `admin show` never
// prints a "loading…" placeholder as if it were a row.
func (t *beadsTab) loaded() bool { return t.src.loaded() || t.src.err() != "" }

// freshness is the header's age readout. Beads are read straight out of the
// local database on a short clock, so there is no cache age to report — what
// is on screen is what the workspace said seconds ago. Unless the last poll
// was abandoned at beadsPollTimeout (a contended embedded Dolt): then what's
// on screen is whatever the poll before that one returned, so it reads stale.
func (t *beadsTab) freshness() seg {
	if t.src.stale() {
		return seg{t: "stale", fg: cYellow}
	}
	return seg{t: "live", fg: cFaint}
}

// update takes the messages the tab reads beyond its keys: the merged config
// names its columns.
func (t *beadsTab) update(msg tea.Msg) tea.Cmd {
	if msg, ok := msg.(settingsMsg); ok {
		t.cols = msg.beads.Columns
	}
	return nil
}
