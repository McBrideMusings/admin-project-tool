package tui

import "github.com/McBrideMusings/admin-project-tool/internal/livework"

// workerRowGlyph is the one-cell marker a Beads or Issues row carries while an
// agent worker is dispatched over it, in the Work tab's own vocabulary: ◈ for a
// worker, ◐ once it is waiting on a person. It outranks every other status the
// row's glyph cell could show, because a row with a live worker is the one
// that must not be dispatched again.
func workerRowGlyph(st livework.State) seg {
	switch st {
	case livework.StateWaiting:
		return seg{t: "◐", fg: cYellow}
	case livework.StateFinished, livework.StateStopped:
		return seg{t: "◈", fg: cDim}
	default:
		return seg{t: "◈", fg: cGreen}
	}
}
