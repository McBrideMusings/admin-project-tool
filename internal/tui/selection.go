package tui

// selPhase is where a mouse-drag text selection sits in its lifecycle.
type selPhase int

const (
	selNone     selPhase = iota // no gesture in progress
	selAnchored                 // mouse down, hasn't moved yet — might be a bare click
	selDragging                 // cursor has left the anchor cell — a real selection
)

// cell is one screen position: row then column, both zero-indexed against the
// full rendered frame.
type cell struct {
	row, col int
}

// selection is drag-to-copy's state machine: anchor on mouse-down, drag on
// motion, finish on mouse-up. It carries no more than an anchor cell, a
// current cell, and a phase — everything else (which text that covers, how it
// renders) is derived from those three by the caller.
type selection struct {
	anchor, cursor cell
	phase          selPhase
}

// anchorAt starts a potential selection at (row, col). Nothing is visible yet
// — the gesture might turn out to be a bare click.
func anchorAt(row, col int) selection {
	c := cell{row, col}
	return selection{anchor: c, cursor: c, phase: selAnchored}
}

// drag extends the selection to the mouse's current cell. The phase only
// advances to dragging once the cursor actually leaves the anchor cell, so a
// press-hold-release on one cell still finishes as a click.
func (s *selection) drag(row, col int) {
	s.cursor = cell{row, col}
	if s.cursor != s.anchor {
		s.phase = selDragging
	}
}

// finish ends the gesture and reports whether it was a real drag. false means
// a bare click — the anchor cell never moved — which is the caller's cue to
// treat the mouse-up as an activation instead of a copy.
func (s *selection) finish() bool {
	return s.phase == selDragging
}

// inProgress reports whether a mouse button is currently down for this
// selection — anchored or dragging, as opposed to no gesture at all.
func (s selection) inProgress() bool {
	return s.phase != selNone
}

// active reports whether the selection has anything to highlight or copy.
// A bare click (anchored, never dragged) never became visible.
func (s selection) active() bool {
	return s.phase == selDragging
}

// ordered returns the anchor/cursor pair in reading order (top-left first),
// so a backward drag highlights and extracts identically to a forward one.
func (s selection) ordered() (start, end cell) {
	if s.anchor.row < s.cursor.row || (s.anchor.row == s.cursor.row && s.anchor.col <= s.cursor.col) {
		return s.anchor, s.cursor
	}
	return s.cursor, s.anchor
}

// contains reports whether (row, col) falls inside the selection.
func (s selection) contains(row, col int) bool {
	if !s.active() {
		return false
	}
	start, end := s.ordered()
	if row < start.row || row > end.row {
		return false
	}
	switch {
	case start.row == end.row:
		return col >= start.col && col <= end.col
	case row == start.row:
		return col >= start.col
	case row == end.row:
		return col <= end.col
	default:
		return true
	}
}
