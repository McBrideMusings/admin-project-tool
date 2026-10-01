package tui

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/slug"
)

// beadAgeW is the age column: "11mo" is the widest value compactSince produces.
const beadAgeW = 4

// beadIDW caps the id column. One timestamp-import id
// ("apple-notepad-1786207837888-76-90c93d5a.11") sized the column for all 67
// rows in the widest workspace measured, spending 42 of 200 cells on a value
// nobody reads end to end.
const beadIDW = 12

// shortBeadID renders a bead id for display: the workspace prefix drops out
// (every id in the pane carries it, so it distinguishes nothing) and the rest is
// capped, keeping the tail because that is the part that differs.
//
// Display only. beadBranch and rowItem.beadID keep the full id, because a
// worktree branch name and the enter-on-bead path both have to name a real bead.
func shortBeadID(id, prefix string) string {
	if prefix != "" {
		id = strings.TrimPrefix(id, prefix+"-")
		id = strings.TrimPrefix(id, prefix)
	}
	if vlen(id) > beadIDW {
		id = truncL(id, beadIDW)
	}
	return id
}

// beadRows builds the Beads view. Beads are grouped by what the dependency graph
// says you can actually pick up — ready work first, then what's already moving,
// then what's waiting on something else — rather than by who owns them, which is
// what the Issues tab does with GitHub's assignee. A bead's owner is a column
// here; its blockers are the thing worth grouping by.
//
// Deferred beads are left out: they are on ice by definition. They still count
// as blockers for anything that depends on them, which is why they're fetched
// and only dropped at render time.
func (m Model) beadRows() []rowItem {
	if m.beadsErr != "" {
		return []rowItem{{kind: rowText, label: m.beadsErr}}
	}
	cols := m.cols()
	switch m.beadTreeMode {
	case beadListBlocksTree:
		return m.beadTreeRows(cols)
	case beadListFamilyTree:
		return m.beadFamilyTreeRows(cols)
	}
	human, ready, inProgress, blocked := groupBeads(m.beads)
	ctx := m.beadCtx(false)
	for _, s := range [][]bd.Bead{human, ready, inProgress, blocked} {
		sortBeadsBy(s, m.trackSort, ctx.unblocks)
	}
	l := beadLayoutFor(m.beadCols, cols, ctx, human, ready, inProgress, blocked)

	secs := []struct {
		title string
		beads []bd.Bead
	}{
		{"Needs you", human},
		{"Ready", ready},
		{"In progress", inProgress},
		{"Blocked", blocked},
	}

	// While loading, every section stays up so the "loading…" placeholder shows;
	// once beads have landed, an empty group drops out entirely. The human
	// section is the exception: it drops on an empty group even mid-load, so a
	// workspace with no human-labelled bead never shows a "Needs you / loading…"
	// placeholder above sections that do have one.
	var rows []rowItem
	for _, s := range secs {
		if len(s.beads) == 0 && (s.title == "Needs you" || m.haveBeads) {
			continue
		}
		if len(rows) > 0 {
			rows = append(rows, blankRow())
		}
		rows = append(rows, headerRow(s.title, countSeg(len(s.beads))...), ruleRow())
		rows = append(rows, m.beadSection(s.beads, cols, l)...)
	}
	if len(rows) == 0 {
		rows = append(rows, rowItem{kind: rowText, label: emptyOrLoading(m.haveBeads, "none")})
	}
	return rows
}

// beadTreeRows renders the dependency forest: one flat sequence where a blocker
// sits directly above the work it holds up. The Ready / In progress / Blocked
// headers go away — a root of the forest is exactly a ready bead, so the tree's
// own shape says what the sections said — and the blocker column goes with them,
// because position now carries what it named. What position cannot carry is
// status and a second blocker, so a glyph column marks in-progress work and a
// "+N" after the title counts the blockers this row's position left out.
func (m Model) beadTreeRows(cols int) []rowItem {
	nodes := beadForest(m.beads)
	if len(nodes) == 0 {
		return []rowItem{{kind: rowText, label: emptyOrLoading(m.haveBeads, "none")}}
	}
	beads := make([]bd.Bead, 0, len(nodes))
	for _, n := range nodes {
		beads = append(beads, n.b)
	}
	l := beadLayoutFor(m.beadCols, cols, m.beadCtx(true), beads)

	rows := make([]rowItem, 0, len(nodes))
	for _, n := range nodes {
		left, right := l.row(n, cols)
		rows = append(rows, rowItem{
			kind: rowBead, leftSegs: left, rightSegs: right,
			beadID: n.b.ID, beadTitle: n.b.Title,
		})
	}
	return rows
}

