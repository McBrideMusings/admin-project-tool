package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
)

// beadSide says which of the row's two clusters a column belongs to, since
// composeLR lays a bead row out as a left group and a right group with the title
// absorbing everything between them.
type beadSide int

const (
	beadLeft beadSide = iota
	beadRight
)

// beadCtx is everything a column needs about the pane it is being drawn in, as
// opposed to the bead it is drawing. The workspace prefix comes off every id, and
// tree mode is what makes the status glyph occupy a cell at all.
//
// unblocks is the one property a column needs that isn't on the bead: how much
// work it frees is a fact about the whole graph, so it is computed once per fetch
// and handed down rather than recomputed per row.
type beadCtx struct {
	prefix   string
	tree     bool
	unblocks map[string]int
}

// beadColumn is one column of the Beads pane: how wide it wants to be for a given
// bead, and how it renders. The pane walks one ordered list of these twice — once
// to size, once to emit — so the two passes cannot disagree about which columns
// exist or what order they sit in.
//
// The title is the one column with no cell of its own: it takes whatever the
// others leave, and its contents (the tree indent, the "+N" blocker count) are
// spent out of that budget by the row builder.
type beadColumn struct {
	name  string
	side  beadSide
	width func(b bd.Bead, c beadCtx) int
	cell  func(b bd.Bead, w int, c beadCtx) []seg
}

// DefaultBeadColumns is the pane as it ships. "owner" is absent: bd fills it from
// the `owner` field, which on a solo workspace is the same name on every row.
// Anyone sharing a workspace puts it back through [beads] columns. "type",
// "labels" and "unblocks" are absent for the same reason as the title's width: the
// row gives the title every cell it can, and the strip under the list
// (beadstrip.go) shows those three for the bead under the cursor.
var DefaultBeadColumns = []string{"id", "priority", "glyph", "title", "age"}

// beadColumnByName is every column the pane knows how to draw. A name outside
// this map is configuration naming something that does not exist, which is
// dropped rather than refused — a typo in one entry should cost that column, not
// the pane.
var beadColumnByName = map[string]beadColumn{
	"id": {
		name: "id", side: beadLeft,
		width: func(b bd.Bead, c beadCtx) int { return vlen(shortBeadID(b.ID, c.prefix)) },
		cell: func(b bd.Bead, w int, c beadCtx) []seg {
			return []seg{{t: padEnd(shortBeadID(b.ID, c.prefix), w), fg: cCyan}}
		},
	},
	"priority": {
		name: "priority", side: beadLeft,
		// "P0"…"P4" is every value there is, so the column is two cells whatever
		// the list holds.
		width: func(bd.Bead, beadCtx) int { return 2 },
		cell: func(b bd.Bead, w int, _ beadCtx) []seg {
			return []seg{{t: padEnd(priorityLabel(b.Priority), w), fg: priorityColor(b.Priority)}}
		},
	},
	"type": {
		name: "type", side: beadLeft,
		width: func(b bd.Bead, _ beadCtx) int { return vlen(b.Type) },
		cell: func(b bd.Bead, w int, _ beadCtx) []seg {
			return []seg{{t: padEnd(b.Type, w), fg: cDim}}
		},
	},
	"glyph": {
		name: "glyph", side: beadLeft,
		// One cell, and only in tree mode: the flat list says "in progress" with a
		// section heading, so the marker would be repeating what the heading said.
		width: func(_ bd.Bead, c beadCtx) int {
			if c.tree {
				return 1
			}
			return 0
		},
		cell: func(b bd.Bead, w int, _ beadCtx) []seg {
			return []seg{{t: padEnd(beadStatusGlyph(b), w), fg: beadGlyphColor(b)}}
		},
	},
	"title": {name: "title", side: beadLeft},
	"labels": {
		name: "labels", side: beadRight,
		width: func(b bd.Bead, _ beadCtx) int { return vlen(strings.Join(b.Labels, ", ")) },
		cell: func(b bd.Bead, w int, _ beadCtx) []seg {
			return []seg{{t: padEnd(truncR(strings.Join(b.Labels, ", "), w), w), fg: cDim}}
		},
	},
	"unblocks": {
		name: "unblocks", side: beadRight,
		// Exactly as wide as the widest count in the list — one cell while nothing
		// frees ten or more, which is the common case. A bead that frees nothing
		// prints nothing: "0" on three rows in four is noise, and blank is what
		// makes the rows that do free work visible by scanning.
		width: func(b bd.Bead, c beadCtx) int { return vlen(unblocksText(b, c)) },
		cell: func(b bd.Bead, w int, c beadCtx) []seg {
			return padStart(seg{t: unblocksText(b, c), fg: unblocksColor(c.unblocks[b.ID])}, w)
		},
	},
	"owner": {
		name: "owner", side: beadRight,
		width: func(b bd.Bead, _ beadCtx) int { return vlen(b.Assignee) },
		cell: func(b bd.Bead, w int, _ beadCtx) []seg {
			return []seg{{t: padEnd(b.Assignee, w), fg: cMag}}
		},
	},
	"age": {
		name: "age", side: beadRight,
		// Always beadAgeW, even where a bead has no timestamp: the age is the
		// right-hand edge of every row, and a column that shrank on one workspace
		// would move every other column with it.
		width: func(bd.Bead, beadCtx) int { return beadAgeW },
		cell: func(b bd.Bead, w int, _ beadCtx) []seg {
			return padStart(seg{t: compactSince(b.Updated), fg: cDim}, w)
		},
	},
}

