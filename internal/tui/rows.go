package tui

import "strconv"

// Builders that turn domain data into rowItems for the flat list tabs. These sit
// with the row model (model.go's rowItem), not with the low-level segment/box
// toolkit in panel.go.

// headerRow builds a section header with an optional right-aligned cluster.
func headerRow(label string, right ...seg) rowItem {
	return rowItem{kind: rowHeader, label: label, rightSegs: right}
}

// ruleRow is the full-width rule rendered under a section header.
func ruleRow() rowItem { return rowItem{kind: rowRule} }

// blankRow is an empty spacer line between sections.
func blankRow() rowItem { return rowItem{kind: rowText} }

// countSeg is the cyan count shown on the right of a section header.
func countSeg(n int) []seg { return []seg{{t: strconv.Itoa(n), fg: cCyan}} }

// numberedRowLeft builds the shared "<glyph> #<num>  <title>" left cluster used by
// the PR and issue rows. numSeg is the pre-built (cyan, linked) number segment and
// numW its budgeted column width; right is sized — not rendered — so the title
// truncates to the remaining columns. tail is an optional trailing run rendered
// after the title (e.g. a PR's faint branch name); its width is reserved so the
// title truncates around it. Pass nil for no tail.
func numberedRowLeft(glyph, numSeg seg, numW int, title string, tail, right []seg, cols int) []seg {
	titleW := flexWidth(cols, numW+6, segsWidth(tail)+segsWidth(right), 4)
	t := truncR(title, titleW)
	// The title carries the number's hyperlink too, so the whole "#12  Fix the
	// thing" reads as one target instead of asking for a click on four characters.
	// numSeg.link is empty for a row with no page to open (a bead, or any repo whose
	// origin isn't GitHub), and an empty link leaves both segments plain text.
	out := []seg{
		glyph, {t: " "}, numSeg,
		{t: "  "}, {t: t, fg: cFg, link: numSeg.link},
	}
	// With a trailing column (a PR's branch), pad the title to its full budget so
	// the tail starts at the same screen column on every row.
	if len(tail) > 0 {
		out = append(out, seg{t: padN(titleW - vlen(t))})
	}
	return append(out, tail...)
}