// beadFamilyTreeRows renders the parent-child forest: one flat sequence where a
// bead sits directly above its children. It is beadTreeRows with beadForest
// swapped for beadFamilyForest — same layout call, same row rendering, because
// beadcolumns.go's row() gates indent and the hidden blocker column on
// c.tree, not on which tree produced the node.
func (m Model) beadFamilyTreeRows(cols int) []rowItem {
	nodes := beadFamilyForest(m.beads)
	if len(nodes) == 0 {
		return []rowItem{{kind: rowText, label: emptyOrLoading(m.haveBeads, "none")}}
	}
	beads := make([]bd.Bead, 0, len(nodes))
	for _, n := range nodes {
		beads = append(beads, n.b)
	}
	l := beadLayoutFor(m.beadCols, cols, m.beadCtx(true), beads)

	rows := make([]rowItem, 0, len(nodes))
	for _, n := range nodes {
		left, right := l.row(n, cols)
		rows = append(rows, rowItem{
			kind: rowBead, leftSegs: left, rightSegs: right,
			beadID: n.b.ID, beadTitle: n.b.Title,
		})
	}
	return rows
}

// beadCtx assembles what the columns need about the pane. The unblocks counts
// are a whole-graph fact, so they are derived here from the full fetch — the
// section a bead lands in must not change how much work it frees.
func (m Model) beadCtx(tree bool) beadCtx {
	return beadCtx{prefix: m.beadPrefix, tree: tree, unblocks: bd.Unblocks(m.beads), workers: livework.Default.WorkerStates()}
}

// beadHumanLabel is bd's own HITL (human-in-the-loop) marker — the one bare
// label the schema permits (`bd human list`, `bd human respond`) — and the
// only label groupBeads treats specially.
const beadHumanLabel = "human"

// beadNeedsHuman reports whether a bead carries bd's human label: it is
// blocked on a person, not on the dependency graph. This is the single source
// of truth both render modes check — tree mode's glyph and flat mode's
// section — so the two can't drift on what counts as a human bead.
func beadNeedsHuman(b bd.Bead) bool {
	for _, l := range b.Labels {
		if strings.EqualFold(strings.TrimSpace(l), beadHumanLabel) {
			return true
		}
	}
	return false
}

// groupBeads splits the fetched beads into the four sections. A human bead is
// peeled off before status is even considered: it is waiting on a person, not
// the dependency graph, so it floats to its own section regardless of whether
// it also happens to be in progress or blocked — one row, one section, never
// duplicated. A bead already in progress stays in that section even when
// something blocks it: it is work someone has their hands on, and burying it
// under "Blocked" would hide that.
func groupBeads(beads []bd.Bead) (human, ready, inProgress, blocked []bd.Bead) {
	open := bd.OpenSet(beads)
	for _, b := range beads {
		switch {
		case b.Status == "deferred":
			continue
		case beadNeedsHuman(b):
			human = append(human, b)
		case b.Status == "in_progress":
			inProgress = append(inProgress, b)
		case b.Status == "blocked" || bd.IsBlocked(b, open):
			blocked = append(blocked, b)
		default:
			ready = append(ready, b)
		}
	}
	return human, ready, inProgress, blocked
}

func (m Model) beadSection(beads []bd.Bead, cols int, l beadLayout) []rowItem {
	if len(beads) == 0 {
		return []rowItem{{kind: rowText, label: emptyOrLoading(m.haveBeads, "none")}}
	}
	rows := make([]rowItem, 0, len(beads))
	for _, b := range beads {
		left, right := l.row(beadNode{b: b}, cols)
		rows = append(rows, rowItem{
			kind: rowBead, leftSegs: left, rightSegs: right,
			beadID: b.ID, beadTitle: b.Title,
		})
	}
	return rows
}

// beadStatusGlyph is the one cell every list mode spends on status. Tree mode
// has no section headers, so the marker is the whole of what "In progress"
// said there; flat mode repeats its headings on the row so a row read alone
// says the same. A human bead outranks in_progress: a bead blocked on you is
// the more urgent fact, and the row has only one cell to spend on either.
func beadStatusGlyph(b bd.Bead) string {
	switch {
	case beadNeedsHuman(b):
		return "?"
	case b.Status == "in_progress":
		return "\u25b6"
	default:
		return " "
	}
}

