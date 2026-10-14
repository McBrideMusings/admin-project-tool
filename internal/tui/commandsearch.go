// commandsearch.go is the Commands tab's quick reach over a list that stays
// on screen (admin-3rgp.72.44): the Recent strip above the list, holding the
// last few fully-resolved invocations, and `/` as a fuzzy search over every
// command and target, with a ghost of the cursor row's invocation that tab
// accepts one word at a time.
package tui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// recentMax is how many invocations the Recent strip shows.
const recentMax = 5

// recentStrip is the Recent strip's own state: the invocations it shows, in
// the order the usage store returned them, and which one it has picked.
type recentStrip struct {
	items []string
	sel   int
}

// searcher is a tab that filters its own rows rather than through the
// shell's substring filter, and drives the search input: the cursor jumps to
// the best match after every edit, ghost is the dim text drawn after the
// input, and complete is what tab turns the query into.
type searcher interface {
	visibleRows(s *shell) []rowItem
	filterChanged(s *shell)
	relocate(s *shell, r rowItem)
	ghost(s *shell) string
	complete(s *shell) (query string, ok bool)
}

// invocationOf is the string a command or target row runs as — "deploy",
// "command-test counter" — the same string the usage store records
// (resolvedInvocation). Empty for a row that runs nothing.
func invocationOf(r rowItem) string {
	if r.work == nil || r.group {
		return ""
	}
	switch r.kind {
	case rowCommand:
		return r.work.cmdName
	case rowCommandTarget:
		return r.work.cmdName + " " + r.work.targetKey
	}
	return ""
}

// fuzzyScore ranks how well q matches inv, case-insensitively: 3 for a
// prefix, 2 for a substring, 1 for a subsequence (every character of q in
// order, gaps allowed), 0 for no match.
func fuzzyScore(inv, q string) int {
	inv, q = strings.ToLower(inv), strings.ToLower(q)
	switch {
	case strings.HasPrefix(inv, q):
		return 3
	case strings.Contains(inv, q):
		return 2
	}
	qr := []rune(q)
	i := 0
	for _, c := range inv {
		if i < len(qr) && qr[i] == c {
			i++
		}
	}
	if i == len(qr) {
		return 1
	}
	return 0
}

// expandedRows is rows with collapse off, so every target is present — what
// the strip resolves invocations against and what a search matches over.
func (t *commandsTab) expandedRows(s *shell) []rowItem {
	if !t.collapsed {
		return t.rows(s)
	}
	all := *t
	all.collapsed = false
	return all.rows(s)
}

// visibleRows is the list as drawn: the Recent strip over the rows while no
// search is typed, or only the rows matching it while one is.
func (t *commandsTab) visibleRows(s *shell) []rowItem {
	if s.filter != "" {
		return t.searchRows(s)
	}
	rows := t.rows(s)
	all := rows
	if t.collapsed {
		all = t.expandedRows(s)
	}
	if strip, ok := t.recentRow(all); ok {
		return append([]rowItem{strip}, rows...)
	}
	return rows
}

// recentRow is the Recent strip, built from the usage store's invocations
// that still name a row in this manifest — a renamed command or a removed
// target drops out rather than offering a dispatch that would fail. The
// strip takes the kind and payload of the invocation it has picked; ok is
// false when nothing resolves. The pick is held as the invocation, not an
// index, so running it — which moves it to the front of the store — leaves
// the same invocation picked; one that dropped out falls back to the first.
func (t *commandsTab) recentRow(all []rowItem) (rowItem, bool) {
	byInv := map[string]rowItem{}
	for _, r := range all {
		if inv := invocationOf(r); inv != "" {
			byInv[inv] = r
		}
	}
	var items []string
	for _, inv := range t.usage.recent() {
		if _, ok := byInv[inv]; ok {
			items = append(items, inv)
		}
	}
	if len(items) == 0 {
		return rowItem{}, false
	}
	sel := max(0, slices.Index(items, t.recentPick))
	strip := byInv[items[sel]]
	strip.recent = &recentStrip{items: items, sel: sel}
	return strip, true
}

// searchRows is every row whose invocation fuzzy-matches the query, in list
// order under the Commands header, each target keeping its group heading
// above it so a match never loses the command it belongs to.
func (t *commandsTab) searchRows(s *shell) []rowItem {
	all := t.expandedRows(s)
	hit := map[string]bool{} // group names with a matching target
	for _, r := range all {
		if r.kind == rowCommandTarget && fuzzyScore(invocationOf(r), s.filter) > 0 {
			hit[r.work.cmdName] = true
		}
	}
	var out []rowItem
	for _, r := range all {
		switch {
		case r.kind == rowHeader:
			out = append(out, r)
		case r.group && hit[r.work.cmdName]:
			out = append(out, r)
		case fuzzyScore(invocationOf(r), s.filter) > 0:
			out = append(out, r)
		}
	}
	return out
}

