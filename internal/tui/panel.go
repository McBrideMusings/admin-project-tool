package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// seg is one colored run of text inside a rendered line. fg nil means default fg.
// link, when set, wraps the text in an OSC-8 hyperlink.
type seg struct {
	t      string
	fg     lipgloss.TerminalColor
	bg     lipgloss.TerminalColor // per-segment background (e.g. active tab pill)
	link   string
	strike bool // struck through (e.g. a branch that is safe to delete)
}

// vlen is the display width (terminal cells) of a string. Wide runes (CJK, many
// emoji) count as 2, so column math stays correct for non-ASCII content. For
// plain ASCII this equals the rune count.
func vlen(s string) int { return lipgloss.Width(s) }

func padN(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

func padEnd(s string, n int) string { return s + padN(n-vlen(s)) }

// truncR truncates to n display cells with a trailing ellipsis (left-anchored).
func truncR(s string, n int) string {
	if vlen(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := vlen(string(r))
		if w+rw > n-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}

// truncL truncates to n display cells keeping the tail, with a leading ellipsis.
func truncL(s string, n int) string {
	if vlen(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	r := []rune(s)
	w := 0
	i := len(r)
	for i > 0 {
		rw := vlen(string(r[i-1]))
		if w+rw > n-1 {
			break
		}
		w += rw
		i--
	}
	return "…" + string(r[i:])
}

// flexWidth is the remaining cell budget for a flexible (truncating) column:
// total cols minus the fixed lead/glyph columns and the right cluster, floored
// at min. Used by the row builders so the title/name truncates to what's left.
func flexWidth(cols, fixed, rightW, min int) int {
	w := cols - fixed - rightW
	if w < min {
		w = min
	}
	return w
}

func segsWidth(segs []seg) int {
	w := 0
	for _, s := range segs {
		w += vlen(s.t)
	}
	return w
}

// composeLR lays left segments flush-left and right segments flush-right within
// cols, filling the gap with spaces.
func composeLR(left, right []seg, cols int) []seg {
	lw, rw := segsWidth(left), segsWidth(right)
	fill := cols - lw - rw
	min := 0
	if len(right) > 0 {
		min = 1
	}
	if fill < min {
		fill = min
	}
	out := append([]seg{}, left...)
	out = append(out, seg{t: padN(fill)})
	return append(out, right...)
}

// truncCells takes the leading runes that fit in n display cells (no ellipsis).
func truncCells(s string, n int) string {
	if vlen(s) <= n {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := vlen(string(r))
		if w+rw > n {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}

// fitSegs clips a line of segments to exactly width display cells, padding with spaces.
func fitSegs(segs []seg, width int) []seg {
	out := []seg{}
	used := 0
	for _, s := range segs {
		if used >= width {
			break
		}
		sw := vlen(s.t)
		room := width - used
		if sw <= room {
			out = append(out, s)
			used += sw
		} else {
			s.t = truncCells(s.t, room)
			out = append(out, s)
			used += vlen(s.t)
		}
	}
	if used < width {
		out = append(out, seg{t: padN(width - used)})
	}
	return out
}

// renderSegs renders a line of segments. When bg is non-nil it is applied to
// every segment so a selected row paints a continuous background.
func renderSegs(segs []seg, bg lipgloss.TerminalColor) string {
	var b strings.Builder
	for _, s := range segs {
		st := lipgloss.NewStyle()
		if s.fg != nil {
			st = st.Foreground(s.fg)
		} else {
			st = st.Foreground(cFg)
		}
		switch {
		case s.bg != nil:
			st = st.Background(s.bg)
		case bg != nil:
			st = st.Background(bg)
		}
		if s.strike {
			st = st.Strikethrough(true)
		}
		// The link wraps the styled text, never the other way round: a strikethrough
		// style renders rune by rune, and styling an OSC-8 sequence that way splits
		// its escape into separately colored characters the terminal prints as text.
		txt := st.Render(s.t)
		if s.link != "" {
			txt = link(s.link, txt)
		}
		b.WriteString(txt)
	}
	return b.String()
}

// compactAge turns git's relative date ("19 minutes ago", "2 days ago") into the
// terse form the prototype shows ("19m", "2d"). Unparseable input passes through.
func compactAge(s string) string {
	s = strings.TrimSpace(s)
	switch s {
	case "":
		return ""
	case "just now", "now":
		return "now"
	case "yesterday":
		return "1d"
	}
	f := strings.Fields(s)
	if len(f) < 2 {
		return s
	}
	unit := strings.TrimSuffix(f[1], "s")
	short := map[string]string{
		"second": "s", "minute": "m", "hour": "h", "day": "d",
		"week": "w", "month": "mo", "year": "y",
	}[unit]
	if short == "" {
		return s
	}
	return f[0] + short
}

// ageMinutes converts a git relative date to minutes for sorting; unknown → huge.
func ageMinutes(s string) int {
	s = strings.TrimSpace(s)
	switch s {
	case "just now", "now":
		return 0
	case "yesterday":
		return 1440
	}
	f := strings.Fields(s)
	if len(f) < 2 {
		return 1 << 30
	}
	n := 0
	for _, c := range f[0] {
		if c < '0' || c > '9' {
			return 1 << 30
		}
		n = n*10 + int(c-'0')
	}
	per := map[string]int{
		"second": 0, "minute": 1, "hour": 60, "day": 1440,
		"week": 10080, "month": 43200, "year": 525600,
	}[strings.TrimSuffix(f[1], "s")]
	return n * per
}

// --- compact upstream status (branch panel right cluster) ---

// Branch-row column widths. The row is a table, so every field occupies the same
// cells on every line and the eye can read straight down a column instead of
// re-parsing where one value ends and the next begins.
const (
	brAgeW = 4 // "11mo"
	// brGutterW is the leading state glyph plus its trailing gap, always drawn on
	// every row (◆/◇/●/?/· — shape carries worktree/current, color carries
	// ownership; see brPanelLine). This is the whole left gutter outside select
	// mode.
	brGutterW = 2
	// brSelectW is "[ ] " — the multi-select checkbox plus its trailing gap,
	// always drawn (even unselected) so the column never shifts once a selection
	// starts. It sits to the LEFT of the gutter glyph while select mode is active,
	// so select mode adds brSelectW cells to the row's left edge and takes them
	// back out of the name column — never a separate width computation.
	brSelectW = 4
	// brNameMaxW and brPurposeMinW size the inline purpose column. When the pane is
	// wide enough that the name column can keep brNameMinW cells and the purpose
	// still gets brPurposeMinW, purpose is a column on the branch's own row; below
	// that it stays on the second line, where the name has the whole width.
	brNameMaxW    = 28
	brNameMinW    = 20
	brPurposeMinW = 24

	// historyStripMaxH is the tallest the Commits/Stashes strip gets under
	// Branches, borders included. It is also capped at a third of the body, so the
	// strip never takes more than a third of what Branches needs.
	historyStripMaxH = 9

	// Columns of the sectioned Branches row. The name column takes whatever is
	// left, so everything right of it sits at the same offset on every line at any
	// terminal width WITHIN A GIVEN SECTION — the property that makes the pane
	// readable by scanning down a column instead of re-parsing each row. A column
	// constant across an entire section is hoisted onto that section's own rule
	// (see columnPlan/planColumns in section.go) and dropped from its rows, so two
	// sections may legitimately size their name column differently at the same
	// pane width; the guarantee is per section, not per pane.
	//
	// Every one is a hard cap, never a minimum: a value longer than its column is
	// truncated, because a cell that grows to fit its content shoves every column
	// right of it out of alignment and, at the end of the row, straight out of the
	// box. See rowCell.
	brOwnerW = 10 // "darren"; a GitHub login when no commit names the person
	brStateW = 14 // "remote deleted", the longest state word
	brHoldW  = 16 // "456 uncommitted"
	brReadyW = 6  // "ready" / "keep" / "held"
)

// gutterWidth is the left gutter's total cell width — the single function the
// row's leading segments (brPanelLine) and the name column's width math
// (branchRowLayout/stateColumnEnd) both read, so select mode can never make
// them disagree about where the name column starts.
func gutterWidth(selectMode bool) int {
	if selectMode {
		return brSelectW + brGutterW
	}
	return brGutterW
}

// padStart right-aligns one segment within n cells, keeping the pad uncolored so a
// column's background never picks up the value's color.
func padStart(s seg, n int) []seg {
	if pad := n - vlen(s.t); pad > 0 {
		return []seg{{t: padN(pad)}, s}
	}
	return []seg{s}
}

// Commit-row and stash-row column widths. Same table rule as the branch rows:
// each field occupies the same cells on every line, so the pane can be read by
// scanning a column instead of re-parsing where one value ends and the next
// begins. cmShaW is git's abbreviated sha, cmAgeW holds the widest compactAge
// ("11mo"), and stRefW holds a stash ref up to "stash@{99}" — a hundredth stash
// truncates its column rather than pushing every subject on that row sideways.
const (
	cmShaW = 8
	cmAgeW = 4
	stRefW = 10
)

// --- box panel rendering ---

type boxPanel struct {
	title   string
	badge   string
	width   int
	height  int
	focused bool
	lines   [][]seg // each entry is one inner line of segments
	active  int     // index into lines for the selected row, or -1
	scroll  int     // top line index
	// footer sits on the bottom border, flush right, the way badge sits on the top
	// one. It is for an affordance that belongs to the box's contents but does not
	// deserve a row of its own — a whole box drawn to hold one line of help text is
	// furniture, and it costs the rows the content needed.
	footer []seg
}

// borderColor returns the box border / chrome color for the focus state.
func (b boxPanel) borderColor() lipgloss.TerminalColor {
	if b.focused {
		return cBlue
	}
	return cRule
}

// titleRule builds "<lead> Title ──…── badge <tail>" sized to width — the one
// piece of chrome math that puts a box's title and badge at a given offset. Both
// the drawn box's top border and the collapsed one-line rule go through it, so a
// pane can never put its title in one place when boxed and another when not; the
// only thing that differs between them is which glyph caps each end.
func (b boxPanel) titleRule(lead, tail string) string {
	bc := b.borderColor()
	titleColor := cDim
	if b.focused {
		titleColor = cBright
	}
	badgeColor := cFaint
	if b.focused {
		badgeColor = cCyan
	}
	segs := []seg{{t: lead + " ", fg: bc}, {t: b.title, fg: titleColor}, {t: " ", fg: bc}}
	badgeTxt := ""
	if b.badge != "" {
		badgeTxt = b.badge + " "
	}
	// lead + " " + title + " ", then the closing tail glyph.
	used := vlen(lead) + 1 + vlen(b.title) + 1
	dashes := b.width - used - vlen(badgeTxt) - vlen(tail)
	if dashes < 0 {
		dashes = 0
	}
	segs = append(segs, seg{t: strings.Repeat("─", dashes), fg: bc})
	if badgeTxt != "" {
		segs = append(segs, seg{t: badgeTxt, fg: badgeColor})
	}
	segs = append(segs, seg{t: tail, fg: bc})
	return renderSegs(fitSegs(segs, b.width), nil)
}

// topBorder builds "┌─ Title ──…── badge ┐" sized to width.
func (b boxPanel) topBorder() string { return b.titleRule("┌─", "┐") }

// renderRule is the box collapsed to a single line, corners opened out so it
// reads as a rule rather than the top of a box whose bottom went missing. It is
// what a pane with nothing to list renders instead of a box: the borders are
// there to enclose rows, and with no rows the chrome is the whole box. The title
// and badge stay, so an empty pane still says what it is and that it holds zero.
// It is also what render draws for a box one row tall.
func (b boxPanel) renderRule() string { return b.titleRule("──", "─") }

// render returns exactly height lines for the box.
// innerRow says what the i-th row inside the box shows: the index into b.lines
// drawn there (-1 for a row past the end), or the overflow hint that eats the
// last row when the content is taller than the box. render() draws by it and the
// click hit-test reads by it, so the two cannot disagree about which row is where.
func (b boxPanel) innerRow(i int) (li int, hint bool) {
	innerH := b.height - 2
	if i < 0 || i >= innerH {
		return -1, false
	}
	if i == innerH-1 && len(b.lines) > b.scroll+innerH {
		return -1, true
	}
	if idx := b.scroll + i; idx < len(b.lines) {
		return idx, false
	}
	return -1, false
}

func (b boxPanel) render() []string {
	// Two borders need two rows. A shorter box collapses to the rule (one row) or
	// to nothing, so the caller's height budget holds.
	switch {
	case b.height <= 0:
		return nil
	case b.height == 1:
		return []string{b.renderRule()}
	}
	bc := b.borderColor()
	innerW := b.width - 2
	if innerW < 0 {
		innerW = 0
	}
	innerH := b.height - 2
	border := renderSegs([]seg{{t: "│", fg: bc}}, nil)
	out := make([]string, 0, b.height)
	out = append(out, b.topBorder())
	total := len(b.lines)
	for i := 0; i < innerH; i++ {
		li, hint := b.innerRow(i)
		// Scroll hint replaces the last visible inner line when content overflows.
		if hint {
			more := total - (b.scroll + innerH)
			inner := fitSegs([]seg{{t: " ↓ " + strconv.Itoa(more) + " more", fg: cFaint}}, innerW)
			out = append(out, border+renderSegs(inner, nil)+border)
			continue
		}
		var lineSegs []seg
		if li >= 0 {
			lineSegs = b.lines[li]
		}
		inner := fitSegs(lineSegs, innerW)
		var bg lipgloss.TerminalColor
		if li == b.active {
			if b.focused {
				bg = cSel
			} else {
				bg = cSelDim
			}
		}
		out = append(out, border+renderSegs(inner, bg)+border)
	}
	out = append(out, b.bottomBorder(innerW))
	return out
}

// bottomBorder builds "└──…── footer ─┘" sized to innerW, or a plain rule when
// there is no footer.
func (b boxPanel) bottomBorder(innerW int) string {
	bc := b.borderColor()
	fw := segsWidth(b.footer)
	// 3 cells of rule either side keep the text from reading as part of the corner.
	if fw == 0 || fw+6 > innerW {
		return renderSegs([]seg{
			{t: "└", fg: bc}, {t: strings.Repeat("─", innerW), fg: bc}, {t: "┘", fg: bc},
		}, nil)
	}
	segs := []seg{
		{t: "└", fg: bc}, {t: strings.Repeat("─", innerW-fw-3), fg: bc}, {t: " ", fg: bc},
	}
	segs = append(segs, b.footer...)
	segs = append(segs, seg{t: " ", fg: bc}, seg{t: "─", fg: bc}, seg{t: "┘", fg: bc})
	return renderSegs(segs, nil)
}

// clampScroll returns a scroll top that keeps active visible within innerH rows.
func clampScroll(scroll, total, innerH, active int) int {
	if active >= 0 {
		if active < scroll {
			scroll = active
		}
		if active > scroll+innerH-1 {
			scroll = active - innerH + 1
		}
	}
	max := total - innerH
	if max < 0 {
		max = 0
	}
	if scroll > max {
		scroll = max
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll
}
