// Package tui is the Bubble Tea dashboard: a tabbed shell over three views
// (Local, PRs, Issues) sharing one row/cursor/filter mechanic. Cleanup lives as
// a focusable pane in the Repo (Local) tab's right column.
package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fsnotify/fsnotify"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/shim"
)

type tab int

// tabWork is drawn first, and only when the project has an admin.toml
// (Model.manifestOK) — admin-3rgp.20's "a repo with no manifest has no Work
// tab at all, absent not disabled." Every other tab is unconditional or
// derived from remote/beads state exactly as before this ticket.
const (
	tabWork tab = iota
	tabLocal
	tabPRs
	tabIssues
	tabBeads
	tabConfig
	numTabs
)

var tabNames = [numTabs]string{"Work", "Local", "PRs", "Issues", "Beads", "Config"}

// rowKind drives how a row renders and what enter/o/i do on it.
type rowKind int

const (
	rowHeader rowKind = iota
	rowRule           // full-width rule under a section header
	rowCenter         // a centered empty-state message (whole-view)
	rowBranch
	rowText // section info / blank spacer — not selectable
	rowCommit
	rowStash
	rowPR
	rowIssue
	rowBead
	rowCommand       // Work tab: a declared admin.toml command, idle or live
	rowCommandTarget // Work tab: one target of an expanded multi-target command
	rowWorker        // Work tab: a livework.Entry with no command behind it
)

// rowItem is one line in a view. Selectable rows carry an action payload.
// leftSegs/rightSegs hold the colored content for list tabs (PRs/Issues/Cleanup);
// renderRow lays them out flush-left / flush-right at the real terminal width.
type rowItem struct {
	kind       rowKind
	label      string // section headers and plain-text rows
	leftSegs   []seg
	rightSegs  []seg
	branch     string
	wtPath     string
	prNum      int
	issueNum   int
	issueTitle string
	beadID     string
	beadTitle  string
	url        string

	// work carries a Work-tab row's payload — set only for rowCommand/
	// rowWorker, since the two share almost no fields with each other or
	// with any other tab's row (the epic's own reason the Work tab needs a
	// polymorphic detail pane rather than a wider rowItem).
	work *workRowData
}

func (r rowItem) selectable() bool {
	switch r.kind {
	case rowHeader, rowRule, rowCenter, rowText:
		return false
	}
	return true
}

// searchText is what the filter matches against: the plain label for text rows,
// or the concatenated segment text for the segment-based list rows.
func (r rowItem) searchText() string {
	if r.label != "" {
		return r.label
	}
	var b strings.Builder
	for _, s := range r.leftSegs {
		b.WriteString(s.t)
	}
	for _, s := range r.rightSegs {
		b.WriteString(s.t)
	}
	return b.String()
}

// PendingAction is work that must happen after the TUI tears down — anything
// that needs to cd the parent shell. cmd/root executes it.
type PendingAction struct {
	Kind     string // "cd", "create-cd", "pr-checkout-cd"
	Path     string // cd target (for "cd")
	Branch   string // branch to create a worktree for
	PRNum    int    // PR to check out
	RepoRoot string
}

