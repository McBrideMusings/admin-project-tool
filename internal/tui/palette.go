// palette.go is the Work tab's command palette (admin-3rgp.72.2): a
// bordered, content-sized box drawn at the top of the row list, replacing
// the flat Commands list workRows() used to emit. Ported from the design
// source of truth, docs/spikes/admin-tui-desktop/variants.go — the
// paletteEntry model, the fuzzy-search functions (fuzzyScore/paletteWords/
// tokenAt/matchesPrefix/paletteCandidates) and the box renderers
// (paletteBoxFit/paletteIdleContent/paletteTypingContent) all mirror that
// file's behaviour, using this package's own style/width helpers
// (faintStyle, vlen, window, truncR/padEnd) in place of the spike's
// throwaway ones (dim, VW, scrollWindow, Pad/trunc).
//
// Word-by-word Tab completion (admin-3rgp.72.3), the browse fallback
// (admin-3rgp.72.5) and a combined box+Workers cursor (admin-3rgp.72.4) are
// out of scope here — the box renders its content but is not yet
// selectable of its own accord; up/down/enter on the Work tab keep acting
// on the Workers rows exactly as they did before this ticket.
package tui

import (
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/workusage"
)

// paletteEntry is one FULLY RESOLVED invocation — what actually gets
// dispatched. "dev cf" is an entry; the bare parent "dev" (ambiguous among
// its targets) never is.
type paletteEntry struct {
	parent, target, run string
}

func (e paletteEntry) resolved() string {
	if e.target == "" {
		return e.parent
	}
	return e.parent + " " + e.target
}

// flatActionRun is a non-multi-target action's own run line: its `run`
// shell command, or "@<action>" when it delegates — the same two shapes
// kinds.TargetSummary reads off a multi-target's target subtable, mirrored
// here for the flat (single-action) case that function doesn't cover.
func flatActionRun(a manifest.Action) string {
	if s, ok := a["run"].(string); ok {
		return s
	}
	if s, ok := a["action"].(string); ok {
		return "@" + s
	}
	return ""
}

// buildPaletteEntries flattens every declared command into the resolved
// shape fuzzy search matches against: one entry per target of a multi-target
// command, one entry for a flat command, and NEVER a bare-parent entry for a
// multi-target one — "dev" alone is ambiguous among its targets and can't be
// dispatched. Reuses commandRowItem so a target's run string, and a flat
// command's, are derived exactly the way the (retired) row renderer always
// derived them — no second copy of that logic.
func (m Model) buildPaletteEntries() []paletteEntry {
	var out []paletteEntry
	for _, cr := range m.declaredCommands() {
		wd := m.commandRowItem(cr, nil).work
		if wd == nil {
			continue
		}
		if len(wd.targets) == 0 {
			if wd.cmdRun == "" {
				continue // a sequence/workflow command has no single resolved invocation
			}
			out = append(out, paletteEntry{parent: cr.name, run: wd.cmdRun})
			continue
		}
		for _, key := range wd.targets {
			out = append(out, paletteEntry{parent: cr.name, target: key, run: wd.targetRuns[key]})
		}
	}
	return out
}

