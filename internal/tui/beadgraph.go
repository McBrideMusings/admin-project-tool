package tui

import (
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
)

// The graph's fixed geometry. Ranks run left to right because the overlay is
// wide and short. beadRankGap stays outside the geometry split below: the
// edge-routing channel logic in render reads it directly and it does not
// shrink between full and compact.
const (
	beadRankGap = 6 // cells between one rank's right edge and the next rank's left
)

// beadGeom is one of the graph's two node geometries: how many rows a node
// takes, how many rows separate two node tops in the same rank, and the
// narrowest a node may draw before it holds nothing but an id.
type beadGeom struct {
	nodeH   int // rows per node: 3 (border/label/border) or 1 (bare id)
	rowGap  int // rows between two nodes' tops in one rank
	nodeMin int // narrowest drawable node at this geometry
}

// labelRow is the row within a node, relative to its top, that holds the id —
// the centre row of a bordered box, the node's only row when compact. Edges,
// arrowheads and text all terminate here so they land on a row the node
// actually occupies.
func (g beadGeom) labelRow() int { return g.nodeH / 2 }

var (
	// beadGeomFull is a three-row bordered box with an id and a title.
	beadGeomFull = beadGeom{nodeH: 3, rowGap: 4, nodeMin: 14}
	// beadGeomCompact is one row, id only, on a tighter row gap — tried when a
	// neighbourhood does not fit as bordered boxes.
	beadGeomCompact = beadGeom{nodeH: 1, rowGap: 2, nodeMin: 8}
)

// beadNeighbourhood is the selected bead's neighbourhood: everything it waits on
// transitively, plus everything that waits on it. Not the whole workspace, and
// not the whole chain — a bead two hops off to the side of a blocker is not part
// of why this bead is stuck.
//
// Only open "blocks" links count, the same rule the Blocked section reads by: a
// blocker that has been closed is gone from the list, so it is gone from here.
func beadNeighbourhood(beads []bd.Bead, id string) []bd.Bead {
	byID := make(map[string]bd.Bead, len(beads))
	for _, b := range beads {
		byID[b.ID] = b
	}
	if _, ok := byID[id]; !ok {
		return nil
	}
	dependents := make(map[string][]string, len(beads))
	for _, b := range beads {
		for _, blocker := range b.Blockers {
			if _, open := byID[blocker]; open {
				dependents[blocker] = append(dependents[blocker], b.ID)
			}
		}
	}

	keep := map[string]bool{id: true}
	var walk func(from string, next func(string) []string)
	walk = func(from string, next func(string) []string) {
		for _, to := range next(from) {
			if _, open := byID[to]; !open || keep[to] {
				continue
			}
			keep[to] = true
			walk(to, next)
		}
	}
	walk(id, func(s string) []string { return byID[s].Blockers })
	walk(id, func(s string) []string { return dependents[s] })

	out := make([]bd.Bead, 0, len(keep))
	for _, b := range beads {
		if keep[b.ID] {
			out = append(out, b)
		}
	}
	return out
}

// beadGraphNode is one placed node: which bead, which rank, and the top-left cell
// of its three-row box.
type beadGraphNode struct {
	b    bd.Bead
	rank int
	x, y int
}

// beadGraphLayout places a neighbourhood. Rank 0 is work nothing in the
// neighbourhood blocks; every other rank sits one column right of the last thing
// it waits on. Order within a rank follows the row its first blocker landed on,
// so edges cross as little as possible.
//
// nodeW is the width every box takes; ranks is how many columns deep the graph
// runs; rows is how many rows the tallest rank needs. Callers decide from those
// three whether the graph fits before anything is drawn.
type beadGraphLayout struct {
	nodes  []beadGraphNode
	edges  [][2]string // src → dst, both in nodes
	nodeW  int
	ranks  int
	rows   int
	byID   map[string]beadGraphNode
	sel    string
	blocks map[string]bool // ids with at least one open blocker in the neighbourhood
	geom   beadGeom        // the geometry this layout was placed at
}

