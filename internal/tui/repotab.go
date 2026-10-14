package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/viewstate"
)

// repoTab is the Repo tab as its own model (ADR-0026): its three lazygit-style
// panes with their cursors, the Branches multi-select, the `G` narrowing, the
// commit/stash strip toggle and the new-worktree name prompt. Everything it
// knows about the repository comes through the handles its constructor asked
// for — the sync (open PRs, issues, the merged-PR head set), the beads list
// (the name prompt's bead search), the signed-in account (which branches are
// yours) and the local git read.
//
// Its methods read those handles only through repoView, the tab's data and
// the shell, joined for the length of one call.
type repoTab struct {
	declared []source
	syncSrc  syncSource
	beadsSrc beadsSource
	identSrc identitySource
	localSrc localSource

	ctx context.Context
	// nwo is the repository's owner/name; def its default branch.
	nwo, def string

	// pane is the focused panel; each pane keeps its own cursor so a trip
	// between them lands on the row it left.
	pane       localPane
	paneCursor [numPanes]int
	// historyStashes is which list the strip under Branches shows: false is
	// Commits, true is Stashes. The strip shows one at a time, so the hidden list's
	// pane is never focusable (paneShown).
	historyStashes bool
	// minesOnly (G) narrows the Branches pane to your work. Clean Up is exempt —
	// see branchRows. Persisted to view.toml's branches.mine_only (admin-y5lc.1):
	// the constructor takes the remembered value and every `G` press saves it to
	// the project-level file.
	minesOnly bool
	// selected is the Branches pane's multi-select, keyed by branch name — `space`
	// toggles the row under the cursor, `x` with a nonempty selection deletes the
	// whole batch together instead of just the one row under the cursor. This is
	// what lets a mixed pick (some proven, some merely eyeballed) go through one
	// confirm, which neither `x` alone (one row) nor `⇧X` (every proven-ready row,
	// no exceptions) can express.
	// The selection is the whole of select mode's state: there is no separate
	// latch, so the pane cannot be in select mode with nothing selected.
	selected map[string]bool

	// The new-worktree prompt: `w` on the Branches pane opens it, typing fills
	// nameBuf. resolving is true while an enter'd nameBuf is out at
	// resolveNameCmd, which holds the keys. nameErr is the inline validation
	// message shown under an invalid nameBuf.
	naming    bool
	nameBuf   string
	nameErr   string
	resolving bool
	// nameCands is the bead search under the naming prompt in a beads repo,
	// recomputed on every edit from the beads list (see refreshNameCands).
	// nameSel is -1 when nothing in the list is highlighted, in which case enter
	// creates a branch from the literal typed text — only an exact id match
	// auto-highlights a candidate, and editing the buffer always drops the
	// highlight again.
	nameCands []bd.Bead
	nameSel   int
}

// newRepoTab builds the tab, declaring the sources it reads by asking for
// their handles. minesOnly is the narrowing view.toml remembered.
func newRepoTab(f feeds, ctx context.Context, nwo, def string, minesOnly bool) repoTab {
	d := sourceSet{f: f}
	t := repoTab{
		syncSrc: d.sync(), beadsSrc: d.beads(), identSrc: d.identity(), localSrc: d.local(),
		ctx: ctx, nwo: nwo, def: def,
		minesOnly: minesOnly,
		selected:  map[string]bool{},
		nameSel:   -1,
	}
	t.declared = d.declared
	return t
}

// repoView is the Repo tab for the length of one call: the tab itself, the
// shell it is mounted in, and the data its handles currently answer. Its
// methods are the tab's reads and draws; the data fields are filled from the
// handles alone.
type repoView struct {
	*repoTab
	*shell
	local     *localData
	remote    *data.Remote
	identity  data.Identity
	haveLocal bool
}

// view joins the tab with sh and the current answer of each handle.
func (t *repoTab) view(sh *shell) repoView {
	return repoView{
		repoTab: t, shell: sh,
		local: t.localSrc.data(), remote: t.syncSrc.remote(), identity: t.identSrc.who(),
		haveLocal: t.localSrc.loaded(),
	}
}

// repo is the mounted Repo tab joined with the shell.
func (m *Model) repo() repoView { return m.repoTab.view(&m.shell) }

func (t *repoTab) sources() []source { return t.declared }

// cursorRef is the focused pane's cursor.
func (t *repoTab) cursorRef() *int { return &t.paneCursor[t.pane] }

// rows is nil: the tab draws panes, not the flat row list.
func (t *repoTab) rows(*shell) []rowItem { return nil }

// modal reports the name prompt, which owns the keyboard until it closes.
func (t *repoTab) modal() bool { return t.naming }