// filterChanged puts the cursor on the best match: the highest score, the
// first in list order among equals.
func (t *commandsTab) filterChanged(s *shell) {
	best, at := 0, -1
	for i, r := range t.visibleRows(s) {
		if sc := fuzzyScore(invocationOf(r), s.filter); sc > best {
			best, at = sc, i
		}
	}
	if at >= 0 {
		t.cursor = at
	}
}

// cursorInvocation is the invocation of the row under the cursor, or "".
func (t *commandsTab) cursorInvocation(s *shell) string {
	r, ok := t.currentRow(s)
	if !ok {
		return ""
	}
	return invocationOf(r)
}

// hasPrefixFold reports whether inv starts with q, ignoring case, comparing
// the same bytes ghost and complete go on to slice.
func hasPrefixFold(inv, q string) bool {
	return len(q) <= len(inv) && strings.EqualFold(inv[:len(q)], q)
}

// ghost is the dim text after the search input: the rest of the cursor row's
// invocation when the query is its prefix, else the whole invocation tab
// heads toward.
func (t *commandsTab) ghost(s *shell) string {
	inv := t.cursorInvocation(s)
	q := s.filter
	if inv == "" || q == "" || strings.EqualFold(inv, q) {
		return ""
	}
	if hasPrefixFold(inv, q) {
		return inv[len(q):]
	}
	return "  → " + inv
}

// complete is what tab turns the query into: one more word of the cursor
// row's invocation. A query that is a prefix of the invocation gains the rest
// of its current word (and the space after it, when a word follows); any
// other query is replaced by as many of the invocation's words as the query
// has, so "ct" becomes "command-test " and "ct c" becomes
// "command-test counter". ok is false when there is nothing to add.
func (t *commandsTab) complete(s *shell) (string, bool) {
	inv := t.cursorInvocation(s)
	q := s.filter
	if inv == "" || q == "" {
		return "", false
	}
	var out string
	if hasPrefixFold(inv, q) {
		rest := inv[len(q):]
		lead := len(rest) - len(strings.TrimLeft(rest, " "))
		if i := strings.IndexByte(rest[lead:], ' '); i >= 0 {
			out = inv[:len(q)+lead+i] + " "
		} else {
			out = inv
		}
	} else {
		words := strings.Fields(inv)
		if k := len(strings.Fields(q)); k < len(words) {
			out = strings.Join(words[:k], " ") + " "
		} else {
			out = inv
		}
	}
	return out, out != q
}

// stepRecent moves the strip's pick by delta, stopping at either end.
func (t *commandsTab) stepRecent(r rowItem, delta int) {
	t.recentPick = r.recent.items[clampi(0, r.recent.sel+delta, len(r.recent.items)-1)]
}

// relocate puts the cursor back on the row r names after the list changed
// shape around it — the strip appearing or vanishing above it, or a search
// ending — and, collapsed, opens r's command so the cursor and the open
// command agree. A row that no longer exists leaves the cursor to clamp.
func (t *commandsTab) relocate(s *shell, r rowItem) {
	if t.collapsed && r.recent == nil {
		if owner := ownerCmdName(r); owner != "" {
			t.openCmd = owner
		}
	}
	for i, v := range t.visibleRows(s) {
		if sameRowIdentity(v, r) {
			t.cursor = i
			return
		}
	}
	t.clamp(s)
}

// renderRecentStrip draws the strip: its label, then each invocation joined
// by dots, the picked one cyan and underlined while the cursor is on the
// strip. Entries that do not fit are dropped — from the front first while
// the pick would not fit, so the invocation ↵ runs is always on screen.
func renderRecentStrip(rs *recentStrip, current bool, width int) string {
	const label, sep = "Recent  ", " · "
	span := func(from, to int) int { // display width of items[from..to]
		w := lipgloss.Width(label)
		for i := from; i <= to; i++ {
			if i > from {
				w += lipgloss.Width(sep)
			}
			w += lipgloss.Width(rs.items[i])
		}
		return w
	}
	first := 0
	for first < rs.sel && span(first, rs.sel) > width {
		first++
	}
	out := lipgloss.NewStyle().Bold(true).Render("Recent") + "  "
	for i := first; i < len(rs.items); i++ {
		if span(first, i) > width {
			break
		}
		inv := rs.items[i]
		if i > first {
			out += faintStyle.Render(sep)
		}
		switch {
		case i == rs.sel && current:
			out += lipgloss.NewStyle().Foreground(cCyan).Underline(true).Render(inv)
		case i == rs.sel:
			out += inv
		default:
			out += mutedStyle.Render(inv)
		}
	}
	return out
}