// beadGraphLayoutFor ranks and places the neighbourhood inside a width of w
// cells at the given geometry. It always returns a layout: whether it fits is
// the caller's question, answered from ranks, rows and nodeW.
func beadGraphLayoutFor(nodes []bd.Bead, sel string, w int, geom beadGeom) beadGraphLayout {
	l := beadGraphLayout{sel: sel, geom: geom, byID: map[string]beadGraphNode{}, blocks: map[string]bool{}}
	if len(nodes) == 0 {
		return l
	}
	in := make(map[string]bd.Bead, len(nodes))
	for _, b := range nodes {
		in[b.ID] = b
	}
	// Blockers restricted to the neighbourhood: a link out of it is not an edge
	// this graph can draw, and pretending otherwise would rank a node behind
	// something that is not on screen.
	kept := make(map[string][]string, len(nodes))
	for _, b := range nodes {
		for _, id := range b.Blockers {
			if _, ok := in[id]; ok {
				kept[b.ID] = append(kept[b.ID], id)
				l.blocks[b.ID] = true
				l.edges = append(l.edges, [2]string{id, b.ID})
			}
		}
	}

	rank := make(map[string]int, len(nodes))
	var rankOf func(id string, seen map[string]bool) int
	rankOf = func(id string, seen map[string]bool) int {
		if r, ok := rank[id]; ok {
			return r
		}
		// A cycle is not a thing beads can express through blocks links, but a
		// database that grew one must not hang the dashboard.
		if seen[id] {
			return 0
		}
		seen[id] = true
		r := 0
		for _, blocker := range kept[id] {
			if br := rankOf(blocker, seen); br+1 > r {
				r = br + 1
			}
		}
		rank[id] = r
		return r
	}
	for _, b := range nodes {
		rankOf(b.ID, map[string]bool{})
		if rank[b.ID]+1 > l.ranks {
			l.ranks = rank[b.ID] + 1
		}
	}

	l.nodeW = (w - (l.ranks-1)*beadRankGap) / l.ranks

	byRank := make([][]bd.Bead, l.ranks)
	for _, b := range nodes {
		byRank[rank[b.ID]] = append(byRank[rank[b.ID]], b)
	}
	for r, group := range byRank {
		if r == 0 {
			sort.Slice(group, func(i, j int) bool { return group[i].ID < group[j].ID })
		} else {
			// The row a node's first blocker landed on is already known, because
			// rank r-1 and below are placed before rank r is ordered.
			sort.SliceStable(group, func(i, j int) bool {
				return firstBlockerY(l.byID, kept[group[i].ID]) < firstBlockerY(l.byID, kept[group[j].ID])
			})
		}
		for i, b := range group {
			n := beadGraphNode{b: b, rank: r, x: r * (l.nodeW + beadRankGap), y: 1 + i*geom.rowGap}
			l.nodes = append(l.nodes, n)
			l.byID[b.ID] = n
		}
		if rows := 1 + (len(group)-1)*geom.rowGap + geom.nodeH; rows > l.rows {
			l.rows = rows
		}
	}
	return l
}

// firstBlockerY is the row a node's first already-placed blocker sits on, or a
// large number when none of them is placed yet — which sorts it after the ones
// whose position is known rather than pinning it to the top.
func firstBlockerY(placed map[string]beadGraphNode, blockers []string) int {
	for _, id := range blockers {
		if n, ok := placed[id]; ok {
			return n.y
		}
	}
	return 1 << 30
}

// beadGraphRefusal is why a graph cannot be drawn in w×h cells, or "" when it
// can. It names the dimension that ran out and the real numbers on both sides,
// because "does not fit" alone leaves nothing to act on — a wider terminal fixes
// one of these and not the other.
func (l beadGraphLayout) refusal(w, h int) string {
	if len(l.nodes) == 0 {
		return "no dependencies — nothing blocks this bead and nothing waits on it"
	}
	if l.nodeW < l.geom.nodeMin {
		need := l.ranks*l.geom.nodeMin + (l.ranks-1)*beadRankGap
		return strconv.Itoa(len(l.nodes)) + " beads over " + strconv.Itoa(l.ranks) +
			" ranks — needs " + strconv.Itoa(need) + " cells, the box has " + strconv.Itoa(w)
	}
	if l.rows > h {
		return strconv.Itoa(len(l.nodes)) + " beads over " + strconv.Itoa(l.ranks) +
			" ranks — the widest rank needs " + strconv.Itoa(l.rows) + " rows, the box has " + strconv.Itoa(h)
	}
	return ""
}