// Model is the root dashboard model.
type Model struct {
	ctx      context.Context
	repoRoot string
	nwo      string
	current  string
	def      string

	width, height int

	active tab
	cursor [numTabs]int

	// Repo (local) tab: lazygit-style panes with independent cursors.
	pane       localPane
	paneCursor [numPanes]int

	local  localData
	remote data.Remote
	// The merged-PR head-branch set (branches a merged PR had as its head) lives on
	// remote as remote.MergedHeads, cached on disk right beside the PR/Issue
	// sections. It comes from GitHub, not git, so it must not live in localData —
	// that is replaced wholesale every time local git is re-read, and every sync
	// ends with exactly that reload, which would blank a set only the slower gh
	// call can refill. branchstate.go joins it with the git-derived signals at read
	// time; nothing merges the two sources into a stored value.
	haveLocal bool
	haveData  bool
	inflight  int // in-flight background work (the sync, row refreshes); header spins while > 0

	// Beads: whether the Beads tab is drawn at all comes from the declared
	// tracker key alone (trackerKind), never from these fields. The probe
	// they hold only runs for a repo that already declared tracker = "beads"
	// (Init), and decides what's inside that tab: hasBeads is whether the `bd
	// where` probe actually found a workspace, beadsProbed records that the
	// probe has answered — the launch sync waits for it, so a beads repo
	// never spends the `gh` issues call before knowing whether it needs one.
	// beadsErr holds either "declared but not found" or a workspace this
	// build can't read (a schema the `bd` on PATH doesn't agree with), which
	// the pane shows in place of rows rather than passing off as an empty
	// backlog.
	hasBeads    bool
	beadsProbed bool
	beadPrefix  string // bd workspace prefix, stripped off every displayed id
	beadTree    bool   // "g": dependency tree instead of the flat, grouped list
	beads       []bd.Bead
	// trackSort is the active order for the tracker list — the Beads tab's flat
	// list and the Issues tab — cycled by `s` (see tracksort.go). It is shared
	// across both tabs rather than kept per-tab: one key, one order, named in
	// the header. Session-scoped only: it lives on Model and resets on restart,
	// same as beadTree. It does not reach beadTreeRows — the dependency tree's
	// order already comes from the graph itself, and an arbitrary sort would
	// scramble the blocker-above-blocked shape that view exists to show.
	trackSort trackSort
	haveBeads   bool
	beadsErr    string
	// beadCols is [beads] columns from the merged config — which columns the pane
	// draws and in what order. Empty means DefaultBeadColumns.
	beadCols []string

	// Bead overlay: `d` opens a fixed-size box over the pane holding one of three
	// tabs — the selected bead's own text, its dependency neighbourhood, or the
	// beads that wait on it. The box is sized from the terminal and never from
	// its content, so cycling between tabs does not move the screen.
	beadOpen   bool           // the overlay is up
	beadTab    beadOverlayTab // which of the three tabs is up
	beadScroll int            // rows scrolled inside the box

	// One background sync covers everything the dashboard reads from outside the
	// working tree — open PRs, open issues, the merged-PR set, and `git fetch
	// --prune` — on one clock that does not care which tab is on screen (syncCmd).
	// Sections used to be fetched per tab visit, which meant the data you were
	// looking at was the data that went stale: sit on the PRs tab and nothing ever
	// re-fetched it, because only a *visit* could.
	//
	// syncing keeps a second sync from going out while one is in flight. synced
	// records that the launch sync has been considered, so the local-git reload
	// every sync ends with can't trigger another one.
	syncing bool
	synced  bool

	// syncGen counts completed syncs. A row-scoped refresh captures it when it
	// fires; if a sync lands before the row-fetch's response does, the generation
	// no longer matches and the stale row result is dropped instead of stomping
	// the fresher wholesale data.
	syncGen int

	fsCh      <-chan struct{}   // file-watcher signal; nil if watching is off
	fsWatcher *fsnotify.Watcher // owned so it can be closed on quit; nil if off

	filtering bool
	filter    string
	naming    bool // "w" on the Local tab: typing a name for a new worktree branch
	nameBuf   string
	nameErr   string // inline validation message shown under an invalid nameBuf
	resolving bool   // an enter'd nameBuf is out at resolveNameCmd; keys are held
	// nameCands is the bead search under the naming prompt in a beads repo,
	// recomputed on every edit from the already-loaded m.beads (see
	// refreshNameCands). nameSel is -1 when nothing in the list is highlighted, in
	// which case enter creates a branch from the literal typed text exactly as it
	// always has — only an exact id match auto-highlights a candidate, and editing
	// the buffer always drops the highlight again.
	nameCands []bd.Bead
	nameSel   int

	confirming     bool // cleanup: awaiting y/n on a batch delete
	confirmTargets []cleanupTarget
	// selected is the Branches pane's multi-select, keyed by branch name — `space`
	// toggles the row under the cursor, `x` with a nonempty selection deletes the
	// whole batch together instead of just the one row under the cursor. This is
	// what lets a mixed pick (some proven, some merely eyeballed) go through one
	// confirm, which neither `x` alone (one row) nor `⇧X` (every proven-ready row,
	// no exceptions) can express.
	// The selection is the whole of select mode's state: there is no separate
	// latch, so the pane cannot be in select mode with nothing selected.
	selected map[string]bool

	// Delete modal: a centered overlay that steps through the batch one git call
	// at a time so the UI repaints between items and shows live per-item progress.
	deleting      bool
	deleteDone    bool // every item processed; modal shows the summary, any key dismisses
	deleteTargets []cleanupTarget
	deleteIdx     int
	deleteResults []deleteResult
	// deleteForce is set only on the `f` retry over conflict-blocked items, and
	// tells the stepper to remove those worktrees without the auto-stash that
	// cannot run on an unmerged index. It rides on the model rather than the
	// target because every step of that retry batch takes the same answer.
	deleteForce bool

	status   string                 // transient action result line
	statusFg lipgloss.TerminalColor // its color (nil → dim)
	// statusHint is pinned flush-right on the status line and the message is
	// truncated to make room, so an affordance can't be the thing that falls off
	// the end — a long git refusal is exactly when "e for detail" has to be visible.
	statusHint string
	Pending    *PendingAction

	// Error detail: the status line holds one line, but a git refusal often carries
	// the part you actually need on the lines after its headline — the files a
	// checkout would overwrite, the worktree already holding a branch. Those lines
	// are parked here when an action fails and shown as a centered overlay on `e`,
	// rather than being dropped or smeared across the dashboard.
	errTitle  string // the action that failed, e.g. "pull failed"
	errDetail string // git's full output, headline included
	errOpen   bool   // the overlay is up

	// herdr mode: when running inside a herdr pane, enter on a worktree action
	// creates/opens it inline (opening a focused herdr workspace) and the
	// dashboard stays up, instead of quitting to cd the parent shell. creating
	// is set while that inline work is in flight and drives the header status.
	herdr       bool
	creating    bool
	createLabel string

	closedIssues bool // Issues view: show closed too (toggle) — reserved

	// identity is who "you" are — the merged [identity] from the global and
	// per-repo config, seeded on first run. It decides which branches the Branches
	// pane treats as your work; see ownership.go. Zero until settingsMsg lands,
	// which only means the first frame files every branch as Unsorted rather than
	// mis-filing any of them.
	identity config.Identity
	// identitySeeded is set when this launch wrote the starter [identity], so the
	// status line can say so once instead of on every launch.
	identitySeeded bool
	// minesOnly (g) narrows the Branches pane to your work. Clean Up is exempt —
	// see branchRows.
	minesOnly bool

	// sel is the in-progress or just-finished drag-to-copy gesture — anchor on
	// mouse-down, drag on motion. It is reset to its zero value (selection{},
	// phase selNone) in the same Update that finishes a real drag, so the
	// highlight is gone on the very next frame; selection never persists.
	sel selection
	// toastGen counts the "copied to clipboard" toasts shown, so a delayed
	// expiry tick can tell whether the status line still holds the toast it was
	// scheduled for, or whether a newer action has already replaced it.
	toastGen int

	// Work tab (admin-3rgp.20): the project's admin.toml, merged with its
	// declared archetypes, loaded once at New (a local file read plus a
	// read-only archetype-directory walk, cheap enough to do synchronously
	// so the first frame already knows whether a Work tab exists at all).
	// manifestOK is true only when repoRoot has an admin.toml — mirroring
	// cmd.resolveProjectManifest's own "no manifest here" vs "one here and
	// it's broken" distinction (duplicated in this package rather than
	// imported: cmd/ already imports tui, and importing back would cycle).
	// manifestErr holds a present-but-broken manifest's parse/resolve error;
	// either way the Work tab stays absent, per the ticket's "absent, not
	// disabled."
	manifest    *manifest.Manifest
	manifestOK  bool
	manifestErr string
	// cmdOrigin/actionOrigin name which archetype (or "(manifest)") last
	// contributed each command/action name — the detail pane's "archetype"
	// and each step's "from <archetype>" line. archetype.Resolve's own merge
	// log only records a COLLISION, not "here is who owns every key", so
	// these are computed by a second, read-only walk of the same search
	// path (computeOrigins).
	cmdOrigin    map[string]string
	actionOrigin map[string]string
	// keyOrigin names which archetype (or "(manifest)") declared each
	// scalar top-level manifest key the Config tab renders ("tracker",
	// "uses_pull_requests") — same mechanism as cmdOrigin/actionOrigin, via
	// computeOrigins. A key missing from the map was never declared, which
	// the Config tab shows as defaulted rather than guessing a source.
	keyOrigin map[string]string
	// expandedCmd is the ONE Work-tab command whose targets are currently
	// listed beneath it, or "" for none. A command with targets is a
	// collapse toggle: enter on it opens its target list and closes
	// whatever was open before, so exactly one submenu is ever on screen.
	// Held on the Model rather than on the row because workRows() rebuilds
	// every rowItem from scratch on each frame.
	expandedCmd string

	// Config tab (admin-3rgp.21): the merged config.Config plus the global-
	// only and local-only reads, so each field's origin (global file, local
	// file, or neither — defaulted) is legible rather than only its final
	// value. Loaded once at launch by loadConfigCmd; zero until settingsMsg
	// lands, same as identity above.
	cfgMerged config.Config
	cfgGlobal config.Config
	cfgLocal  config.Config
	cfgErr    string

	// Workflow confirm overlay (admin-3rgp.26): a query-sourced workflow row
	// (Work tab) never dispatches on a bare enter. Enter instead resolves the
	// query (wfResolving, while the resolveWorkflowCmd is in flight) and this
	// overlay is what shows the resolved set before anything runs — the
	// epic's "a query matching nine things must not be able to start nine
	// agents without the set having been seen." wfErr holds either a
	// resolution failure or "matched nothing"; either way there is nothing to
	// confirm and the overlay's only key is dismiss. A real, non-empty
	// wfTargets is what the confirm key (enter/y) hands to
	// dispatchWorkflowCmd — esc/n (or dismissing an error/empty result)
	// closes the overlay having dispatched nothing.
	wfResolving bool
	wfOpen      bool
	wfName      string
	wfAction    manifest.Action
	wfQuery     string
	wfTargets   []kinds.Target
	wfErr       string
	wfAgent     string
	wfCeiling   int
	// wfFromSelection distinguishes a confirm overlay opened from the tracker
	// picker below from one opened by a query-sourced Work-tab row: only the
	// former should clear trackerSelected once dispatch actually fires (see
	// selectionworkflow.go's dispatchTrackerSelection), and wfQuery alone
	// can't tell the two apart — a selection-sourced workflow still names a
	// real action, it just never had a query to resolve.
	wfFromSelection bool

	// trackerSelected is the Beads/Issues tabs' multi-select for a
	// selection-sourced Workflow (admin-3rgp.27, selectionworkflow.go): every
	// selected row's tracker id (a bead id, or a GitHub issue number as a
	// string), keyed the same "map length is the mode" way `selected` above
	// keys branch names — a completely different feature (running a
	// Workflow over picked rows, not staging branches for deletion), so it
	// gets its own map rather than overloading branch-name keys with tracker
	// ids that could theoretically collide with one.
	trackerSelected map[string]bool

	// Workflow picker overlay: `w` with a nonempty trackerSelected opens it,
	// listing every selection-sourced Workflow (selectionWorkflows) so the
	// user picks which one runs over the selection; enter on a candidate
	// hands off into the very same confirm overlay above (wfOpen etc.) — see
	// selectionworkflow.go's own doc comment for the full three-step flow.
	swOpen    bool
	swOptions []workCmdRow
	swCursor  int
}