// paletteMRU is the box's idle-screen row list: workusage.Top's most-recent
// invocations for nwo, resolved back to their paletteEntry (dropping any
// invocation whose command or target the manifest no longer declares) —
// never a guessed or seeded set on a fresh project (workusage.Top's own
// contract).
func paletteMRU(entries []paletteEntry, nwo string) []paletteEntry {
	var out []paletteEntry
	for _, r := range workusage.Top(nwo, 5) {
		for _, e := range entries {
			if e.resolved() == r {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// fuzzyScore is a subsequence match: every rune of query must appear in
// target IN ORDER (case-insensitive); score rewards contiguous runs and a
// literal prefix match, so "cf" ranks "dev cf" and "kill cf" above a
// scattered match like "chat-moderation".
func fuzzyScore(query, target string) (int, bool) {
	q, t := strings.ToLower(query), strings.ToLower(target)
	if q == "" {
		return 0, true
	}
	score, ti, lastMatch := 0, 0, -1
	for _, qc := range q {
		found := false
		for ; ti < len(t); ti++ {
			if rune(t[ti]) == qc {
				if lastMatch == ti-1 {
					score += 3 // contiguous run
				} else {
					score++
				}
				lastMatch = ti
				ti++
				found = true
				break
			}
		}
		if !found {
			return 0, false
		}
	}
	if strings.HasPrefix(t, q) {
		score += 10
	}
	return score, true
}

// paletteWords splits query into its complete (space-terminated) words and
// the partial word still being typed — "" if query ends in a space or is
// empty, meaning nothing is in progress yet.
func paletteWords(query string) (complete []string, partial string) {
	fields := strings.Fields(query)
	if query == "" || strings.HasSuffix(query, " ") {
		return fields, ""
	}
	if len(fields) == 0 {
		return nil, ""
	}
	return fields[:len(fields)-1], fields[len(fields)-1]
}

// tokenAt is a resolved entry's word at position idx: 0 is the parent
// command, 1 is its target ("" for a single command, which only ever has a
// word 0).
func tokenAt(e paletteEntry, idx int) string {
	switch idx {
	case 0:
		return e.parent
	case 1:
		return e.target
	}
	return ""
}

func matchesPrefix(complete []string, e paletteEntry) bool {
	for i, w := range complete {
		t := tokenAt(e, i)
		if t == "" || !strings.HasPrefix(strings.ToLower(t), strings.ToLower(w)) {
			return false
		}
	}
	return true
}

// paletteCandidates is every entry consistent with the words already fully
// typed, ranked by how well the IN-PROGRESS word matches the token at that
// same position — "dev c" ranks by "c" against each candidate's target
// (having already filtered to parent-starts-with-"dev"), never by a fuzzy
// match across the whole resolved string.
func paletteCandidates(entries []paletteEntry, query string) (cands []paletteEntry, wordIdx int, partial string) {
	var complete []string
	complete, partial = paletteWords(query)
	wordIdx = len(complete)
	type scored struct {
		e paletteEntry
		s int
	}
	var out []scored
	for _, e := range entries {
		if !matchesPrefix(complete, e) {
			continue
		}
		tok := tokenAt(e, wordIdx)
		if tok == "" && partial != "" {
			continue // no word at this position to match the partial against
		}
		if s, ok := fuzzyScore(partial, tok); ok {
			out = append(out, scored{e, s})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].s != out[j].s {
			return out[i].s > out[j].s
		}
		return out[i].e.resolved() < out[j].e.resolved()
	})
	cands = make([]paletteEntry, len(out))
	for i, sc := range out {
		cands[i] = sc.e
	}
	return cands, wordIdx, partial
}

// paletteGhost is the dim inline suggestion — the rest of the TOP
// candidate's current-word token, past what's already typed. Only shown
// when that token genuinely starts with the typed partial (a real
// completion, not a fuzzy guess dressed up as one). Tab actually accepting
// it is admin-3rgp.72.3; this only decides what renders.
func paletteGhost(cands []paletteEntry, wordIdx int, partial string) string {
	if len(cands) == 0 {
		return ""
	}
	tok := tokenAt(cands[0], wordIdx)
	if !strings.HasPrefix(strings.ToLower(tok), strings.ToLower(partial)) {
		return ""
	}
	return tok[len(partial):]
}

// pluralES is the "match / matches" suffix the palette's candidate count
// uses — its own function, never shared with an ordinary "N thing/things"
// helper, so a rename of one can't silently change the other's suffix too.
func pluralES(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}

var paletteBoldStyle = lipgloss.NewStyle().Bold(true)

// paletteCaretStyle is the typing screen's cursor glyph — cyan, matching
// the design's own cy.Render("▏").
var paletteCaretStyle = lipgloss.NewStyle().Foreground(cCyan)

// paletteMark renders the box's own row marker: a cyan ◆ for the selected
// row, a dim ◇ otherwise — identical in shape to renderWorkRow's marker,
// kept as its own function since the box has no cursor of its own yet
// (admin-3rgp.72.4) and every call site here passes sel=false.
func paletteMark(sel bool, s string) string {
	if sel {
		return lipgloss.NewStyle().Foreground(cCyan).Render("◆ ") + s
	}
	return faintStyle.Render("◇ ") + s
}

// paletteBoxFit draws a box sized to its content — height len(body)+2, never
// stretched to fill the pane. w is the box's own full width (the caller's
// left-column width, lw — the same convention every other Work-tab row
// already renders at, so the composed line just needs its usual single
// leading space and no extra margin baked in here).
func paletteBoxFit(w int, title string, body []string) []string {
	inner := maxi(w-4, 8)
	dashes := maxi(w-5-vlen(title), 0)
	out := []string{"┌─ " + paletteBoldStyle.Render(title) + " " + faintStyle.Render(strings.Repeat("─", dashes)) + "┐"}
	for _, l := range body {
		out = append(out, "│ "+padEnd(l, inner)+" │")
	}
	out = append(out, "└"+strings.Repeat("─", w-2)+"┘")
	return out
}

// paletteIdleContent is the box's inner lines for the idle screen — the MRU
// strip. One hint row ("/ search   b browse all", plus "N recent" once
// something has actually been dispatched — never a guessed seed), then
// either the strip's rows or, when it's empty, a single dim "no commands run
// yet" row. boxSelected/cursor are for the row marker only — always false/0
// until admin-3rgp.72.4 gives the box its own cursor.
func paletteIdleContent(w int, mru []paletteEntry, boxSelected bool, cursor int) []string {
	hint := faintStyle.Render("/ ") + paletteBoldStyle.Render("search") + faintStyle.Render("   b ") + paletteBoldStyle.Render("browse all")
	if len(mru) > 0 {
		hint += faintStyle.Render("   " + strconv.Itoa(len(mru)) + " recent")
	}
	body := []string{hint}
	if len(mru) == 0 {
		body = append(body, faintStyle.Render("no commands run yet"))
		return body
	}
	for i, e := range mru {
		name := padEnd(truncR(e.resolved(), 20), 20)
		run := faintStyle.Render(truncR(e.run, maxi(6, w-30)))
		body = append(body, paletteMark(boxSelected && i == cursor, name)+run)
	}
	return body
}

// paletteTypingContent is the box's inner lines for the typing screen. Typed
// text renders plain, the rest of the top candidate's CURRENT WORD renders
// dim right after the cursor (paletteGhost) — visible before Tab is ever
// pressed — and the candidate list narrows by word position, not a
// whole-string fuzzy match. Capped to a handful of rows (maxRows): the box
// doesn't own the whole pane, so it can't grow to show everything.
func paletteTypingContent(w int, query string, entries []paletteEntry, boxSelected bool, cursor int) []string {
	cands, wordIdx, partial := paletteCandidates(entries, query)
	ghost := paletteGhost(cands, wordIdx, partial)

	body := []string{
		faintStyle.Render("/ ") + paletteBoldStyle.Render(query) + faintStyle.Render(ghost) + paletteCaretStyle.Render("▏"),
		"",
	}
	if len(cands) == 0 {
		body = append(body, faintStyle.Render("no match"))
		return body
	}
	const maxRows = 5
	win := cursor
	if !boxSelected {
		win = 0 // scroll window centers on the box's own top row instead
	}
	start, end := window(len(cands), win, maxRows)
	for i := start; i < end; i++ {
		e := cands[i]
		name := padEnd(truncR(e.resolved(), 28), 30)
		row := paletteMark(boxSelected && i == cursor, name)
		row += faintStyle.Render(truncR(e.run, maxi(6, w-38)))
		body = append(body, row)
	}
	body = append(body, faintStyle.Render(strconv.Itoa(len(cands))+" match"+pluralES(len(cands))+" · tab complete word · ↵ dispatch"))
	return body
}

// renderPaletteBox is the palette's box lines for the Work tab body's left
// column, sized to lw — pinned above the (post-72.2) Workers-only row list.
// The box shows no selection of its own in this slice (boxSelected always
// false): cursor unification with the Workers section is admin-3rgp.72.4.
func (m Model) renderPaletteBox(lw int) []string {
	entries := m.buildPaletteEntries()
	inner := maxi(lw-4, 8)
	var content []string
	if m.paletteTyping {
		content = paletteTypingContent(inner, m.paletteQuery, entries, false, 0)
	} else {
		content = paletteIdleContent(inner, paletteMRU(entries, m.nwo), false, 0)
	}
	return paletteBoxFit(lw, "Command Palette", content)
}