// beadGrid is the character grid the graph is drawn onto. Box-drawing characters
// are held as a bitmask of the directions leaving the cell rather than as runes,
// so two lines meeting on one cell resolve into the junction they form — writing
// the second rune over the first would lose it, turning a fork into a corner.
type beadGrid struct {
	w, h int
	mask []uint8
	ch   []rune
	fg   []lipgloss.TerminalColor
}

// Directions leaving a cell.
const (
	dirUp uint8 = 1 << iota
	dirRight
	dirDown
	dirLeft
)

// beadJunctions maps a direction set to the character that draws it.
var beadJunctions = [16]rune{
	0: ' ', 1: '│', 2: '─', 3: '└', 4: '│', 5: '│', 6: '┌', 7: '├',
	8: '─', 9: '┘', 10: '─', 11: '┴', 12: '┐', 13: '┤', 14: '┬', 15: '┼',
}

func newBeadGrid(w, h int) *beadGrid {
	g := &beadGrid{w: w, h: h, mask: make([]uint8, w*h), ch: make([]rune, w*h), fg: make([]lipgloss.TerminalColor, w*h)}
	for i := range g.ch {
		g.ch[i] = ' '
	}
	return g
}

func (g *beadGrid) in(x, y int) bool { return x >= 0 && y >= 0 && x < g.w && y < g.h }

// line ORs directions into a cell and redraws it from the merged set.
func (g *beadGrid) line(x, y int, dirs uint8, fg lipgloss.TerminalColor) {
	if !g.in(x, y) {
		return
	}
	i := y*g.w + x
	g.mask[i] |= dirs
	g.ch[i] = beadJunctions[g.mask[i]]
	if g.fg[i] == nil {
		g.fg[i] = fg
	}
}

// put writes a literal character, clearing any junction state under it. Used for
// text and for arrowheads, neither of which merges with anything.
func (g *beadGrid) put(x, y int, r rune, fg lipgloss.TerminalColor) {
	if !g.in(x, y) {
		return
	}
	i := y*g.w + x
	g.mask[i] = 0
	g.ch[i] = r
	g.fg[i] = fg
}

func (g *beadGrid) text(x, y int, s string, fg lipgloss.TerminalColor) {
	for _, r := range s {
		g.put(x, y, r, fg)
		x++
	}
}

// hline draws a horizontal run from x0 to x1 inclusive. A run with x1 < x0 is
// zero-length and is not drawn at all: normalising the ends instead would paint
// backwards over whatever sits to the left, which is how a ┬ becomes a ┼.
func (g *beadGrid) hline(x0, x1, y int, fg lipgloss.TerminalColor) {
	for x := x0; x <= x1; x++ {
		g.line(x, y, dirLeft|dirRight, fg)
	}
}

// vline draws a vertical run from y0 to y1 inclusive, and nothing at all when
// y1 < y0. Same rule as hline, same defect it prevents.
func (g *beadGrid) vline(x, y0, y1 int, fg lipgloss.TerminalColor) {
	for y := y0; y <= y1; y++ {
		g.line(x, y, dirUp|dirDown, fg)
	}
}

// beadNodeColor tones a node by what it says about the work: green is ready,
// cyan is moving, blue is the bead the overlay was opened on, dim is waiting.
func (l beadGraphLayout) nodeColor(b bd.Bead) lipgloss.TerminalColor {
	switch {
	case b.ID == l.sel:
		return cBlue
	case b.Status == "in_progress":
		return cCyan
	case !l.blocks[b.ID]:
		return cGreen
	default:
		return cDim
	}
}