// New builds the dashboard model. The cheap git context (root, nwo, branch) is
// gathered by the caller and passed in so the first frame can render immediately.
func New(ctx context.Context, repoRoot, nwo, current, def string) Model {
	m := Model{
		ctx:             ctx,
		repoRoot:        repoRoot,
		nwo:             nwo,
		current:         current,
		def:             def,
		active:          tabLocal,
		herdr:           shim.Available(),
		selected:        map[string]bool{},
		trackerSelected: map[string]bool{},
	}
	project, merged, sp, ok, err := loadWorkManifest(repoRoot)
	switch {
	case err != nil:
		m.manifestErr = err.Error()
	case ok:
		m.manifest = merged
		m.manifestOK = true
		m.cmdOrigin, m.actionOrigin, m.keyOrigin = computeOrigins(project, sp)
		// AC: "the dashboard opens on the Work tab when the project has a
		// manifest."
		m.active = tabWork
	}
	return m
}

// Init kicks off the local git load (instant), the cache read (instant), the file
// watcher, and the sync poll. The first sync itself waits for local git to land
// (see the localDataMsg case), so the first frame paints off git and cache with
// nothing blocking on the network.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		loadLocalCmd(m.ctx, m.repoRoot, m.current, m.def),
		loadCacheCmd(m.nwo),
		startWatchCmd(m.ctx),
		syncTickCmd(),
		loadConfigCmd(m.ctx, m.repoRoot),
	}
	if m.trackerKind() == "beads" {
		// The `bd where` probe only runs for a repo that declared tracker =
		// "beads" — its answer feeds the tab's content (which bead workspace,
		// or an error if none is found), never whether the tab is drawn at
		// all. A repo that declared "github" or "none" has no reason to pay
		// for this subprocess.
		cmds = append(cmds, detectBeadsCmd(m.ctx, m.repoRoot))
	}
	if m.manifestOK {
		// A herdr pane or agent worker changes state (running → waiting →
		// failed, a new Activity line) from goroutines this process never
		// hears from otherwise — livework.Default has no notification of
		// its own. This tick is the Work tab's only wake source for that,
		// mirroring beadsTickCmd's own reason for existing.
		cmds = append(cmds, workTickCmd())
	}
	return tea.Batch(cmds...)
}

// syncTickMsg fires on a repeating timer to run the whole-repo sync. Everything
// this dashboard shows from GitHub — a PR review landing, an issue closing, a
// branch merged and deleted on the remote — happens without touching a single
// local file, so the file watcher never hears about any of it. This timer is the
// only wake source that keeps an idle dashboard honest.
type syncTickMsg struct{}

// syncTickCmd schedules the next poll one data.PollInterval out. It re-arms
// itself on every fire (see the syncTickMsg case in Update), so the poll runs for
// the life of the process.
func syncTickCmd() tea.Cmd {
	return tea.Tick(data.PollInterval, func(time.Time) tea.Msg { return syncTickMsg{} })
}

// trackerKind returns the manifest's declared tracker ("beads", "github",
// "none", or any other value a future tracker tab wants to recognize), or ""
// when the manifest declares none (including when there is no manifest at
// all — m.manifest is nil exactly when m.manifestOK is false, since New()
// only ever sets the two together). Nothing here falls back to a `bd where`
// probe or any other guess — admin-3rgp.22 replaces exactly that inference
// with the declared key admin-3rgp.21 added to admin.toml.
func (m Model) trackerKind() string {
	if m.manifest == nil || !m.manifest.HasTracker {
		return ""
	}
	return m.manifest.Tracker
}

// wantsPRs reports whether the manifest declares uses_pull_requests = true.
// Undeclared defaults to false — the same default the Config tab shows
// (configtab.go's manifestConfigLines). The PRs tab's presence follows this
// declaration alone: zero open PRs is a legitimate state prRows renders as an
// empty section, not a reason for the tab itself to disappear.
func (m Model) wantsPRs() bool {
	return m.manifest != nil && m.manifest.HasUsesPullRequests && m.manifest.UsesPullRequests
}

// selectMode reports whether the Branches pane is in multi-select, which is
// exactly "at least one row is selected" — the mode has no state of its own, so
// it can neither be entered with an empty selection nor stay on after the last
// row is deselected. brPanelLine, branchRowLayout and stateColumnEnd all read
// this one predicate so the row's checkbox and the name column's width can never
// disagree about whether select mode is active.
func (m Model) selectMode() bool {
	return len(m.selected) > 0
}

// visibleTabs is the tab bar, in the order it is drawn. Only the Repo tab is
// unconditional: Work needs a manifest, PRs needs uses_pull_requests declared
// true, and the tracker tab is Issues or Beads depending on the declared
// tracker (or absent when none is declared). Both PRs and Issues still need a
// GitHub remote to mean anything; Beads does not, since beads is a local
// database. None of the three infers itself from live repo state — not from
// how many PRs happen to be open, and not from a `bd where` probe of the repo
// — that inference is exactly what admin-3rgp.22 replaced with the declared
// keys admin-3rgp.21 added. The digit keys index this list, so `3` is
// whatever the third drawn tab happens to be.
func (m Model) visibleTabs() []tab {
	var tabs []tab
	if m.manifestOK {
		tabs = append(tabs, tabWork)
	}
	tabs = append(tabs, tabLocal)
	if m.nwo != "" && m.wantsPRs() {
		tabs = append(tabs, tabPRs)
	}
	switch m.trackerKind() {
	case "github":
		if m.nwo != "" {
			tabs = append(tabs, tabIssues)
		}
	case "beads":
		tabs = append(tabs, tabBeads)
	}
	tabs = append(tabs, tabConfig)
	return tabs
}

// tabIndex is where t sits in the drawn order, or -1 when it isn't drawn at all.
func (m Model) tabIndex(t tab) int {
	for i, v := range m.visibleTabs() {
		if v == t {
			return i
		}
	}
	return -1
}

func (m Model) rows() []rowItem {
	switch m.active {
	case tabWork:
		return m.workRows()
	case tabPRs:
		return m.prRows()
	case tabIssues:
		return m.issueRows()
	case tabBeads:
		return m.beadRows()
	}
	// The Repo (local) tab uses the lazygit panes, not the flat row list.
	return nil
}

// visibleRows applies the active filter (substring on label), always keeping
// section headers so the structure stays legible.
func (m Model) visibleRows() []rowItem {
	all := m.rows()
	if m.filter == "" {
		return all
	}
	q := strings.ToLower(m.filter)
	var out []rowItem
	for _, r := range all {
		switch r.kind {
		case rowHeader, rowRule:
			out = append(out, r) // keep section structure
		default:
			if strings.Contains(strings.ToLower(r.searchText()), q) {
				out = append(out, r)
			}
		}
	}
	return out
}

// clampCursor runs after every state change that can shrink a pane's rows
// (filtering, `g`, a refresh, a delete). It owns focus as well as cursors: on
// the Repo tab, once every pane's cursor is back in range, a focused pane
// left with zero rows hands focus to the next pane that still has rows, so a
// filter (or any other emptying change) never strands row-scoped keys on an
// inert pane. nextVisiblePane returns the current pane when nothing else has
// rows, so this is a no-op when every pane is empty.
func (m *Model) clampCursor() {
	if m.active == tabLocal {
		for p := localPane(0); p < numPanes; p++ {
			m.paneCursor[p] = clampi(0, m.paneCursor[p], m.paneCount(p)-1)
		}
		if m.paneCount(m.pane) == 0 {
			m.pane = m.nextVisiblePane(m.pane, 1)
		}
		return
	}
	rows := m.visibleRows()
	n := len(rows)
	if n == 0 {
		m.cursor[m.active] = 0
		return
	}
	if m.cursor[m.active] >= n {
		m.cursor[m.active] = n - 1
	}
	if m.cursor[m.active] < 0 {
		m.cursor[m.active] = 0
	}
	// Row 0 of a list tab is a section header, which is not selectable, so a
	// cursor left sitting there makes every row-scoped key a silent no-op. Snap
	// forward through moveCursor rather than repeating its skip logic here.
	if !rows[m.cursor[m.active]].selectable() {
		m.moveCursor(1)
	}
}

