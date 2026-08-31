package tui

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// Palette — ANSI 16-color indices, NOT hex. Every color resolves through the
// user's terminal theme (Ghostty et al.) so the dashboard follows their colors
// instead of imposing fixed values. The semantic roles mirror the design; the
// actual hues come from the terminal.
var (
	cBg     = lipgloss.Color("0") // black — text on the blue active-tab pill
	cFg     = lipgloss.Color("7") // default text
	cDim    = lipgloss.Color("8") // bright black / gray: ages, counts, in-review
	cFaint  = lipgloss.Color("8") // gray: no-remote, "↓ N more", brackets, rules
	cRule   = lipgloss.Color("8") // gray: rules, unfocused box borders
	cSel    = lipgloss.Color("8") // selected-row background (focused pane)
	cSelDim = lipgloss.Color("0") // selected-row background (unfocused pane)
	// cDrag is the drag-to-copy highlight background. It repaints the selected
	// cells flat (fg cBg on this bg), replacing whatever colors and background
	// that range already carried — including the cursor row's own cSel — so the
	// highlight is always the one thing legible there, never a reverse-video
	// toggle of colors that were already mixed.
	cDrag   = lipgloss.Color("11")
	cBlue   = lipgloss.Color("12") // branches, active tab, focused border, cursor
	cGreen  = lipgloss.Color("10") // current ●, ahead ↑, checks ✓, success
	cRed    = lipgloss.Color("9")  // behind ↓, checks ✗
	cYellow = lipgloss.Color("11") // pending ●, quarantine [!], needs-review
	cCyan   = lipgloss.Color("14") // sha, #num, badge counts, key hints
	cMag    = lipgloss.Color("13") // stash refs, ⎇ has-PR, author
	cBright = lipgloss.Color("15") // current-branch name, focused box title
	// cSlate: a co-contributor's branch name in the Branches pane — still live,
	// not yours. Deliberately not cFaint/gray: gray already means "finished, safe
	// to ignore" (a struck-through Clean Up row), and a colleague's live branch is
	// the opposite of that. Not cBlue either — that stays "mine" throughout the
	// pane, so the two never look interchangeable at a glance.
	cSlate = lipgloss.Color("6")
)

// A few plain (non-segment) styles for one-off lines: loading/error text and
// empty-state messages. Colored row content goes through renderSegs.
var (
	mutedStyle = lipgloss.NewStyle().Foreground(cDim)
	faintStyle = lipgloss.NewStyle().Foreground(cFaint)
	badStyle   = lipgloss.NewStyle().Foreground(cRed)
)

// link wraps text in an OSC-8 terminal hyperlink pointing at url. Terminals that
// don't support OSC-8 just show text.
func link(url, text string) string {
	if url == "" {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// branchGlyph is the marker printed before a branch name, trailing space
// included. The default is U+E0A0, the Powerline branch icon: it sits in the
// primary monospace face of every Nerd Font patch (JetBrains Mono, Fira Code,
// Hack, Meslo), so it lands in one cell at the row's own weight. The old U+2387
// did not — no monospace face on macOS carries it, so CoreText fell back to a
// proportional math face and squeezed a mis-sized glyph into the cell.
//
// A terminal program cannot ask what its font covers. No escape sequence
// reports glyph coverage, and the one probe that exists — print, then CSI 6n —
// measures cell width, which a wrong-font substitution does not change. So
// there is no autodetection to write: REPO_BRANCH_GLYPH overrides the marker
// instead. Set it empty to print none. The branch column keeps its width either
// way, so the rows stay aligned whatever the marker is.
func branchGlyph() string {
	g, ok := os.LookupEnv("REPO_BRANCH_GLYPH")
	if !ok {
		return defaultBranchGlyph + " "
	}
	if g == "" {
		return ""
	}
	return g + " "
}

// defaultBranchGlyph is U+E0A0, the Powerline branch icon.
const defaultBranchGlyph = "\ue0a0"
