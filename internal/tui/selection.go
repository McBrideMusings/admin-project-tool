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
// current cell, a phase, and the cell before the current one — everything
// else (which text that covers, how it renders) is derived from those by the
// caller.
type selection struct {
	anchor, cursor, prevCursor cell
	phase                      selPhase
}

// anchorAt starts a potential selection at (row, col). Nothing is visible yet
// — the gesture might turn out to be a bare click.
func anchorAt(row, col int) selection {
	c := cell{row, col}
	return selection{anchor: c, cursor: c, prevCursor: c, phase: selAnchored}
}

// drag extends the selection to the mouse's current cell. The phase only
// advances to dragging once the cursor actually leaves the anchor cell, so a
// press-hold-release on one cell still finishes as a click. prevCursor keeps
// the cell just before this one, purely so extendToEdge can later tell which
// way the gesture was heading when its events stopped.
func (s *selection) drag(row, col int) {
	s.prevCursor = s.cursor
	s.cursor = cell{row, col}
	if s.cursor != s.anchor {
		s.phase = selDragging
	}
}

// direction reports the sign of the last motion step on each axis: -1, 0, or
// 1. Both zero means the last sample didn't move (or there's only ever been
// one), which extendToEdge treats as "no edge implied on that axis."
func (s selection) direction() (dRow, dCol int) {
	return sign(s.cursor.row - s.prevCursor.row), sign(s.cursor.col - s.prevCursor.col)
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	default:
		return 0
	}
}

// colEdge is stood in for "the far right of whatever line this row turns out
// to be" — selRangeOnLine already clamps any column past a line's real width
// down to that width, so a plain sentinel does the job without this package
// needing to know real line widths.
const colEdge = 1 << 30

// extendToEdge pushes cursor out to the frame's edge along whichever axis
// the last motion sample was heading — the best available guess at where an
// off-window drag was aimed once its events stop arriving. maxRow must be a
// real, non-negative row index (typically len(lines)-1); the caller is
// responsible for not calling this against an empty frame.
func (s *selection) extendToEdge(maxRow int) {
	dRow, dCol := s.direction()
	switch {
	case dRow > 0:
		s.cursor.row = maxRow
	case dRow < 0:
		s.cursor.row = 0
	}
	switch {
	case dCol > 0:
		s.cursor.col = colEdge
	case dCol < 0:
		s.cursor.col = 0
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