func (m *Model) moveCursor(delta int) {
	if m.active == tabLocal {
		n := m.paneCount(m.pane)
		if n == 0 {
			return
		}
		m.paneCursor[m.pane] = clampi(0, m.paneCursor[m.pane]+delta, n-1)
		return
	}
	rows := m.visibleRows()
	n := len(rows)
	if n == 0 {
		return
	}
	i := m.cursor[m.active]
	for steps := 0; steps < n; steps++ {
		i += delta
		if i < 0 {
			i = n - 1
		}
		if i >= n {
			i = 0
		}
		if rows[i].selectable() {
			m.cursor[m.active] = i
			return
		}
	}
}

// setStatus sets the transient status line and its color in one place.
func (m *Model) setStatus(text string, fg lipgloss.TerminalColor) {
	m.status = text
	m.statusFg = fg
	m.statusHint = ""
}

// stashErrDetail parks a failed action's full git output for the `e` overlay, and
// clears any older one. Only a *git.CmdError carrying more than its headline gets
// stashed — for every other error the status line already says everything there is
// to say, and offering a detail view that repeats it would be a dead end.
func (m *Model) stashErrDetail(title string, err error) {
	m.errTitle, m.errDetail, m.errOpen = "", "", false
	var ce *git.CmdError
	if errors.As(err, &ce) && ce.HasDetail() {
		m.errTitle, m.errDetail = title, ce.Output
	}
}

// resetCursors sends every cursor back to the top — used when the filter changes.
// The clamp then snaps the active tab's cursor onto the first selectable row.
func (m *Model) resetCursors() {
	m.cursor[m.active] = 0
	m.paneCursor = [numPanes]int{}
	m.clampCursor()
}

// closeWatcher releases the file watcher (and its goroutine, which returns once
// the watcher's channels close). Called at every quit path so nothing is leaked.
func (m *Model) closeWatcher() {
	if m.fsWatcher != nil {
		_ = m.fsWatcher.Close()
		m.fsWatcher = nil
	}
}

// quitWith records the post-teardown action (a cd the parent shell must perform),
// closes the watcher, and quits.
func (m *Model) quitWith(p *PendingAction) (tea.Model, tea.Cmd) {
	m.Pending = p
	m.closeWatcher()
	return *m, tea.Quit
}

// switchTab makes t the active tab. It spends no network call: every remote
// section is refreshed by the background sync on its own clock, so which tab is
// on screen no longer decides what is current. A tab that isn't drawn (Issues in
// a beads repo, PRs with no remote) is not switchable to.
//
// A tab switch also ends any in-progress drag-to-copy gesture, the same as a
// mouse-up would: a held mouse button and a keypress are independent input
// sources, so a drag anchored on one tab can otherwise survive a `→`/`←` press
// and splice its highlight — or extract its copy — against a different tab's
// frame at stale coordinates.
func (m *Model) switchTab(t tab) {
	if m.tabIndex(t) < 0 {
		return
	}
	m.active = t
	m.sel = selection{}
	m.clampCursor()
}

// settleActiveTab moves off a tab that has just stopped being drawn. Every
// tab's presence is a declared, session-static fact now (admin-3rgp.22), so
// this has no live trigger left in practice — it stays as the one guard
// against landing on a tab index that turns out not to be drawn, rather than
// leaving a view no key can leave and no label points at.
func (m *Model) settleActiveTab() {
	if m.tabIndex(m.active) < 0 {
		m.active = tabLocal
		m.clampCursor()
	}
}

// stepTab moves delta places along the drawn tab order, wrapping at both ends.
func (m *Model) stepTab(delta int) {
	tabs := m.visibleTabs()
	i := m.tabIndex(m.active)
	if i < 0 {
		m.switchTab(tabs[0])
		return
	}
	m.switchTab(tabs[((i+delta)%len(tabs)+len(tabs))%len(tabs)])
}

// switchTabAt jumps to the nth drawn tab — what the digit keys do. Out of range
// (a `3` in a repo showing two tabs) does nothing.
func (m *Model) switchTabAt(n int) {
	tabs := m.visibleTabs()
	if n < 0 || n >= len(tabs) {
		return
	}
	m.switchTab(tabs[n])
}

// startSyncCmd returns the whole-repo sync when one is due — nothing already in
// flight, and either forced or the cache is past TTL — and nil otherwise. It is
// the single gate every sync goes through: the launch sync, the poll tick, and
// the `r`/`⇧R` override, so none of them can double up on the other's work.
//
// The poll tick fires further apart than TTL, so it always finds the cache stale
// and always syncs; the staleness check is what stops a relaunch in a fresh
// terminal from re-fetching data the disk cache already holds.
func (m *Model) startSyncCmd(force bool) tea.Cmd {
	withIssues := m.trackerKind() == "github"
	if m.syncing || !(force || m.remote.Stale(withIssues)) {
		return nil
	}
	m.syncing = true
	m.inflight++
	return syncCmd(m.ctx, m.nwo, withIssues)
}

// launchSyncCmd fires the one-time launch sync, but only once local git has
// landed (so a PR row paints with its worktree overlay the first time it is
// drawn) — and, for a repo that declared tracker = "beads", only once the
// probe that workspace's data comes from has also answered. Whether the sync
// itself asks GitHub for issues is decided by the declared tracker alone
// (startSyncCmd), so a "github" or "none" tracker never waits on the probe at
// all.
func (m *Model) launchSyncCmd() tea.Cmd {
	if m.synced || !m.haveLocal {
		return nil
	}
	if m.trackerKind() == "beads" && !m.beadsProbed {
		return nil
	}
	m.synced = true
	return m.startSyncCmd(false)
}

// refreshAllCmd is the manual override on the poll: re-read local git and force a
// sync now, whatever the clock says. Bound to `⇧R`, and to `r` anywhere a
// row-scoped refresh doesn't apply.
func (m *Model) refreshAllCmd() tea.Cmd {
	m.setStatus("refreshing…", cDim)
	// The launch sync is moot once an explicit one has been asked for; marking it
	// considered keeps the local reload below from firing a second sync.
	m.synced = true
	return tea.Batch(
		loadLocalCmd(m.ctx, m.repoRoot, m.current, m.def),
		m.startSyncCmd(true),
	)
}

// isRefreshing reports whether any background GitHub fetch is in flight, driving
// the header's sync indicator.
func (m Model) isRefreshing() bool { return m.inflight > 0 }

// fetchDone decrements the in-flight counter when a background fetch returns,
// floored at zero so a stray message can't drive it negative.
func (m *Model) fetchDone() {
	if m.inflight > 0 {
		m.inflight--
	}
}

func (m Model) currentRow() (rowItem, bool) {
	if m.active == tabLocal {
		return m.localCurrent()
	}
	rows := m.visibleRows()
	i := m.cursor[m.active]
	if i >= 0 && i < len(rows) && rows[i].selectable() {
		return rows[i], true
	}
	return rowItem{}, false
}

