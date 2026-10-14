package tui

import (
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
)

// beadProseW is the widest the overlay's prose is allowed to run. The box is
// sized for the graph, which wants every cell it can get; a description is a
// different thing entirely, and at a 200-cell terminal the box is 176 wide and
// 172-character lines are unreadable. The text keeps a measure and the rest of
// the box stays empty. The asymmetry is deliberate.
const beadProseW = 92

// beadOverlayTab is which of the box's three tabs is up. A bool cannot name
// three states.
type beadOverlayTab int

const (
	beadTabDetail beadOverlayTab = iota
	beadTabGraph
	beadTabBlocking
	beadTabCount // the number of tabs, used to wrap the cycle
)

// beadOverlaySize is the box's geometry. It is derived from the terminal and
// never from the content: a modal that grows to what it holds is a modal whose
// size you cannot predict, and switching between the bead and its graph then
// reads as the screen jumping.
func beadOverlaySize(width, height int) (boxW, boxH int) {
	boxW = clampi(40, width-2*maxi(4, width*6/100), 220)
	boxH = maxi(height-2*maxi(2, height*10/100), 12)
	return mini(boxW, width), mini(boxH, height)
}

// beadUnderCursor is the bead the overlay was opened on, found by the id the row
// carries rather than by cursor arithmetic, so a refresh that reorders the list
// cannot swap the bead out from under an open overlay.
func (t beadsTab) beadUnderCursor(sh *shell) (bd.Bead, bool) {
	r, ok := sh.rowAt(t.visibleRows(sh), t.cursor)
	if !ok || r.kind != rowBead {
		return bd.Bead{}, false
	}
	for _, b := range t.src.list() {
		if b.ID == r.beadID {
			return b, true
		}
	}
	return bd.Bead{}, false
}

// renderBeadOverlay draws the fixed box over the pane: the bead's own text, its
// dependency neighbourhood, or what waits on it, with all three named in the
// top border.
func (t beadsTab) renderBeadOverlay(sh *shell, width, height int) []string {
	boxW, boxH := beadOverlaySize(width, height)
	innerW, innerH := boxW-2, boxH-2
	// The last inner row is the box's own help line, so the content gets one fewer.
	contentH := maxi(innerH-1, 1)

	b, ok := t.beadUnderCursor(sh)
	var lines [][]seg
	switch {
	case !ok:
		lines = [][]seg{{{t: " no bead under the cursor", fg: cDim}}}
	case t.overlayTab == beadTabGraph:
		lines = t.beadGraphLines(b, innerW, contentH)
	case t.overlayTab == beadTabBlocking:
		lines = t.beadBlockingLines(b, innerW)
	default:
		lines = beadTextLines(b, innerW)
	}

	scroll := clampi(0, t.scroll, maxi(len(lines)-contentH, 0))
	out := make([]string, 0, boxH)
	out = append(out, t.beadOverlayTop(boxW))
	border := renderSegs([]seg{{t: "│", fg: cBlue}}, nil)
	for i := 0; i < contentH; i++ {
		var row []seg
		if i == contentH-1 && len(lines) > scroll+contentH {
			more := len(lines) - (scroll + contentH - 1)
			row = []seg{{t: " ↓ " + strconv.Itoa(more) + " more — ↑↓ scrolls", fg: cDim}}
		} else if li := scroll + i; li < len(lines) {
			row = lines[li]
		}
		out = append(out, border+renderSegs(fitSegs(row, innerW), nil)+border)
	}
	out = append(out, border+renderSegs(fitSegs([]seg{
		{t: " ←  →", fg: cCyan}, {t: "  switch", fg: cDim},
		{t: "      ↑↓", fg: cCyan}, {t: "  scroll", fg: cDim},
		{t: "      esc", fg: cCyan}, {t: "  close", fg: cDim},
	}, innerW), nil)+border)
	out = append(out, renderSegs([]seg{{t: "└" + strings.Repeat("─", innerW) + "┘", fg: cBlue}}, nil))

	lp := padN(clampi(0, (width-boxW)/2, width))
	top := clampi(0, (height-boxH)/2, height)
	frame := make([]string, height)
	for i := top; i < height && i-top < len(out); i++ {
		frame[i] = lp + out[i-top]
	}
	return frame
}