// beadGlyphColor tints the glyph column's one cell: a human bead gets the
// same yellow the rest of the pane reserves for something wanting attention,
// so the cell that marks "blocked on you" doesn't recede into the ordinary
// cyan every other glyph value uses.
func beadGlyphColor(b bd.Bead) lipgloss.TerminalColor {
	if beadNeedsHuman(b) {
		return cYellow
	}
	return cCyan
}

// beadColumnsFor resolves configured names to columns, dropping unknown ones and
// forcing "title" in when it was left out — a pane with no title is a pane of
// identifiers, which is never what a missing entry meant. An empty list means the
// default set.
func beadColumnsFor(names []string) []beadColumn {
	if len(names) == 0 {
		names = DefaultBeadColumns
	}
	out := make([]beadColumn, 0, len(names)+1)
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		col, ok := beadColumnByName[n]
		if !ok || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, col)
	}
	if !seen["title"] {
		out = append(out, beadColumnByName["title"])
	}
	return out
}

// beadLayout is one pane's resolved columns and the width each of them got. Rows
// are emitted from it, so the sequence a row draws is by construction the
// sequence the widths were computed for.
type beadLayout struct {
	ctx    beadCtx
	cols   []beadColumn
	widths []int // parallel to cols; the title's entry is its resolved flex width
}

// beadLayoutFor sizes every column once across all the groups the pane will draw,
// so a value sits in the same cells on every line and a column can be read by
// scanning down it. The labels column takes whatever is left after the others and
// the minimum title, and is dropped entirely on a narrow terminal — the same
// budget the Issues tab keeps, so the two panes narrow the same way.
func beadLayoutFor(names []string, cols int, c beadCtx, groups ...[]bd.Bead) beadLayout {
	l := beadLayout{ctx: c, cols: beadColumnsFor(names)}
	l.widths = make([]int, len(l.cols))
	for i, col := range l.cols {
		if col.width == nil {
			continue
		}
		// Seeded from the zero bead so a column whose width is a constant keeps it
		// on an empty workspace.
		l.widths[i] = col.width(bd.Bead{}, c)
	}
	natLabel := 0
	for _, g := range groups {
		for _, b := range g {
			for i, col := range l.cols {
				if col.width == nil {
					continue
				}
				w := col.width(b, c)
				if col.name == "labels" {
					natLabel = max(natLabel, w)
					continue
				}
				l.widths[i] = max(l.widths[i], w)
			}
		}
	}

	li := l.indexOf("labels")
	minTitle := 20
	if cols >= 72 {
		minTitle = 28
	}
	right := l.rightW(0)
	maxLabel := max(cols-l.fixedW()-right-minTitle, 0)
	if li >= 0 {
		if cols >= 58 {
			l.widths[li] = natLabel
		}
		if l.widths[li] > maxLabel {
			switch {
			case cols >= 72:
				l.widths[li] = max(13, maxLabel)
			case cols >= 58:
				l.widths[li] = min(13, maxLabel)
			default:
				l.widths[li] = 0
			}
		}
	}
	return l
}