// localCurrent synthesizes a rowItem for the focused pane's selected item so the
// shared enter/o handlers work for the Repo tab too.
func (m Model) localCurrent() (rowItem, bool) {
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

// Update handles messages and key input.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case localDataMsg:
		m.local = msg.d
		m.haveLocal = true
		m.clampCursor()
		// The first local load is one of the two gates on the launch sync. Waiting
		// for git rather than firing at Init is what lets PR rows paint with their
		// worktree overlay (enter → cd) the first time they are drawn, instead of
		// after a re-render — and every later local reload leaves synced set, so the
		// reload each sync ends with cannot start another one.
		return m, m.launchSyncCmd()

	case beadsDetectedMsg:
		// The probe answered: does a beads workspace actually exist here. This
		// only fires for a repo that already declared tracker = "beads" (see
		// Init), so the Beads tab itself is never at stake — it's already
		// drawn on the declaration alone. This decides what's inside it: real
		// rows, or the "declared but not found" state below.
		m.beadsProbed = true
		m.hasBeads = msg.present
		m.beadPrefix = msg.ws.Prefix
		m.beadsErr = ""
		switch {
		case msg.err != nil:
			m.beadsErr = msg.err.Error()
		case !msg.present:
			m.beadsErr = `tracker = "beads" is declared, but no beads workspace was found here`
		}
		var cmds []tea.Cmd
		if m.hasBeads && m.beadsErr == "" {
			cmds = append(cmds, loadBeadsCmd(m.ctx, m.repoRoot), beadsTickCmd())
		}
		cmds = append(cmds, m.launchSyncCmd())
		m.clampCursor()
		return m, tea.Batch(cmds...)

	case beadsMsg:
		m.haveBeads = true
		if msg.err != nil {
			m.beadsErr = msg.err.Error()
		} else {
			m.beads = msg.beads
			m.beadsErr = ""
		}
		m.clampCursor()
		return m, nil

	case beadsTickMsg:
		// Beads live in a local database, so re-reading them costs a subprocess and
		// no network at all — nothing like the GitHub sync this dashboard rations to
		// one every five minutes. The tick is short enough that a bead an agent
		// creates mid-session shows up while you are still looking at the pane.
		if !m.hasBeads {
			return m, nil
		}
		return m, tea.Batch(loadBeadsCmd(m.ctx, m.repoRoot), beadsTickCmd())

	case cacheDataMsg:
		if msg.ok {
			m.remote = msg.r
			m.haveData = true
		}
		m.settleActiveTab()
		m.clampCursor()
		return m, nil

	case watcherReadyMsg:
		m.fsWatcher = msg.w
		m.fsCh = msg.ch
		return m, waitForFSCmd(m.fsCh)

	case fsChangedMsg:
		// A file changed on disk. Reload local git so the header's dirty state and
		// the branch list's unsaved-work marking stay live, then resume listening
		// for the next change.
		return m, tea.Batch(
			loadLocalCmd(m.ctx, m.repoRoot, m.current, m.def),
			waitForFSCmd(m.fsCh),
		)

	case syncDoneMsg:
		m.syncing = false
		m.syncGen++
		m.fetchDone()
		// ApplyTo overlays each section that came back clean and leaves a failed
		// one's cached rows and stamp alone, so one timed-out `gh` call empties
		// nothing on screen.
		msg.r.ApplyTo(&m.remote)
		m.haveData = true
		m.settleActiveTab()
		m.clampCursor()
		// The sync pruned remote-tracking refs, so "gone" and "on origin" need
		// recomputing — re-read local git rather than trying to patch the maps.
		return m, loadLocalCmd(m.ctx, m.repoRoot, m.current, m.def)

	case syncTickMsg:
		// The poll. It fires regardless of which tab is on screen — the merged set
		// behind the Repo tab and the PR list behind the PRs tab go stale at the
		// same rate whether or not you are looking at them. Re-arm unconditionally
		// so the next tick still comes even when this one found a sync in flight.
		return m, tea.Batch(m.startSyncCmd(false), syncTickCmd())

	case issueRowMsg:
		m.fetchDone()
		if msg.gen != m.syncGen {
			// A sync has completed since this row-fetch was dispatched; that result
			// is fresher, so drop this one rather than stomping it with stale data.
			return m, nil
		}
		switch {
		case msg.err != nil:
			m.setStatus("refresh failed: "+msg.err.Error(), cRed)
		case !msg.ok:
			// The issue has since closed — it no longer belongs in the
			// (open-only) Issues view at all.
			for i := range m.remote.Issues {
				if m.remote.Issues[i].Number == msg.number {
					m.remote.Issues = append(m.remote.Issues[:i], m.remote.Issues[i+1:]...)
					break
				}
			}
			m.setStatus("closed — removed from list", cDim)
		default:
			for i := range m.remote.Issues {
				if m.remote.Issues[i].Number == msg.item.Number {
					m.remote.Issues[i] = msg.item
					break
				}
			}
			m.setStatus("refreshed #"+strconv.Itoa(msg.item.Number), cDim)
		}
		m.clampCursor()
		return m, nil

	case openPRRowMsg:
		m.fetchDone()
		if msg.gen != m.syncGen {
			// A sync has completed since this row-fetch was dispatched; drop the
			// stale result instead of stomping it.
			return m, nil
		}
		if msg.err != nil {
			m.setStatus("refresh failed: "+msg.err.Error(), cRed)
		} else {
			for i := range m.remote.PRs {
				if m.remote.PRs[i].Number == msg.pr.Number {
					m.remote.PRs[i] = msg.pr
					break
				}
			}
			m.setStatus("refreshed #"+strconv.Itoa(msg.pr.Number), cDim)
		}
		m.clampCursor()
		return m, nil

	case deleteStepMsg:
		return m.advanceDelete(msg)

	case nameResolvedMsg:
		// Esc during the lookup already closed the prompt; a late plan must not
		// resurrect it or create a worktree the user cancelled.
		if !m.naming {
			return m, nil
		}
		m.resolving = false
		if msg.err != nil {
			// Keep the prompt and the typed text so the input can be corrected.
			m.nameErr = msg.err.Error()
			return m, nil
		}
		m.naming = false
		m.nameBuf = ""
		m.nameErr = ""
		// A resolved PR takes the PR route, not the create route: its head branch
		// may live only on the contributor's fork, where nothing that knows just
		// the name can reach it. Both routes are the ones the PRs tab already uses.
		if msg.plan.PRNum > 0 {
			if m.herdr {
				return m.startWorktree(worktreeReq{kind: "pr", branch: msg.plan.Branch, prNum: msg.plan.PRNum})
			}
			return m.quitWith(&PendingAction{Kind: "pr-checkout-cd", PRNum: msg.plan.PRNum, Branch: msg.plan.Branch, RepoRoot: m.repoRoot})
		}
		if m.herdr {
			return m.startWorktree(worktreeReq{kind: "create", branch: msg.plan.Branch})
		}
		return m.quitWith(&PendingAction{Kind: "create-cd", Branch: msg.plan.Branch, RepoRoot: m.repoRoot})

	case actionMsg:
		m.creating = false
		m.createLabel = ""
		if msg.err != nil {
			m.stashErrDetail(msg.text, msg.err)
			m.setStatus(msg.text+": "+msg.err.Error(), cRed)
			if m.errDetail != "" {
				m.statusHint = "e for detail"
			}
		} else {
			fg := msg.color
			if fg == nil {
				fg = cDim
			}
			m.setStatus(msg.text, fg)
		}
		// refresh local state after any mutation
		return m, loadLocalCmd(m.ctx, m.repoRoot, m.current, m.def)

	case settingsMsg:
		m.identity, m.identitySeeded = msg.id, msg.seeded
		m.beadCols = msg.beads.Columns
		m.cfgMerged, m.cfgGlobal, m.cfgLocal, m.cfgErr = msg.merged, msg.global, msg.local, msg.loadErr
		if msg.seeded {
			// Said once, on the launch that wrote it. A config file appearing without
			// anyone asking is worth one line; saying it every launch would be noise.
			m.setStatus("seeded [identity] in "+config.GlobalPath(), cCyan)
		}
		return m, nil

	case workDispatchStartedMsg:
		// A hosted dispatch reports "started" immediately rather than an
		// exit status — the command keeps running on its own pty long after
		// this message lands. workTickCmd is what repaints the Work tab so
		// the live-work entry StartHostedPTY just registered shows up
		// without waiting on a keypress.
		if msg.err != nil {
			m.setStatus(msg.name+" failed to start: "+msg.err.Error(), cRed)
			return m, nil
		}
		m.setStatus(msg.name+" started", cGreen)
		return m, workTickCmd()

	case workflowResolvedMsg:
		m.workflowResolved(msg)
		return m, nil

	case workflowDispatchDoneMsg:
		m.workflowDispatchDone(msg)
		return m, nil

	case workTickMsg:
		// Re-arm unconditionally, exactly like syncTickMsg — this is the Work
		// tab's only wake source for a livework.Entry another goroutine mutated
		// (see Init's own comment).
		return m, workTickCmd()

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case copyToastExpiredMsg:
		if msg.gen == m.toastGen {
			m.status = ""
			m.statusFg = nil
		}
		return m, nil
	}
	return m, nil
}

