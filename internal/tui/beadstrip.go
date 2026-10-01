package tui

import (
	"strconv"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
)

// beadStripRows is the height of the strip under the Beads list: a rule, the
// cursor bead's title on up to two lines, and its metadata.
const beadStripRows = 4

// beadStripMinBody is the smallest list-plus-strip height at which the strip is
// drawn. Below it the strip would leave the list under ten rows, so the list
// keeps the space.
const beadStripMinBody = 14

// beadStrip is what the list rows no longer carry for the bead under the cursor:
// its whole title and its type, labels, status, unblocks and age. The row gives
// the title every cell the id and age leave, and this strip is where a title too
// long for the row is read in full.
func (m Model) beadStrip(width int) []string {
	lines := make([]string, 0, beadStripRows)
	lines = append(lines, m.ruleLine(width))
	b, ok := m.beadUnderCursor()
	if !ok {
		for len(lines) < beadStripRows {
			lines = append(lines, "")
		}
		return lines
	}
	title := wrapWords(b.Title, max(width-2, 1))
	if len(title) > 2 {
		title = append(title[:1], truncR(strings.Join(title[1:], " "), max(width-2, 1)))
	}
	for len(title) < 2 {
		title = append(title, "")
	}
	for _, t := range title {
		lines = append(lines, renderSegs([]seg{{t: " " + t, fg: cFg}}, nil))
	}
	lines = append(lines, renderSegs(fitSegs(m.beadStripMeta(b), width), nil))
	return lines
}

func (m Model) beadStripMeta(b bd.Bead) []seg {
	segs := []seg{
		{t: " " + b.ID, fg: cCyan},
		{t: "  " + b.Type + "  " + priorityLabel(b.Priority) + "  " + b.Status, fg: cDim},
	}
	if len(b.Labels) > 0 {
		segs = append(segs, seg{t: "  " + strings.Join(b.Labels, ", "), fg: cDim})
	}
	if n := bd.Unblocks(m.beads)[b.ID]; n > 0 {
		segs = append(segs, seg{t: "  unblocks " + strconv.Itoa(n), fg: unblocksColor(n)})
	}
	if !b.Updated.IsZero() {
		segs = append(segs, seg{t: "  updated " + compactSince(b.Updated) + " ago", fg: cDim})
	}
	return segs
}

// showBeadStrip reports whether the Beads list gives up beadStripRows to the
// strip: only when the body is tall enough and there is a bead for the strip to
// describe. An empty list or a lone centered message keeps the whole body.
func (m Model) showBeadStrip(contentH int) bool {
	if m.active != tabBeads || contentH < beadStripMinBody {
		return false
	}
	for _, r := range m.visibleRows() {
		if r.kind == rowBead {
			return true
		}
	}
	return false
}