// key handles the keys the Repo tab owns. While the name prompt is up it takes
// every key. Otherwise: tab / shift+tab cycle the focused pane, c swaps the
// strip, and on the Branches pane p pulls, G narrows to your work, w opens the
// name prompt, space selects and x deletes; ⇧X sweeps from any pane. Every
// other key, and these on another pane, report unhandled so the shell's
// built-ins run.
func (t *repoTab) key(sh *shell, msg tea.KeyMsg) (bool, tea.Cmd) {
	if t.naming {
		return true, t.nameKey(sh, msg)
	}
	v := t.view(sh)
	onBranches := t.pane == paneBranches
	switch msg.String() {
	case "tab":
		t.pane = v.nextVisiblePane(t.pane, 1)
		return true, nil
	case "shift+tab":
		t.pane = v.nextVisiblePane(t.pane, -1)
		return true, nil
	case "c":
		v.toggleHistory()
		return true, nil
	case "p":
		if onBranches {
			sh.setStatus("pulling…", cDim)
			return true, pullCmd(t.ctx, sh.checkoutDir())
		}
	case "G":
		if onBranches {
			v.toggleMinesOnly()
			return true, nil
		}
	case "w":
		if onBranches {
			t.naming = true
			t.nameBuf = ""
			t.nameCands = nil
			t.nameSel = -1
			return true, nil
		}
	case " ":
		// Select mode is the selection, so a refused toggle changes nothing: on
		// the protected main row or a branch checked out elsewhere, `space`
		// leaves the pane exactly as it found it.
		if onBranches {
			v.toggleBranchSelection()
			return true, nil
		}
	case "x":
		// With a selection, x takes the whole batch — a mixed pick (some proven
		// ready, some merely eyeballed) that neither a single x nor ⇧X (every
		// proven-ready row, no exceptions) can express on its own. With nothing
		// selected it falls back to the row under the cursor.
		if onBranches {
			if len(t.selected) > 0 {
				v.deleteSelectedBranches()
			} else {
				v.deleteBranchUnderCursor()
			}
			return true, nil
		}
	case "X":
		// Clean-all works from any Repo-tab pane: the collapsed Cleanup box advertises
		// it from all of them, and it needs no per-row cursor.
		v.cleanAll()
		return true, nil
	}
	return false, nil
}

// nameKey is the new-worktree name entry: it swallows every key. Enter
// resolves the text through the same path as `repo branch` — a branch name, an
// issue/PR number, or a GitHub issue/PR/project URL all land on a branch — and
// the outcome arrives as a nameResolvedMsg. Esc cancels.
func (t *repoTab) nameKey(sh *shell, msg tea.KeyMsg) tea.Cmd {
	// A lookup in flight freezes the buffer: editing or re-submitting it would
	// leave the returned plan describing text that is no longer on screen. Esc
	// still gets through, so a slow lookup is never a trap.
	if t.resolving && msg.Type != tea.KeyEsc {
		return nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		t.closeNamePrompt()
		t.resolving = false
	case tea.KeyUp:
		if t.nameSel > -1 {
			t.nameSel--
		}
	case tea.KeyDown:
		if t.nameSel < len(t.nameCands)-1 {
			t.nameSel++
		}
	case tea.KeyEnter:
		name := strings.TrimSpace(t.nameBuf)
		if name == "" {
			t.closeNamePrompt()
			return nil
		}
		// A highlighted candidate (only ever set by an exact id match or by
		// ↑/↓) takes that bead's id instead of the literal text; with no
		// highlight the typed text is what gets resolved.
		if t.nameSel >= 0 && t.nameSel < len(t.nameCands) {
			name = t.nameCands[t.nameSel].ID
		}
		// Resolution reaches the network for a number or URL, so the prompt
		// stays up in a resolving state and the outcome arrives as a
		// nameResolvedMsg — which either creates or reports the failure inline.
		t.resolving = true
		t.nameErr = ""
		return resolveNameCmd(t.ctx, sh.checkoutDir(), t.nwo, name)
	case tea.KeyBackspace:
		if len(t.nameBuf) > 0 {
			t.nameBuf = t.nameBuf[:len(t.nameBuf)-1]
		}
		t.nameErr = ""
		t.refreshNameCands()
	case tea.KeyRunes:
		t.nameBuf += string(msg.Runes)
		t.nameErr = ""
		t.refreshNameCands()
	}
	return nil
}

// closeNamePrompt puts the prompt away and forgets what was typed.
func (t *repoTab) closeNamePrompt() {
	t.naming = false
	t.nameBuf = ""
	t.nameErr = ""
	t.nameCands = nil
	t.nameSel = -1
}