// handleMouse drives click-to-act and drag-to-copy: press anchors a potential
// selection, motion extends it (highlighted live via applyHighlight), and
// release either activates the row under the pointer (a bare click) or copies
// the dragged range (a real drag) — see selection.finish.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Action {
	case tea.MouseActionPress:
		if msg.Button != tea.MouseButtonLeft {
			return m, nil
		}
		m.sel = anchorAt(msg.Y, msg.X)
		return m, nil
	case tea.MouseActionMotion:
		if !m.sel.inProgress() {
			return m, nil
		}
		m.sel.drag(msg.Y, msg.X)
		return m, nil
	case tea.MouseActionRelease:
		if !m.sel.inProgress() {
			return m, nil
		}
		return m.finishMouse(msg)
	}
	return m, nil
}

// finishMouse ends the gesture on mouse-up: a real drag copies, a bare click
// activates whatever is under the pointer. The selection is cleared here,
// before either branch runs, so nothing downstream can leave a highlight
// standing past this Update.
func (m Model) finishMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	sel := m.sel
	m.sel = selection{}
	if sel.finish() {
		return m.copySelection(sel)
	}
	return m.click(msg.Y, msg.X)
}

// copyToastExpiredMsg clears the "copied to clipboard" toast once it has had
// its 2 seconds on screen — but only if gen still matches: a copy toast that a
// newer status line has already replaced must not be blanked out from under it.
type copyToastExpiredMsg struct{ gen int }

// copyToastDuration is how long the "copied to clipboard" toast stays up.
const copyToastDuration = 2 * time.Second

func copyToastCmd(gen int) tea.Cmd {
	return tea.Tick(copyToastDuration, func(time.Time) tea.Msg { return copyToastExpiredMsg{gen: gen} })
}

// copySelection extracts sel's plain text and writes it to the clipboard. An
// empty selection (every covered line trimmed to nothing) is silently
// dropped; a failed copy shows an error toast and never a success one.
func (m Model) copySelection(sel selection) (tea.Model, tea.Cmd) {
	text := m.extractSelection(sel)
	if text == "" {
		return m, nil
	}
	if err := pbcopy(text); err != nil {
		m.setStatus("copy failed: "+err.Error(), cRed)
		return m, nil
	}
	m.toastGen++
	m.setStatus("copied to clipboard", cGreen)
	return m, copyToastCmd(m.toastGen)
}

// click is a bare mouse-up: resolve the hit at (y, x) against the last frame
// and either move the cursor onto that row or, if it was already there,
// activate it — the same handler enter uses.
func (m Model) click(y, x int) (tea.Model, tea.Cmd) {
	_, hits := m.frameRaw()
	if y < 0 || y >= len(hits) {
		return m, nil
	}
	h := hits[y]
	switch h.kind {
	case hitTabBar:
		for _, t := range h.tabs {
			if x >= t.x0 && x < t.x1 {
				m.filter = ""
				m.switchTab(t.tab)
				return m, nil
			}
		}
		return m, nil
	case hitRow:
		pane, row := h.pane, h.row
		// x == splitX is zipColumns' own single-space joiner between the two
		// boxes — neither pane's content — so it falls through to h.row, which
		// is -1 on that column and makes clickRow a no-op.
		if h.split && x > h.splitX {
			pane, row = h.pane2, h.row2
		}
		return m.clickRow(pane, row)
	}
	return m, nil
}