// render draws the graph into w×h cells. Edges go down first, then the node
// boxes over them, and only then the arrowheads — an arrowhead drawn while its
// edge is being routed is erased by the border it points at.
func (l beadGraphLayout) render(w, h int, prefix string) [][]seg {
	g := newBeadGrid(w, h)

	// One vertical channel per source, so two edges leaving the same box never
	// share a column and overlap. The channels sit in the gap between the source's
	// rank and the next; the last cell of that gap belongs to the arrowhead.
	slot := map[string]int{}
	perRank := map[int]int{}
	for _, n := range l.nodes {
		slot[n.b.ID] = 1 + perRank[n.rank]%(beadRankGap-2)
		perRank[n.rank]++
	}

	labelRow := l.geom.labelRow()
	for _, e := range l.edges {
		src, dst := l.byID[e[0]], l.byID[e[1]]
		fg := l.nodeColor(src.b)
		cx := src.x + l.nodeW + slot[src.b.ID]
		y1, y2 := src.y+labelRow, dst.y+labelRow
		g.hline(src.x+l.nodeW, cx-1, y1, fg)
		switch {
		case y1 == y2:
			g.line(cx, y1, dirLeft|dirRight, fg)
		case y2 > y1:
			g.line(cx, y1, dirLeft|dirDown, fg)
			g.vline(cx, y1+1, y2-1, fg)
			g.line(cx, y2, dirUp|dirRight, fg)
		default:
			g.line(cx, y1, dirLeft|dirUp, fg)
			g.vline(cx, y2+1, y1-1, fg)
			g.line(cx, y2, dirDown|dirRight, fg)
		}
		g.hline(cx+1, dst.x-2, y2, fg)
	}

	// Boxes paint OVER edges rather than merging with them: an edge that spans
	// more than one rank crosses the column an intermediate box sits in, and a
	// merged border reads as a line passing through the box.
	for _, n := range l.nodes {
		fg := l.nodeColor(n.b)
		x0, x1 := n.x, n.x+l.nodeW-1
		if l.geom.nodeH == 1 {
			g.text(x0, n.y, padEnd(truncR(shortBeadID(n.b.ID, prefix), l.nodeW), l.nodeW), fg)
			continue
		}
		g.put(x0, n.y, '┌', fg)
		g.put(x1, n.y, '┐', fg)
		g.put(x0, n.y+2, '└', fg)
		g.put(x1, n.y+2, '┘', fg)
		for x := x0 + 1; x < x1; x++ {
			g.put(x, n.y, '─', fg)
			g.put(x, n.y+2, '─', fg)
		}
		g.put(x0, n.y+1, '│', fg)
		g.put(x1, n.y+1, '│', fg)
		label := " " + shortBeadID(n.b.ID, prefix) + " " + n.b.Title
		g.text(x0+1, n.y+1, padEnd(truncR(label, l.nodeW-2), l.nodeW-2), fg)
	}

	// Arrowheads last: the box borders have already been painted, so nothing is
	// left to erase them.
	for _, e := range l.edges {
		dst := l.byID[e[1]]
		g.put(dst.x-1, dst.y+labelRow, '▸', l.nodeColor(l.byID[e[0]].b))
	}
	return g.lines()
}

// lines turns the grid into rendered segments, one run per colour so a node and
// the edges leaving it read as one colour rather than per-cell noise.
func (g *beadGrid) lines() [][]seg {
	out := make([][]seg, g.h)
	for y := 0; y < g.h; y++ {
		var line []seg
		var b strings.Builder
		var fg lipgloss.TerminalColor
		flush := func() {
			if b.Len() > 0 {
				line = append(line, seg{t: b.String(), fg: fg})
				b.Reset()
			}
		}
		for x := 0; x < g.w; x++ {
			i := y*g.w + x
			cf := g.fg[i]
			if cf == nil {
				cf = cDim
			}
			if cf != fg {
				flush()
				fg = cf
			}
			b.WriteRune(g.ch[i])
		}
		flush()
		out[y] = line
	}
	return out
}