// refreshNameCands recomputes the new-worktree prompt's bead search from the
// current nameBuf. It always drops any prior highlight and only picks a fresh
// one back up when the typed text is now an exact id match — that is the one
// case the issue calls out for auto-highlighting; a prefix or title match never
// pre-selects, since it may not be the bead the user meant.
func (t *repoTab) refreshNameCands() {
	t.nameCands = beadCandidates(t.beadsSrc.list(), t.beadsSrc.prefix(), t.nameBuf)
	t.nameSel = -1
	q := strings.ToLower(strings.TrimSpace(t.nameBuf))
	for i, b := range t.nameCands {
		id := strings.ToLower(b.ID)
		short := strings.ToLower(shortBeadID(b.ID, t.beadsSrc.prefix()))
		if id == q || short == q {
			t.nameSel = i
			break
		}
	}
}

func (t *repoTab) body(sh *shell, width, height int) ([]string, []hitTarget) {
	return t.view(sh).renderLocalPanels(width, height)
}

// footer replaces the keybar with the name prompt while it is up: the typed
// text, then the bead search under it.
func (t *repoTab) footer(width int) (string, bool) {
	if !t.naming {
		return "", false
	}
	hint, hintFg := "(name, #number or URL · enter to create · esc to cancel)", cFaint
	if t.resolving {
		hint = "(resolving… · esc to cancel)"
	}
	if t.nameErr != "" {
		hint, hintFg = "("+t.nameErr+" · esc to cancel)", cRed
	}
	line := renderSegs([]seg{
		{t: " "}, {t: "new worktree: ", fg: cDim}, {t: t.nameBuf, fg: cFg},
		{t: "▌", fg: cBlue},
		{t: "   " + hint, fg: hintFg},
	}, nil)
	for _, cand := range t.nameCandLines(width) {
		line += "\n" + cand
	}
	return line, true
}

// nameCandLines renders the new-worktree prompt's bead search as extra footer
// rows: id / priority / type / title, in the same fixed-width columns the Beads
// pane draws, so the search list reads as a table like every other list in this
// repo (see CLAUDE.md, "Row layout: every list pane is a table"). Reusing
// beadLayoutFor rather than hand-rolling padding means these columns can never
// disagree with the Beads pane's own.
//
// Empty in every case that predates this feature — no beads loaded, or nothing
// typed yet — so a repo with no beads workspace draws a footer byte-identical to
// before this prompt could search anything.
func (t *repoTab) nameCandLines(width int) []string {
	if len(t.nameCands) == 0 {
		return nil
	}
	l := beadLayoutFor([]string{"id", "priority", "type", "title"}, width, beadCtxFor(t.beadsSrc.list(), t.beadsSrc.prefix(), false), t.nameCands)
	lines := make([]string, len(t.nameCands))
	for i, b := range t.nameCands {
		left, right := l.row(beadNode{b: b}, width)
		selected := i == t.nameSel
		line := append([]seg{leadSeg(selected)}, left...)
		fitted := fitSegs(composeLR(line, right, width), width)
		if selected {
			lines[i] = renderSegs(fitted, cSel)
		} else {
			lines[i] = renderSegs(fitted, nil)
		}
	}
	return lines
}

// count is the tab-bar badge: how many local branches this checkout has, the
// default branch not counted — the branches you could be working on. The
// pane's filter and the `G` narrowing do not apply: the badge counts the
// machine, not the view.
func (t *repoTab) count() int {
	n := 0
	for _, b := range t.localSrc.data().branches {
		if b.Name != t.def {
			n++
		}
	}
	return n
}

func (t *repoTab) loaded() bool { return t.localSrc.loaded() }

// freshness is the age of the merged-PR set, the tab's one remote input: what
// strikes a squash-merged branch through.
func (t *repoTab) freshness() seg {
	return cachedAgeSeg(t.syncSrc.remote().MergedFetchedAt)
}

func (t *repoTab) update(*shell, tea.Msg) tea.Cmd { return nil }

// toggleMinesOnly flips the `G` narrowing, refusing it while nothing defines
// whose work is "yours".
func (m repoView) toggleMinesOnly() {
	m.minesOnly = !m.minesOnly
	m.paneCursor[paneBranches] = 0
	if m.minesOnly {
		if !m.identity.Known() {
			// Narrowing to "yours" with nothing defining yours would empty the pane and
			// look like a repo with no work in it. Say which it is.
			m.minesOnly = false
			m.setStatus("your GitHub account is not known yet — nothing is known to be yours", cYellow)
			return
		}
		m.setStatus("showing your branches (Clean Up still shows everything)", cDim)
	} else {
		m.setStatus("showing every branch", cDim)
	}
	m.clampPanes()
	if err := viewstate.SaveMinesOnly(m.manifestDir(), m.minesOnly); err != nil {
		m.setStatus("view.toml: "+err.Error(), cRed)
	}
}