// clickRow is a click resolved to one pane's row: the first click on a row
// moves the cursor (and, on the Local tab, focus) there; a second click on
// the row already under the cursor activates it, exactly as enter would.
func (m Model) clickRow(pane localPane, row int) (tea.Model, tea.Cmd) {
	if row < 0 {
		return m, nil
	}
	if m.active == tabLocal {
		if m.pane == pane && m.paneCursor[pane] == row {
			return m.activate()
		}
		m.pane = pane
		m.paneCursor[pane] = row
		return m, nil
	}
	if m.cursor[m.active] == row {
		return m.activate()
	}
	m.cursor[m.active] = row
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Confirm phase of the delete modal swallows everything but y/n.
	if m.confirming {
		switch msg.String() {
		case "y", "Y":
			return m.startDelete()
		default:
			m.confirming = false
			m.confirmTargets = nil
			m.setStatus("cancelled", cDim)
		}
		return m, nil
	}

	// The workflow-picker overlay (admin-3rgp.27) owns the keyboard while the
	// user is choosing which selection-sourced Workflow to run over the
	// tracker selection — see selectionworkflow.go. It always closes into
	// either the confirm overlay below or nothing, so it sits directly ahead
	// of that check.
	if m.swOpen {
		return m.workflowPickerKey(msg.String())
	}

	// The workflow confirm overlay (admin-3rgp.26) owns the keyboard while a
	// query is resolving or its resolved set is up for confirmation: nothing
	// underneath (the Work tab's own cursor, dispatch, anything else) can
	// react to a keypress meant for "does this resolved set actually run".
	if m.wfResolving || m.wfOpen {
		return m.workflowConfirmKey(msg.String())
	}

	// The bead overlay owns the keyboard while it is up, the same way the
	// error-detail overlay does below: ← → switch mode, ↑↓ scroll, esc closes, and
	// every other key is swallowed rather than moving the list it is drawn over.
	if m.beadOpen {
		m.beadOverlayKey(msg.String())
		return m, nil
	}

	// The error-detail overlay swallows all keys: any key dismisses it. It sits above
	// the delete modal in this chain because it is only ever opened from the idle
	// dashboard, so nothing else can be mid-flight underneath it.
	if m.errOpen {
		m.errOpen = false
		return m, nil
	}

	// While the delete modal runs it swallows all keys; once done, any key dismisses
	// — except `f` over items refused for unresolved conflicts, which re-runs just
	// those without the auto-stash. The retry lives here rather than on the confirm
	// screen because until the batch runs, nothing knows which items will refuse.
	if m.deleting {
		if !m.deleteDone {
			return m, nil
		}
		switch {
		case msg.String() == "f" && m.blockedCount() > 0:
			return m.forceBlocked()
		case msg.String() == "e":
			// The overlay sits above the modal in the key chain, so dismissing it
			// falls back here with the summary still up rather than out to the
			// dashboard — a long refusal can be read without losing the batch.
			if title, detail := m.deleteErrDetail(); detail != "" {
				m.errTitle, m.errDetail, m.errOpen = title, detail, true
				return m, nil
			}
		}
		m.deleting = false
		m.deleteDone = false
		m.deleteForce = false
		m.deleteResults = nil
		m.deleteTargets = nil
		m.clampCursor()
		return m, nil
	}

	// New-worktree name entry swallows most keys; enter resolves the text through
	// the same path as `repo branch` — a branch name, an issue/PR number, or a
	// GitHub issue/PR/project URL all land on a branch — then creates via the same
	// path as enter-on-issue (herdr inline, else quit-and-cd). Esc cancels.
	if m.naming {
		// A lookup in flight freezes the buffer: editing or re-submitting it would
		// leave the returned plan describing text that is no longer on screen. Esc
		// still gets through, so a slow lookup is never a trap.
		if m.resolving && msg.Type != tea.KeyEsc {
			return m, nil
		}
		switch msg.Type {
		case tea.KeyEsc:
			m.naming = false
			m.resolving = false
			m.nameBuf = ""
			m.nameErr = ""
			m.nameCands = nil
			m.nameSel = -1
		case tea.KeyUp:
			if m.nameSel > -1 {
				m.nameSel--
			}
		case tea.KeyDown:
			if m.nameSel < len(m.nameCands)-1 {
				m.nameSel++
			}
		case tea.KeyEnter:
			name := strings.TrimSpace(m.nameBuf)
			if name == "" {
				m.naming = false
				m.nameBuf = ""
				m.nameErr = ""
				m.nameCands = nil
				m.nameSel = -1
				return m, nil
			}
			// A highlighted candidate (only ever set by an exact id match or by
			// ↑/↓) takes that bead's id instead of the literal text; with no
			// highlight the typed text is what gets resolved, exactly as before
			// this prompt could search beads at all.
			if m.nameSel >= 0 && m.nameSel < len(m.nameCands) {
				name = m.nameCands[m.nameSel].ID
			}
			// Resolution reaches the network for a number or URL, so the prompt
			// stays up in a resolving state and the outcome arrives as a
			// nameResolvedMsg — which either creates or reports the failure inline.
			m.resolving = true
			m.nameErr = ""
			return m, resolveNameCmd(m.ctx, m.nwo, name)
		case tea.KeyBackspace:
			if len(m.nameBuf) > 0 {
				m.nameBuf = m.nameBuf[:len(m.nameBuf)-1]
			}
			m.nameErr = ""
			m.refreshNameCands()
		case tea.KeyRunes:
			m.nameBuf += string(msg.Runes)
			m.nameErr = ""
			m.refreshNameCands()
		}
		return m, nil
	}

	// Filter input mode swallows most keys.
	if m.filtering {
		switch msg.Type {
		case tea.KeyEsc:
			m.filtering = false
			m.filter = ""
			m.clampCursor()
		case tea.KeyEnter:
			m.filtering = false
			m.clampCursor()
		case tea.KeyBackspace:
			if len(m.filter) > 0 {
				m.filter = m.filter[:len(m.filter)-1]
				m.resetCursors()
			}
		case tea.KeyRunes:
			m.filter += string(msg.Runes)
			m.resetCursors()
		case tea.KeySpace:
			m.filter += " "
			m.resetCursors()
		}
		return m, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.closeWatcher()
		return m, tea.Quit
	case "right":
		// On the Work tab, right on a collapsed command-with-targets row opens
		// its submenu instead of switching tab — l keeps switching tab even
		// there, since only the arrow gains the submenu meaning.
		if m.active == tabWork {
			if r, ok := m.currentRow(); ok && r.kind == rowCommand && r.work != nil &&
				len(r.work.targets) > 0 && !r.work.expanded {
				return m.expandCommand(r.work.cmdName)
			}
		}
		m.filter = ""
		m.stepTab(1)
	case "l":
		// Arrows step along the drawn tab order, whatever this repo draws.
		m.filter = ""
		m.stepTab(1)
	case "left":
		if m.active == tabWork {
			if r, ok := m.currentRow(); ok {
				switch {
				case r.kind == rowCommand && r.work != nil && r.work.expanded:
					return m.collapseCommand(r.work.cmdName)
				case r.kind == rowCommandTarget && r.work != nil:
					return m.collapseCommand(r.work.cmdName)
				}
			}
		}
		m.filter = ""
		m.stepTab(-1)
	case "h":
		m.filter = ""
		m.stepTab(-1)
	case "esc":
		if m.active == tabWork {
			if r, ok := m.currentRow(); ok {
				switch {
				case r.kind == rowCommand && r.work != nil && r.work.expanded:
					return m.collapseCommand(r.work.cmdName)
				case r.kind == rowCommandTarget && r.work != nil:
					return m.collapseCommand(r.work.cmdName)
				}
			}
		}
	case "tab":
		// Tab cycles the focused pane within the Repo tab; it never switches
		// the top-level tab. The Config tab is a plain read-only readout
		// (admin-3rgp.21) with nothing to cycle.
		if m.active == tabLocal {
			m.pane = m.nextVisiblePane(m.pane, 1)
		}
	case "shift+tab":
		if m.active == tabLocal {
			m.pane = m.nextVisiblePane(m.pane, -1)
		}
	case "1", "2", "3", "4":
		// The digits number the tabs in the order they are drawn, so `3` is the
		// third tab on screen — Beads in a beads repo, Issues in a GitHub one.
		n, _ := strconv.Atoi(msg.String())
		m.switchTabAt(n - 1)
	case "j", "down":
		m.moveCursor(1)
	case "up":
		m.moveCursor(-1)
	case "k":
		// The Work tab reuses k for "kill" (a running row's own action) rather
		// than "move up" — the prototype's own footer ("↑↓ move · k kill"), and
		// the reason arrow-only movement is what the Work tab's keymap advertises.
		if m.active == tabWork {
			return m.killCurrentWork()
		}
		m.moveCursor(-1)
	case "a":
		if m.active == tabWork {
			return m.attachCurrentWork()
		}
	case "/":
		m.filtering = true
		m.filter = ""
	case "r":
		// The Work tab has no `r` at all. It used to mean retry, which was
		// removed because restarting a live thing under the same identity is
		// not something every producer can honour. It must not silently become
		// "refresh" by falling through either: the Work tab renders the live
		// registry and the resolved manifest, neither of which a refresh
		// fetches, so `r` there would spend a network sync to redraw the same
		// rows. `R` still syncs everything from any tab.
		if m.active == tabWork {
			return m, nil
		}
		// The Beads tab has no per-row refresh: re-reading the whole workspace is
		// one local query, so there is nothing a single bead would save.
		if m.active == tabBeads {
			return m, loadBeadsCmd(m.ctx, m.repoRoot)
		}
		// On an Issues/PRs row, refresh just that row — one targeted GraphQL call
		// instead of syncing the whole repo. Anywhere else (the Repo tab, or no row
		// under the cursor), fall back to refreshing everything.
		if cmd, handled := m.rowRefreshCmd(); handled {
			return m, cmd
		}
		return m, m.refreshAllCmd()
	case "R":
		// The override on the poll: sync everything now, from any tab, whatever the
		// clock says.
		return m, m.refreshAllCmd()
	case "o":
		if r, ok := m.currentRow(); ok && r.url != "" {
			return m, openURLCmd(r.url)
		}
	case "i":
		return m.openIssue()
	case "e":
		// Show the last failure's full git output. Available from any tab: the
		// action that failed may have been run from a different one.
		if m.errDetail != "" {
			m.errOpen = true
		}
	case "d":
		// The bead's own text and its dependency neighbourhood are the two things
		// the row cannot carry: 883 characters of description and a graph do not
		// fit in a table cell.
		if m.active == tabBeads {
			if r, ok := m.currentRow(); ok && r.kind == rowBead {
				m.beadOpen, m.beadTab, m.beadScroll = true, beadTabDetail, 0
			}
		}
	case "p":
		if m.active == tabLocal && m.pane == paneBranches {
			m.setStatus("pulling…", cDim)
			return m, pullCmd(m.ctx, m.repoRoot)
		}
	case "s":
		if m.active == tabBeads || m.active == tabIssues {
			m.trackSort = m.trackSort.next()
			m.cursor[m.active] = 0
			m.setStatus("sorted: "+m.trackSort.label(), cDim)
		}
	case "g":
		if m.active == tabBeads {
			m.beadTree = !m.beadTree
			m.cursor[tabBeads] = 0
			if m.beadTree {
				m.setStatus("dependency tree — blockers sit above what they block", cDim)
			} else {
				m.setStatus("flat list, grouped by what's ready", cDim)
			}
			break
		}
		if m.active == tabLocal && m.pane == paneBranches {
			m.minesOnly = !m.minesOnly
			m.paneCursor[paneBranches] = 0
			if m.minesOnly {
				if !m.identity.Known() {
					// Narrowing to "yours" with nothing defining yours would empty the pane and
					// look like a repo with no work in it. Say which it is.
					m.minesOnly = false
					m.setStatus("no [identity] configured — nothing is known to be yours", cYellow)
					break
				}
				m.setStatus("showing your branches (Clean Up still shows everything)", cDim)
			} else {
				m.setStatus("showing every branch", cDim)
			}
			m.clampCursor()
		}
	case "w":
		if m.active == tabLocal && m.pane == paneBranches {
			m.naming = true
			m.nameBuf = ""
			m.nameCands = nil
			m.nameSel = -1
		}
		if m.active == tabBeads || m.active == tabIssues {
			return m.startWorkflowPicker()
		}
	case "enter":
		return m.activate()
	case "alt+enter", "W":
		// W is a plain-letter fallback for terminals that swallow alt+enter
		// (same class that collapses shift+enter to a bare CR); mnemonic pair
		// with w = new named worktree.
		if m.active == tabLocal && m.pane == paneBranches {
			return m.worktreeCurrentBranch()
		}
	case " ":
		if m.active == tabLocal && m.pane == paneBranches {
			// Select mode is the selection, so a refused toggle changes nothing: on
			// the protected main row or a branch checked out elsewhere, `space` leaves
			// the pane exactly as it found it.
			m.toggleBranchSelection()
		}
		if m.active == tabBeads || m.active == tabIssues {
			m.toggleTrackerSelection()
		}
	case "x":
		// With a selection, x takes the whole batch — a mixed pick (some proven
		// ready, some merely eyeballed) that neither a single x nor ⇧X (every
		// proven-ready row, no exceptions) can express on its own. With nothing
		// selected it falls back to the row under the cursor, unchanged.
		if m.active == tabLocal && m.pane == paneBranches {
			if len(m.selected) > 0 {
				return m.deleteSelectedBranches()
			}
			return m.deleteBranchUnderCursor()
		}
	case "X":
		// Clean-all works from any Repo-tab pane: the collapsed Cleanup box advertises
		// it from all of them, and it needs no per-row cursor.
		if m.active == tabLocal {
			return m.cleanAll()
		}
	}
	return m, nil
}