// indexOf is the position of a named column in the resolved list, or -1.
func (l beadLayout) indexOf(name string) int {
	for i, col := range l.cols {
		if col.name == name {
			return i
		}
	}
	return -1
}

// fixedW is everything to the left of the title that never truncates: the cursor
// cell renderRow prepends, the row's own leading space, each present left column
// with the two spaces that separate it from what follows — plus the two cells
// left between the left and right clusters, which composeLR will not give up (it
// keeps at least one, and a single cell reads as a collision).
func (l beadLayout) fixedW() int {
	w := 1 + 1
	for i, col := range l.cols {
		if col.side != beadLeft || col.name == "title" || l.widths[i] <= 0 {
			continue
		}
		w += l.widths[i] + 2
	}
	return w + 2
}

// rightW is the width of the right-hand cluster at a given labels width, with a
// two-cell gap between each pair of present columns. The last one runs to the
// row's right edge, so it carries no gap of its own.
func (l beadLayout) rightW(labelW int) int {
	w, n := 0, 0
	for i, col := range l.cols {
		if col.side != beadRight {
			continue
		}
		cw := l.widths[i]
		if col.name == "labels" {
			cw = labelW
		}
		if cw <= 0 {
			continue
		}
		if n > 0 {
			w += 2
		}
		w += cw
		n++
	}
	return w
}

// titleW is what the title column has left once the fixed columns and the right
// cluster have taken theirs. Nothing trails the title any more: the blocker tail
// that used to sit here charged every row in the pane for a column that only ever
// had content inside the Blocked section.
func (l beadLayout) titleW(cols int) int {
	return flexWidth(cols, l.fixedW(), l.rightW(l.labelsW()), 4)
}

// labelsW is the resolved labels width, or 0 when the column is not drawn.
func (l beadLayout) labelsW() int {
	if i := l.indexOf("labels"); i >= 0 {
		return l.widths[i]
	}
	return 0
}

// row lays out one bead: the left columns, then the title, then the blocker it
// waits on trailing the title, and the right columns in their own fixed cells.
//
// Tree mode changes only what sits inside the title cell: the indent and the
// branch glyph are spent out of the title's own budget, so every other column
// holds the same cells it holds in the flat list and the pane is still a table.
func (l beadLayout) row(n beadNode, cols int) (left, right []seg) {
	b, c := n.b, l.ctx
	for i, col := range l.cols {
		if col.side != beadRight || l.widths[i] <= 0 {
			continue
		}
		if len(right) > 0 {
			right = append(right, seg{t: "  "})
		}
		right = append(right, col.cell(b, l.widths[i], c)...)
	}

	titleW := l.titleW(cols)

	// The indent and the "+N" are inside the title cell, so they come out of the
	// title's budget before it is fitted — nothing to the right of it moves.
	var indent, extra string
	if c.tree {
		indent = strings.Repeat("  ", min(n.depth, beadTreeMaxDepth))
		if n.depth > 0 {
			indent += "└╴ "
		}
		if n.extra > 0 {
			extra = "  +" + strconv.Itoa(n.extra)
		}
	}
	title := truncR(b.Title, max(titleW-vlen(indent)-vlen(extra), 0))

	left = []seg{{t: " "}}
	for i, col := range l.cols {
		if col.side != beadLeft || col.name == "title" || l.widths[i] <= 0 {
			continue
		}
		left = append(left, col.cell(b, l.widths[i], c)...)
		left = append(left, seg{t: "  "})
	}
	left = append(left, seg{t: indent, fg: cDim}, seg{t: title, fg: cFg})
	if extra != "" {
		left = append(left, seg{t: extra, fg: cYellow})
	}
	return left, right
}