// beadOverlayTop draws "┌─[ Bead ]─[ Graph ]─[ Blocking ]──…── [ ✕ ]─┐". The
// three tabs live in the border rather than in a row of their own: the box is
// fixed-size, so a row spent on chrome is a row taken off the content in every
// tab forever.
func (t beadsTab) beadOverlayTop(boxW int) string {
	tab := func(label string, on bool) []seg {
		if on {
			return []seg{{t: "[ " + label + " ]", fg: cBg, bg: cBlue}}
		}
		return []seg{{t: "[ " + label + " ]", fg: cDim}}
	}
	segs := []seg{{t: "┌─", fg: cBlue}}
	segs = append(segs, tab("Bead", t.overlayTab == beadTabDetail)...)
	segs = append(segs, seg{t: "─", fg: cBlue})
	segs = append(segs, tab("Graph", t.overlayTab == beadTabGraph)...)
	segs = append(segs, seg{t: "─", fg: cBlue})
	segs = append(segs, tab("Blocking", t.overlayTab == beadTabBlocking)...)
	// The close affordance sits at a fixed offset from the right edge so it does
	// not move when the tab labels do.
	used := segsWidth(segs)
	dashes := maxi(boxW-7-used, 0)
	segs = append(segs, seg{t: strings.Repeat("─", dashes), fg: cBlue})
	segs = append(segs, seg{t: "[ ✕ ]", fg: cDim}, seg{t: "─┐", fg: cBlue})
	return renderSegs(fitSegs(segs, boxW), nil)
}

// beadTextLines is the bead itself: its title, the one-line summary of what kind
// of work it is, and the prose bd already sent with the list — no second
// subprocess for a bead that is already in memory.
func beadTextLines(b bd.Bead, innerW int) [][]seg {
	proseW := mini(innerW-1, beadProseW)
	var lines [][]seg
	add := func(segs ...seg) { lines = append(lines, append([]seg{{t: " "}}, segs...)) }

	add(seg{t: truncR(b.Title, innerW-1), fg: cBright})
	meta := []string{b.Type, priorityLabel(b.Priority), b.Status}
	if b.Assignee != "" {
		meta = append(meta, b.Assignee)
	}
	add(seg{t: truncR(strings.Join(meta, " · "), innerW-1), fg: cDim})
	lines = append(lines, nil)

	lines = append(lines, beadProse(b.Description, proseW)...)
	if strings.TrimSpace(b.AcceptanceCriteria) != "" {
		lines = append(lines, nil)
		rule := maxi(proseW-vlen("Acceptance criteria")-1, 0)
		add(seg{t: "Acceptance criteria ", fg: cBright}, seg{t: strings.Repeat("─", rule), fg: cRule})
		lines = append(lines, beadProse(b.AcceptanceCriteria, proseW)...)
	}
	return lines
}

// beadProse renders a bead's markdown as terminal lines wrapped to a measure.
//
// bd stores descriptions hard-wrapped at whatever width they were written at, so
// wrapping line by line leaves a one-word orphan wherever the author's break
// falls before ours. Consecutive lines of one paragraph are reflowed into a
// single block first, and only then wrapped. A "#" heading takes the blue every
// heading in this dashboard takes; nothing else is interpreted, because these are
// terminal lines and not a markdown renderer.
func beadProse(s string, w int) [][]seg {
	var out [][]seg
	for _, blk := range beadBlocks(s) {
		if blk == "" {
			out = append(out, nil)
			continue
		}
		fg := cFg
		if strings.HasPrefix(strings.TrimSpace(blk), "#") {
			fg = cBlue
		}
		for _, line := range wrapWords(blk, w) {
			out = append(out, []seg{{t: " " + line, fg: fg}})
		}
	}
	return out
}

// beadBlocks groups markdown lines into the units that get wrapped as one: a
// blank line ends a block, and so does a line that opens a new one — a heading, a
// bullet, a numbered item, a quote. Everything else is a continuation of the
// paragraph above it and is joined onto it.
func beadBlocks(s string) []string {
	var out []string
	cur := ""
	flush := func() {
		if cur != "" {
			out = append(out, cur)
			cur = ""
		}
	}
	for _, raw := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		raw = strings.TrimRight(strings.ReplaceAll(raw, "\t", "    "), " ")
		if strings.TrimSpace(raw) == "" {
			flush()
			out = append(out, "")
			continue
		}
		if cur == "" || beadOpensBlock(raw) {
			flush()
			cur = raw
			continue
		}
		cur += " " + strings.TrimSpace(raw)
	}
	flush()
	// A trailing blank block is the newline the text ended on, not a line.
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// beadOpensBlock reports whether a line starts something of its own rather than
// continuing the paragraph above it.
func beadOpensBlock(line string) bool {
	t := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(t, "#"), strings.HasPrefix(t, "- "), strings.HasPrefix(t, "* "),
		strings.HasPrefix(t, "> "), strings.HasPrefix(t, "```"), t == "---":
		return true
	}
	// "1." / "12)" and the like.
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	return i > 0 && i < len(t) && (t[i] == '.' || t[i] == ')')
}