// rowRefreshCmd returns the row-scoped refresh for the row under the cursor —
// dispatched by row kind, not active tab, so it naturally only fires on an
// Issues/PRs row and leaves Local/Changes (whose rows are rowBranch/rowCommit/
// rowStash) to fall back to a full refresh. handled is false when there's no
// row under the cursor, or its kind doesn't support a row-scoped refresh.
func (m *Model) rowRefreshCmd() (tea.Cmd, bool) {
	r, ok := m.currentRow()
	if !ok {
		return nil, false
	}
	var number int
	var cmd tea.Cmd
	switch r.kind {
	case rowIssue:
		number, cmd = r.issueNum, fetchIssueRowCmd(m.ctx, m.nwo, r.issueNum, m.syncGen)
	case rowPR:
		number, cmd = r.prNum, fetchOpenPRRowCmd(m.ctx, m.nwo, r.prNum, m.syncGen)
	default:
		return nil, false
	}
	m.inflight++
	m.setStatus("refreshing #"+strconv.Itoa(number)+"…", cDim)
	return cmd, true
}

// activate handles enter on the selected row, per view.
func (m Model) activate() (tea.Model, tea.Cmd) {
	r, ok := m.currentRow()
	if !ok {
		return m, nil
	}
	switch r.kind {
	case rowBranch:
		if r.wtPath != "" {
			if m.herdr {
				return m.startWorktree(worktreeReq{kind: "open", branch: r.branch, path: r.wtPath})
			}
			return m.quitWith(&PendingAction{Kind: "cd", Path: r.wtPath})
		}
		return m, checkoutInPlaceCmd(m.ctx, r.branch)
	case rowCommit:
		return m, copyClipboardCmd(r.label, "copied "+r.label+" to clipboard")
	case rowStash:
		m.setStatus(r.label+"  ·  local only", cMag)
		return m, nil
	case rowPR:
		if r.wtPath != "" {
			if m.herdr {
				return m.startWorktree(worktreeReq{kind: "open", branch: r.branch, path: r.wtPath})
			}
			return m.quitWith(&PendingAction{Kind: "cd", Path: r.wtPath})
		}
		if m.herdr {
			return m.startWorktree(worktreeReq{kind: "pr", branch: r.branch, prNum: r.prNum})
		}
		return m.quitWith(&PendingAction{Kind: "pr-checkout-cd", PRNum: r.prNum, Branch: r.branch, RepoRoot: m.repoRoot})
	case rowIssue:
		branch := issueBranch(r.issueNum, r.issueTitle)
		if m.herdr {
			return m.startWorktree(worktreeReq{kind: "create", branch: branch})
		}
		return m.quitWith(&PendingAction{Kind: "create-cd", Branch: branch, RepoRoot: m.repoRoot})
	case rowBead:
		// Starting work on a bead builds the worktree and nothing else. Moving the
		// bead to in_progress belongs to whoever does the work, not to the dashboard
		// that opened the window for them.
		branch := beadBranch(r.beadID, r.beadTitle)
		if m.herdr {
			return m.startWorktree(worktreeReq{kind: "create", branch: branch})
		}
		return m.quitWith(&PendingAction{Kind: "create-cd", Branch: branch, RepoRoot: m.repoRoot})
	case rowCommand:
		return m.activateCommand(r.work)
	case rowCommandTarget:
		return m.activateCommandTarget(r.work)
	case rowWorker:
		return m.attachEntry(r.work.worker)
	}
	return m, nil
}

// worktreeCurrentBranch attaches a worktree to the branch under the cursor —
// alt+enter on the Local branch pane. Plain enter checks the branch out in
// place; this instead runs the same worktree flow PRs and issues use
// (create.Worktree sees the branch already exists locally and attaches to it,
// then populate + cd). A branch that already has a worktree just opens it,
// matching enter's behavior for that case.
func (m Model) worktreeCurrentBranch() (tea.Model, tea.Cmd) {
	r, ok := m.currentRow()
	if !ok || r.kind != rowBranch {
		return m, nil
	}
	if r.wtPath != "" {
		if m.herdr {
			return m.startWorktree(worktreeReq{kind: "open", branch: r.branch, path: r.wtPath})
		}
		return m.quitWith(&PendingAction{Kind: "cd", Path: r.wtPath})
	}
	if m.herdr {
		return m.startWorktree(worktreeReq{kind: "create", branch: r.branch})
	}
	return m.quitWith(&PendingAction{Kind: "create-cd", Branch: r.branch, RepoRoot: m.repoRoot})
}

// startWorktree kicks off an inline (herdr-mode) worktree action: it marks the
// dashboard busy and runs worktreeCmd, which ends in an actionMsg that clears
// the busy state, sets the status line, and refreshes the local view. A second
// enter while one is in flight is ignored so a slow create can't be double-fired.
func (m Model) startWorktree(req worktreeReq) (tea.Model, tea.Cmd) {
	if m.creating {
		return m, nil
	}
	m.creating = true
	if req.kind == "open" {
		m.createLabel = "opening " + req.branch
	} else {
		m.createLabel = "creating " + req.branch
	}
	return m, worktreeCmd(m.ctx, m.repoRoot, req)
}

func (m Model) openIssue() (tea.Model, tea.Cmd) {
	r, ok := m.currentRow()
	if !ok {
		return m, nil
	}
	var n int
	switch r.kind {
	case rowIssue:
		n = r.issueNum
	case rowPR:
		n = r.issueNum // linked issue if known
	}
	if n > 0 {
		return m, openURLCmd(issueURL(m.nwo, n))
	}
	return m, nil
}

var _ = git.ErrNotInRepo // keep git import used across files