// unblocksText is how much open work finishing this bead would free, or "" when
// the answer is none. It replaced a column naming the bead's own blocker, which
// said what the Blocked heading already said and named it with an id you could
// not read or jump to. This asks the question the id could not: is this bead
// worth doing first?
func unblocksText(b bd.Bead, c beadCtx) string {
	n := c.unblocks[b.ID]
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// unblocksColor lifts a bead out of the list once it gates enough work to be
// worth reordering the day around. Below the threshold the number is context, not
// a call to action, so it stays as quiet as the other trailing columns.
func unblocksColor(n int) lipgloss.TerminalColor {
	if n >= 3 {
		return cGreen
	}
	return cDim
}

// priorityLabel renders beads' 0–4 priority as P0…P4.
func priorityLabel(p int) string { return "P" + strconv.Itoa(p) }

// priorityColor tints the priority column so the top of the list reads as the
// top of the list: P0 is a fire, P1 wants attention, everything below is quiet.
func priorityColor(p int) lipgloss.TerminalColor {
	switch p {
	case 0:
		return cRed
	case 1:
		return cYellow
	default:
		return cDim
	}
}

// compactSince renders how long ago a bead was touched in at most four cells
// ("now", "5m", "3h", "6d", "11mo"), matching the branch pane's age column.
func compactSince(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	case d < 7*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	case d < 365*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24/7)) + "w"
	default:
		return strconv.Itoa(int(d.Hours()/24/365)) + "y"
	}
}

// nameCandMax caps the bead search under the new-worktree prompt at five rows —
// enough to recognize the bead you meant without the footer swallowing the body.
const nameCandMax = 5

// beadCandidates ranks the loaded beads against the new-worktree prompt's typed
// text: an exact id match first, then an id prefix, then a title substring —
// everything else is not a candidate at all. Both the full id and the short id
// the Beads pane actually displays are checked, because a user searching from
// what's on screen types the short form, not the workspace-prefixed one.
func beadCandidates(beads []bd.Bead, prefix, buf string) []bd.Bead {
	q := strings.ToLower(strings.TrimSpace(buf))
	if q == "" {
		return nil
	}
	type ranked struct {
		b    bd.Bead
		rank int
	}
	var matches []ranked
	for _, b := range beads {
		id := strings.ToLower(b.ID)
		short := strings.ToLower(shortBeadID(b.ID, prefix))
		title := strings.ToLower(b.Title)
		switch {
		case id == q || short == q:
			matches = append(matches, ranked{b, 0})
		case strings.HasPrefix(id, q) || strings.HasPrefix(short, q):
			matches = append(matches, ranked{b, 1})
		case strings.Contains(title, q):
			matches = append(matches, ranked{b, 2})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].rank != matches[j].rank {
			return matches[i].rank < matches[j].rank
		}
		return matches[i].b.ID < matches[j].b.ID
	})
	if len(matches) > nameCandMax {
		matches = matches[:nameCandMax]
	}
	out := make([]bd.Bead, len(matches))
	for i, r := range matches {
		out[i] = r.b
	}
	return out
}

// refreshNameCands recomputes the new-worktree prompt's bead search from the
// current nameBuf. It always drops any prior highlight and only picks a fresh
// one back up when the typed text is now an exact id match — that is the one
// case the issue calls out for auto-highlighting; a prefix or title match never
// pre-selects, since it may not be the bead the user meant.
func (m *Model) refreshNameCands() {
	m.nameCands = beadCandidates(m.beads, m.beadPrefix, m.nameBuf)
	m.nameSel = -1
	q := strings.ToLower(strings.TrimSpace(m.nameBuf))
	for i, b := range m.nameCands {
		id := strings.ToLower(b.ID)
		short := strings.ToLower(shortBeadID(b.ID, m.beadPrefix))
		if id == q || short == q {
			m.nameSel = i
			break
		}
	}
}

// beadBranch derives the branch name a worktree gets when you start work on a
// bead: the bead's own id, then a few words of its title. The id already carries
// the workspace prefix, so "bd-4f2-add-password-reset" names its bead outright.
func beadBranch(id, title string) string {
	return slug.FromBead(id, title)
}