// clampPanes runs after every state change that can shrink a pane's rows
// (filtering, `G`, a refresh, a delete). It owns focus as well as cursors: once
// every pane's cursor is back in range, a focused pane left with zero rows
// hands focus to the next pane that still has rows, so a filter (or any other
// emptying change) never strands row-scoped keys on an inert pane.
// nextVisiblePane returns the current pane when nothing else has rows, so this
// is a no-op when every pane is empty.
func (m repoView) clampPanes() {
	for p := localPane(0); p < numPanes; p++ {
		m.paneCursor[p] = clampi(0, m.paneCursor[p], m.paneCount(p)-1)
	}
	if m.paneCount(m.pane) == 0 {
		m.pane = m.nextVisiblePane(m.pane, 1)
	}
}

// movePane moves the focused pane's cursor by delta, stopping at either end.
func (m repoView) movePane(delta int) {
	n := m.paneCount(m.pane)
	if n == 0 {
		return
	}
	m.paneCursor[m.pane] = clampi(0, m.paneCursor[m.pane]+delta, n-1)
}

// atTop reports whether the focused pane's cursor is on its first row.
func (m repoView) atTop() bool { return m.paneCursor[m.pane] == 0 }

// bodyRowCount is the selectable row count of the panes on screen.
func (m repoView) bodyRowCount() int {
	n := 0
	for p := localPane(0); p < numPanes; p++ {
		if m.paneShown(p) {
			n += m.paneCount(p)
		}
	}
	return n
}

// toggleHistory switches the strip under Branches between Commits and Stashes.
// Focus follows it when it was on the strip, so the key a user pressed with the
// cursor in Commits leaves the cursor in Stashes, not on a pane that just vanished
// — unless the list it switched to is empty, which has no row to focus, and then
// clampPanes hands focus to Branches.
func (m repoView) toggleHistory() {
	onStrip := m.pane == m.historyPane()
	m.historyStashes = !m.historyStashes
	if onStrip {
		m.pane = m.historyPane()
	}
	m.clampPanes()
}

// current synthesizes a rowItem for the focused pane's selected item so the
// shared enter/o handlers work for the Repo tab too.
func (m repoView) current() (rowItem, bool) {
	commits, stashes := m.localPanes()
	branches := m.branchInfos(m.branchRows())
	switch m.pane {
	case paneCleanup:
		// Unreachable in practice — paneCount is 0 here, so nextVisiblePane never
		// focuses it — but kept as a defensive no-op rather than assuming that
		// guarantee holds forever.
		return rowItem{}, false
	case paneCommits:
		i := m.paneCursor[paneCommits]
		if i < 0 || i >= len(commits) {
			return rowItem{}, false
		}
		c := commits[i]
		return rowItem{kind: rowCommit, label: c.SHA, url: commitURL(m.nwo, c.SHA)}, true
	case paneStashes:
		i := m.paneCursor[paneStashes]
		if i < 0 || i >= len(stashes) {
			return rowItem{}, false
		}
		s := stashes[i]
		return rowItem{kind: rowStash, label: s.Ref + "  " + s.Subject}, true
	default:
		i := m.paneCursor[paneBranches]
		if i < 0 || i >= len(branches) {
			return rowItem{}, false
		}
		b := branches[i]
		return rowItem{kind: rowBranch, label: b.Name, branch: b.Name, wtPath: b.WorktreePath}, true
	}
}

// branchSpace is the herdr workspace open on a branch's worktree, when the
// dashboard runs under herdr and one is. The dashboard's last poll answers it, so
// drawing a row never shells out.
func (m repoView) branchSpace(b git.BranchInfo) (string, bool) {
	if !m.herdr {
		return "", false
	}
	path := b.WorktreePath
	if path == "" {
		path = m.worktreeByBranch()[b.Name]
	}
	id, ok := m.local.herdrOpen[path]
	return id, ok && path != ""
}

// spaceAtRow is branchSpace for the Branches row at index i.
func (m repoView) spaceAtRow(i int) (string, bool) {
	branches := m.branchInfos(m.branchRows())
	if i < 0 || i >= len(branches) {
		return "", false
	}
	return m.branchSpace(branches[i])
}

// worktreeByBranch maps each branch checked out in a linked worktree to that
// worktree's path; the main tree is omitted.
func (m repoView) worktreeByBranch() map[string]string {
	return worktreesByBranch(m.local.wts, m.repoRoot)
}

// selectMode reports whether the Branches pane is in multi-select, which is
// exactly "at least one row is selected" — the mode has no state of its own, so
// it can neither be entered with an empty selection nor stay on after the last
// row is deselected. brPanelLine, branchRowLayout and stateColumnEnd all read
// this one predicate so the row's checkbox and the name column's width can never
// disagree about whether select mode is active.
func (m repoView) selectMode() bool {
	return len(m.selected) > 0
}