// wrapWords folds a line at word boundaries to at most w cells, breaking a word
// longer than the measure rather than letting it run past the box. A wrapped
// bullet hangs under its own text rather than under its marker, so a list still
// reads as a list.
func wrapWords(s string, w int) []string {
	if w < 1 {
		return []string{s}
	}
	indent := s[:len(s)-len(strings.TrimLeft(s, " "))]
	hang := indent
	if beadOpensBlock(s) && !strings.HasPrefix(strings.TrimSpace(s), "#") {
		hang = indent + "  "
	}
	var out []string
	line, lead := indent, indent
	for _, word := range strings.Fields(s) {
		for vlen(lead)+vlen(word) > w {
			// A single word longer than the measure is broken rather than allowed
			// to run past the box; nothing else in a bead description is this long.
			head := truncCells(word, maxi(w-vlen(lead), 1))
			out = append(out, lead+head)
			word = string([]rune(word)[len([]rune(head)):])
			line, lead = hang, hang
		}
		switch {
		case line == lead:
			line += word
		case vlen(line)+1+vlen(word) <= w:
			line += " " + word
		default:
			out = append(out, line)
			lead = hang
			line = hang + word
		}
	}
	if strings.TrimSpace(line) != "" {
		out = append(out, line)
	}
	return out
}

// beadGraphLines is the neighbourhood drawn into the box, or one line saying
// which dimension ran out. A refusal is the honest state for the largest chains
// in a real workspace and is expected to appear. Full geometry — bordered boxes
// with a title — is tried first; a neighbourhood too big for that is retried
// compact, one row per node holding just the id, before it is refused.
func (t beadsTab) beadGraphLines(b bd.Bead, innerW, innerH int) [][]seg {
	// One cell of inset either side, matching every other line in the box.
	w := maxi(innerW-2, 1)
	nodes := beadNeighbourhood(t.src.list(), b.ID)
	var l beadGraphLayout
	var why string
	for _, geom := range []beadGeom{beadGeomFull, beadGeomCompact} {
		l = beadGraphLayoutFor(nodes, b.ID, w, geom)
		why = l.refusal(w, innerH)
		if why == "" {
			break
		}
	}
	if why != "" {
		return [][]seg{nil, {{t: "  " + why, fg: cDim}}}
	}
	var out [][]seg
	for _, row := range l.render(w, l.rows, t.src.prefix()) {
		out = append(out, append([]seg{{t: " "}}, row...))
	}
	return out
}

// beadBlockingLines lists the beads that wait on b: every bead in the feed whose
// Blockers names b.ID, derived from the list already on the model rather than a
// second `bd` call. Rows render id/priority/type/title as fixed-width columns
// per this repo's row-layout rule, so the four fields line up down the box.
func (t beadsTab) beadBlockingLines(b bd.Bead, innerW int) [][]seg {
	var deps []bd.Bead
	for _, d := range t.src.list() {
		if slices.Contains(d.Blockers, b.ID) {
			deps = append(deps, d)
		}
	}
	if len(deps) == 0 {
		return [][]seg{nil, {{t: "  nothing waits on this bead", fg: cDim}}}
	}
	sort.SliceStable(deps, func(i, j int) bool {
		if deps[i].Priority != deps[j].Priority {
			return deps[i].Priority < deps[j].Priority
		}
		return deps[i].ID < deps[j].ID
	})

	wID, wType := 0, 0
	for _, d := range deps {
		wID = maxi(wID, vlen(shortBeadID(d.ID, t.src.prefix())))
		wType = maxi(wType, vlen(d.Type))
	}
	const wPrio = 2
	titleW := maxi(innerW-1-wID-2-wPrio-2-wType-2, 0)

	var out [][]seg
	for _, d := range deps {
		out = append(out, []seg{
			{t: " "},
			{t: padEnd(shortBeadID(d.ID, t.src.prefix()), wID), fg: cCyan},
			{t: "  "},
			{t: padEnd(priorityLabel(d.Priority), wPrio), fg: priorityColor(d.Priority)},
			{t: "  "},
			{t: padEnd(d.Type, wType), fg: cDim},
			{t: "  "},
			{t: truncR(d.Title, titleW), fg: cFg},
		})
	}
	return out
}

// beadOverlayKey handles a key while the overlay owns the keyboard. Every other
// key is swallowed: a modal that lets one through moves the list underneath it,
// so the row the overlay is describing is no longer the row under the cursor.
func (t *beadsTab) beadOverlayKey(key string) {
	switch key {
	case "right":
		t.overlayTab = (t.overlayTab + 1) % beadTabCount
		t.scroll = 0
	case "left":
		t.overlayTab = (t.overlayTab + beadTabCount - 1) % beadTabCount
		t.scroll = 0
	case "up":
		if t.scroll > 0 {
			t.scroll--
		}
	case "down":
		t.scroll++
	case "esc":
		t.overlay = false
	}
}

// beadOverlayFooter is the dashboard footer while the overlay is up.
func beadOverlayFooter() string {
	return renderSegs([]seg{
		{t: " "}, {t: "← →", fg: cCyan}, {t: " switch", fg: cDim},
		{t: " · ", fg: cFaint}, {t: "↑↓", fg: cCyan}, {t: " scroll", fg: cDim},
		{t: " · ", fg: cFaint}, {t: "esc", fg: cCyan}, {t: " close", fg: cDim},
	}, nil)
}
