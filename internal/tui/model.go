// Package tui is the Bubble Tea dashboard: a tabbed shell over three views
// (Local, PRs, Issues) sharing one row/cursor/filter mechanic. Cleanup lives as
// a focusable pane in the Repo (Local) tab's right column.
package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fsnotify/fsnotify"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/keymap"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/shim"
	"github.com/McBrideMusings/admin-project-tool/internal/viewstate"
)

type tab int

// tabWork (drawn as "Commands") and tabWorkers are drawn first, and only when
// the project has an admin.toml (Model.manifestOK) — admin-3rgp.20's "a repo
// with no manifest has no Work tab at all, absent not disabled." Commands lists
// the manifest's commands; Workers lists every live agent worker, whichever
// surface started it (admin-spjp.9). Every other tab is unconditional or
// derived from remote/beads state exactly as before this ticket.
const (
	tabWork tab = iota
	tabWorkers
	tabLocal
	tabPRs
	tabIssues
	tabBeads
	tabConfig
	numTabs
)

var tabNames = [numTabs]string{"Commands", "Workers", "Local", "PRs", "Issues", "Beads", "Config"}

// focusZone is the one thing on screen that owns the keyboard's left/right and
// up/down meaning: either the tab bar (nothing selected, arrows step tabs) or
// the body (today's row/pane cursor). focusBody MUST stay the zero value —
// every test that builds a `Model{...}` literal directly (rather than through
// New) relies on the zero value behaving exactly as it did before this field
// existed.
type focusZone int

const (
	focusBody focusZone = iota
	focusTabs
)

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
	rowConfigValue   // Config tab: a value-only row (tracker, ceiling) — selectable, nothing to open
	rowConfigPath    // Config tab: a row naming an absolute path — a key file, the worktree root, a populate pattern
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
	pr         *gh.OpenPR // set by prRows so `admin show prs` can report bucket/checks/review by name
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
	// group marks a rowCommand row whose command is a multi-target parent
	// (more than one declared target): it is a heading over its targets and
	// runs nothing itself, so it carries no cursor marker and selectable()
	// below skips it in both directions (admin-3rgp.72.11) — the same seam
	// moveCursor already uses for headers and rules, so no separate skip
	// logic is needed at the cursor-advance call sites.
	group bool
	// cfg carries a Config-tab row's payload — set only for rowConfigValue/
	// rowConfigPath, following the same one-field-per-tab precedent as work.
	cfg *configRowData
}

func (r rowItem) selectable() bool {
	switch r.kind {
	case rowHeader, rowRule, rowCenter, rowText:
		return false
	}
	if r.group {
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

// Model is the root dashboard model: the shell every mounted tab shares (see
// shell), the data sources those tabs declare (see sources.go), and the
// per-tab state that has not yet moved into a tab model of its own (ADR-0026).
type Model struct {
	shell

	ctx context.Context
	nwo string
	// remoteHost and remoteAddr describe a non-GitHub origin (see WithRemote);
	// both are empty when origin is GitHub, absent, or a local path.
	remoteHost string
	remoteAddr string
	current    string
	def        string

	cursor [numTabs]int

	// localApplied is the seq of the newest local read on screen (localDataMsg.seq).
	localApplied uint64
	inflight     int // in-flight background work (the sync, row refreshes); header spins while > 0

	// feeds is the state of every data source a tab model reads through a
	// handle (feeds.go); the shell starts each one for the mounted tabs that
	// declare it (sources.go). Whether the Beads tab is drawn at all comes
	// from the declared tracker list alone (trackers/hasTracker), never from
	// the beads feed: the feed decides what is inside the tab.
	//
	// feeds is embedded so Model reads the sync's remote and haveData and the
	// local git read as its own fields; a tab model holds the same pointers as
	// typed handles instead.
	feeds
	// beadsTab is the Beads tab's own model (beadstab.go). It is built only
	// when the tab is mounted (mountTabs) and reached through tabModel.
	beadsTab beadsTab
	// configTab is the Config tab's own model (configmodel.go), mounted the
	// same way.
	configTab configTab
	// issuesTab is the Issues tab's own model (issuestab.go), built and reached
	// the same way.
	issuesTab issuesTab
	// workersTab is the Workers tab's own model (workerstab.go), built and
	// reached the same way.
	workersTab workersTab
	// prsTab is the PRs tab's own model (prstab.go), built and reached the
	// same way.
	prsTab prsTab
	// commandsTab is the Commands tab's own model (commandstab.go), reached
	// through tabModel. It is nil until mountTabs builds it for a mounted
	// Commands tab, so a Model that never mounted it has no Commands tab at
	// all rather than one with no handles.
	commandsTab *commandsTab
	// repoTab is the Repo tab's own model (repotab.go), built and reached the
	// same way.
	repoTab repoTab

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

	fsCh <-chan struct{} // file-watcher signal; nil if watching is off
	// fsReloading is true from a watcher-triggered local reload until its
	// localDataMsg lands; fsStale records a change that arrived meanwhile, so
	// reloads run one at a time with one trailing catch-up instead of piling up.
	fsReloading, fsStale bool
	fsWatcher            *fsnotify.Watcher // owned so it can be closed on quit; nil if off

	// headless is set by the headless renderer (loadedPump) for a one-shot
	// frame with no terminal and no later repaint. It turns off everything
	// that only exists to keep a long-lived dashboard current — the file
	// watcher and the sync/beads/work polls — because each is a command that
	// never delivers within a frame: left on, the pump never goes quiescent
	// and every frame waits out its full Settle backstop, and the watcher's
	// per-directory descriptors are never closed.
	headless bool
	// blurred is true between a tea.BlurMsg and the next tea.FocusMsg. The zero
	// value is "focused", so a terminal that never reports focus keeps polling.
	blurred bool

	Pending *PendingAction

	// syncFailedAt is when the last sync that came back with a failed section
	// finished — printed beside the header's "sync failed" so a failure from
	// twenty minutes ago is not mistaken for one that just happened. The failed
	// sections themselves live on m.remote (data.Remote.FetchErrors).
	syncFailedAt time.Time

	// creating is set while an inline (herdr-mode) worktree action is in flight
	// and drives the header status.
	creating    bool
	createLabel string

	closedIssues bool // Issues view: show closed too (toggle) — reserved

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
	// started records which data sources have been started (see sources.go),
	// so each runs once for the life of the process however many times the
	// manifest flaps between invalid and valid.
	started [numSources]bool
	// manifestStamp is admin.toml's mtime+size as of the last loadManifest
	// call — the gate fsChangedMsg checks before reloading, so a save
	// anywhere else in the repo (a source file, a build artifact) doesn't
	// re-parse the manifest or overwrite the status line.
	manifestStamp manifestStamp
	// rowKeys is the resolved [keys.<row-kind>] lookup (ADR-0018,
	// admin-l6kb.2), built once at New from validate.RowKeys over
	// m.manifest.Keys — see rowkeys.go. Nil (not just empty) when there is
	// no manifest at all, which is what makes rowKeyDispatch a clean miss
	// on every project written before this feature existed.
	rowKeys map[keymap.RowKind]map[string]string

	// Config tab (admin-3rgp.21): the resolved config.Config — repoRoot's
	// admin.toml [worktrees]/[[populate]]/[beads]/[workflow]
	// tables, merged with its declared archetypes. The Config tab keeps its own
	// copy (configmodel.go); this one is for the terminal launcher keys.
	// Loaded once at launch by loadConfigCmd; zero until settingsMsg lands.
	cfgMerged config.Config

	// Worktree-target picker overlay (admin-76tv, worktreepicker.go): `T`
	// lists the repo's worktrees (git.Worktrees(m.repoRoot)) so the user
	// can point the dashboard at one of them; enter switches wtTarget/
	// wtTargetBranch and reloads the manifest from there. wtTarget is ""
	// until a pick is made, which is what headerLine treats as "nothing to
	// show" — the dashboard's original repoRoot-rooted behavior.
	wtPickerOpen    bool
	wtPickerOptions []git.Worktree
	wtPickerCursor  int
	wtTarget        string
	wtTargetBranch  string
}

// Option adjusts a Model at construction. Options are applied before the
// manifest is loaded, so one may change where that load reads from.
type Option func(*Model)

// WithManifestRoot points the manifest load at dir instead of repoRoot. The
// caller resolves it, because only the process that owns the real working
// directory can: see the manifestRoot field. An empty dir is ignored.
func WithManifestRoot(dir string) Option {
	return func(m *Model) {
		if dir != "" {
			m.manifestRoot = dir
		}
	}
}

// WithCheckout runs the dashboard's git commands in dir instead of repoRoot —
// the top level of the checkout the process was opened in. An empty dir is
// ignored.
func WithCheckout(dir string) Option {
	return func(m *Model) {
		if dir != "" {
			m.checkout = dir
		}
	}
}

// WithRemote names the machine a non-GitHub origin points at, so the header can
// show that a remote exists. addr is what the host resolves to, when that
// differs from the name. An empty host shows nothing.
func WithRemote(host, addr string) Option {
	return func(m *Model) {
		m.remoteHost, m.remoteAddr = host, addr
	}
}

// aloneVerbs maps the verb that opens one tab alone (`admin prs`) to the tab it
// mounts. The tab bar calls the Repo tab "Local" internally and the Commands
// tab "Work"; a verb is the lowercase title a person reads.
var aloneVerbs = []struct {
	verb string
	tab  tab
}{
	{"cmd", tabWork},
	{"workers", tabWorkers},
	{"repo", tabLocal},
	{"prs", tabPRs},
	{"issues", tabIssues},
	{"beads", tabBeads},
	{"config", tabConfig},
}

// WithMountedAlone mounts only the tab the verb names: no tab bar, no
// tab-switch keys or footer hint, focus on the body, and no data source that
// only the other tabs declare. It is applied inside New, before the tabs claim
// their sources, because Init starts exactly the sources New claimed. A verb
// that names no tab is ignored.
func WithMountedAlone(verb string) Option {
	return func(m *Model) {
		for _, v := range aloneVerbs {
			if v.verb == verb {
				m.mounted = []tab{v.tab}
				m.alone = true
				return
			}
		}
	}
}

// AloneVerbs lists the verbs WithMountedAlone accepts, in tab-bar order.
func AloneVerbs() []string {
	out := make([]string, len(aloneVerbs))
	for i, v := range aloneVerbs {
		out[i] = v.verb
	}
	return out
}

// MountedDrawn reports whether the project draws the tab this dashboard was
// opened on alone. It is false when that tab needs a manifest, a GitHub origin
// or a tracker the project lacks. A dashboard that is not alone is always
// drawn.
func (m Model) MountedDrawn() bool {
	return !m.alone || len(m.visibleTabs()) > 0
}

// FindManifestRoot walks up from cwd for the nearest admin.toml, exactly as
// cmd.resolveProjectManifest does, and returns "" when there is none — the
// caller then leaves the dashboard on repoRoot.
func FindManifestRoot(cwd string) string {
	if root, found := manifest.FindRoot(cwd); found {
		return root
	}
	return ""
}

// New builds the dashboard model. The cheap git context (root, nwo, branch) is
// gathered by the caller and passed in so the first frame can render immediately.
func New(ctx context.Context, repoRoot, nwo, current, def string, opts ...Option) Model {
	m := Model{
		ctx:     ctx,
		nwo:     nwo,
		current: current,
		def:     def,
		shell:   shell{active: tabLocal, repoRoot: repoRoot, herdr: shim.Available()},
	}
	for _, opt := range opts {
		opt(&m)
	}
	// view.toml cascade (admin-y5lc.1): project dir, then the main checkout
	// when this is a linked worktree, then ~/.admin/view.toml, then these
	// fields' own zero values — see viewstate.Load. Read before loadManifest
	// only because manifestDir() (which this uses) doesn't need the manifest
	// itself loaded to resolve.
	vs := viewstate.Load(ctx, m.manifestDir())
	m.mountTabs(vs)
	if sort, ok := trackSortFromView(vs.TrackSort); ok {
		m.trackSort = sort
	}
	m.loadManifest()
	if m.manifestOK {
		// AC: "the dashboard opens on the Work tab when the project has a
		// manifest."
		m.active = tabWork
	}
	if m.tabModel(tabWork) != nil && m.commandsTab.collapsed {
		// Launching already collapsed (view.toml's work.collapsed = true) has
		// no toggle keypress to open the command under the cursor, so the tab
		// does it here from the row the cursor starts on. loadManifest has
		// already run, so the tab's rows reflect the real command list;
		// clampCursor moves the zero-value cursor off row 0's section header
		// onto the first real row. m.focus is still its zero value
		// (focusBody) here — m.focus = focusTabs runs later below — so the
		// tab reads the same row a keypress would at this point.
		m.clampCursor()
		m.commandsTab.seedOpen(&m.shell)
	}
	if m.alone {
		m.active = m.mounted[0]
	}
	// Init starts exactly the sources claimed here, so a later manifest
	// reload (fsChangedMsg) never starts one a second time.
	m.claimSources()
	// admin-70hb: a new session opens with focus on the tab bar, not on a
	// body row — set after active settles so it doesn't interact with which
	// tab that is.
	if !m.alone {
		m.focus = focusTabs
	}
	return m
}

// Init kicks off the local git load (instant), the merged config, the file
// watcher, and every data source New claimed for the mounted tabs
// (sources.go). The first sync itself waits for local git to land (see the
// localDataMsg case), so the first frame paints off git and cache with
// nothing blocking on the network.
func (m Model) Init() tea.Cmd {
	// No per-model state — see installTerminalOpener's own comment on why
	// calling it again on a second Init in the same process is harmless.
	installTerminalOpener()
	return tea.Batch(
		loadLocalCmd(m.ctx, m.repoRoot, m.checkoutDir(), m.current, m.def),
		m.startWatchCmd(),
		loadConfigCmd(m.ctx, m.manifestDir()),
		m.sourceCmds(m.startedSources()),
	)
}

// syncTickMsg fires on a repeating timer to run the whole-repo sync. Everything
// this dashboard shows from GitHub — a PR review landing, an issue closing, a
// branch merged and deleted on the remote — happens without touching a single
// local file, so the file watcher never hears about any of it. This timer is the
// only wake source that keeps an idle dashboard honest.
type syncTickMsg struct{}

// syncTickCmd schedules the next poll one data.PollInterval out. It re-arms
// itself on every fire (see the syncTickMsg case in Update), so the poll runs for
// the life of the process — or not at all for a headless frame.
func (m Model) syncTickCmd() tea.Cmd {
	if m.headless {
		return nil
	}
	return tea.Tick(data.PollInterval, func(time.Time) tea.Msg { return syncTickMsg{} })
}

// trackers returns the manifest's declared tracker list ("beads", "github",
// or both, in declared order), or nil when the manifest declares none
// (including when there is no manifest at all — m.manifest is nil exactly
// when m.manifestOK is false, since New() only ever sets the two together).
// Nothing here falls back to a `bd where` probe or any other guess —
// admin-3rgp.22 replaces exactly that inference with the declared key
// admin-3rgp.21 added to admin.toml, and admin-j00d lets that key name more
// than one tracker at once.
func (m Model) trackers() []string {
	return manifestTrackers(m.manifest)
}

// manifestTrackers is mf's declared tracker list, nil when mf is nil or
// declares none.
func manifestTrackers(mf *manifest.Manifest) []string {
	if mf == nil || !mf.HasTracker {
		return nil
	}
	return mf.Trackers
}

// hasTracker reports whether name is one of the manifest's declared
// trackers.
func (m Model) hasTracker(name string) bool {
	return slices.Contains(m.trackers(), name)
}

// wantsPRs reports whether the manifest declares uses_pull_requests = true.
// Undeclared defaults to false — the same default the Config tab shows
// (configtab.go's manifestConfigRows). The PRs tab's presence follows this
// declaration alone: zero open PRs is a legitimate state prRows renders as an
// empty section, not a reason for the tab itself to disappear.
func (m Model) wantsPRs() bool {
	return m.manifest != nil && m.manifest.HasUsesPullRequests && m.manifest.UsesPullRequests
}

// visibleTabs is the tab bar, in the order it is drawn. Only the Repo tab is
// unconditional: Work needs a manifest, PRs needs uses_pull_requests declared
// true, and the tracker tabs are Issues and/or Beads depending on the
// declared list — both draw, in declared order, when both are declared
// (admin-j00d). Both PRs and Issues still need a GitHub remote to mean
// anything; Beads does not, since beads is a local database. None of these
// infers itself from live repo state — not from how many PRs happen to be
// open, and not from a `bd where` probe of the repo — that inference is
// exactly what admin-3rgp.22 replaced with the declared keys admin-3rgp.21
// added. The digit keys index this list, so `3` is whatever the third drawn
// tab happens to be. A tab the shell has not mounted is left out.
func (m Model) visibleTabs() []tab {
	var drawn []tab
	if m.manifestOK {
		drawn = append(drawn, tabWork, tabWorkers)
	}
	drawn = append(drawn, tabLocal)
	if m.nwo != "" && m.wantsPRs() {
		drawn = append(drawn, tabPRs)
	}
	for _, t := range m.trackers() {
		switch t {
		case "github":
			if m.nwo != "" {
				drawn = append(drawn, tabIssues)
			}
		case "beads":
			drawn = append(drawn, tabBeads)
		}
	}
	drawn = append(drawn, tabConfig)
	tabs := drawn[:0]
	for _, t := range drawn {
		if m.isMounted(t) {
			tabs = append(tabs, t)
		}
	}
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

// mountTabs builds the feeds and a model for every mounted tab that has one,
// each constructor declaring the sources it reads by asking for their handles
// (feeds.go), and each starting from the view state vs remembered. New calls
// it once the options have settled what is mounted.
func (m *Model) mountTabs(vs viewstate.State) {
	m.feeds = newFeeds(m.ctx, m.repoRoot)
	if m.isMounted(tabLocal) {
		m.repoTab = newRepoTab(m.feeds, m.ctx, m.nwo, m.def, vs.MinesOnly)
	}
	if m.isMounted(tabBeads) {
		tree, _ := beadTreeModeFromView(vs.BeadTree)
		m.beadsTab = newBeadsTab(m.feeds, tree)
	}
	if m.isMounted(tabConfig) {
		m.configTab = newConfigTab(m.feeds)
	}
	if m.isMounted(tabIssues) {
		m.issuesTab = newIssuesTab(m.feeds, m.nwo)
	}
	if m.isMounted(tabWorkers) {
		m.workersTab = newWorkersTab(m.feeds)
	}
	if m.isMounted(tabPRs) {
		m.prsTab = newPRsTab(m.feeds, m.nwo)
	}
	if m.isMounted(tabWork) {
		m.commandsTab = newCommandsTab(m.feeds, vs.WorkCollapsed)
	}
}

// updateTabs hands msg to every mounted tab model.
func (m *Model) updateTabs(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for t := tab(0); t < numTabs; t++ {
		if tm := m.tabModel(t); tm != nil {
			cmds = append(cmds, tm.update(&m.shell, msg))
		}
	}
	return tea.Batch(cmds...)
}

// tabModel is the model of tab t when t is mounted and has moved into a model
// of its own (ADR-0026), or nil while t's state still lives on Model.
func (m *Model) tabModel(t tab) tabModel {
	if !m.isMounted(t) {
		return nil
	}
	switch t {
	case tabBeads:
		return &m.beadsTab
	case tabConfig:
		return &m.configTab
	case tabIssues:
		return &m.issuesTab
	case tabWorkers:
		return &m.workersTab
	case tabPRs:
		return &m.prsTab
	case tabWork:
		if m.commandsTab == nil {
			return nil
		}
		return m.commandsTab
	case tabLocal:
		return &m.repoTab
	}
	return nil
}

// cur is tab t's list cursor: the tab model's own, or Model's slot for a tab
// that has not moved yet.
func (m *Model) cur(t tab) *int {
	if tm := m.tabModel(t); tm != nil {
		return tm.cursorRef()
	}
	return &m.cursor[t]
}

func (m Model) rows() []rowItem {
	if tm := m.tabModel(m.active); tm != nil {
		return tm.rows(&m.shell)
	}
	// The Repo (local) tab uses the lazygit panes, not the flat row list.
	return nil
}

// visibleRows applies the active filter (substring on label), always keeping
// section headers so the structure stays legible.
func (m Model) visibleRows() []rowItem {
	return m.filterRows(m.rows())
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
		m.repo().clampPanes()
		return
	}
	rows := m.visibleRows()
	n := len(rows)
	if n == 0 {
		*m.cur(m.active) = 0
		return
	}
	if *m.cur(m.active) >= n {
		*m.cur(m.active) = n - 1
	}
	if *m.cur(m.active) < 0 {
		*m.cur(m.active) = 0
	}
	// Row 0 of a list tab is a section header, which is not selectable, so a
	// cursor left sitting there makes every row-scoped key a silent no-op. Snap
	// forward through moveCursor rather than repeating its skip logic here.
	if !rows[*m.cur(m.active)].selectable() {
		m.moveCursor(1)
	}
}

func (m *Model) moveCursor(delta int) {
	if m.active == tabLocal {
		m.repo().movePane(delta)
		return
	}
	if mv, ok := m.tabModel(m.active).(cursorMover); ok {
		mv.moveCursor(&m.shell, delta)
		return
	}
	stepCursor(m.visibleRows(), m.cur(m.active), delta)
}

// stepCursor moves *cur delta rows through rows, skipping every row the
// cursor cannot sit on and wrapping past either end. With no selectable row
// it leaves *cur where it is.
func stepCursor(rows []rowItem, cur *int, delta int) {
	n := len(rows)
	if n == 0 {
		return
	}
	i := *cur
	for steps := 0; steps < n; steps++ {
		i += delta
		if i < 0 {
			i = n - 1
		}
		if i >= n {
			i = 0
		}
		if rows[i].selectable() {
			*cur = i
			return
		}
	}
}

// settleSyncFailure updates what the dashboard says about the sync once a
// result lands. A failure puts its first error on the status line and every
// error, verbatim, in the `e` overlay beside the sync log's path — the same
// one-slot rule action failures follow, newest failure wins. Two guards keep a
// failure that simply persists from clobbering anything: an unchanged set of
// errors is not announced again on every poll, and an open overlay is never
// swapped out from under the reader. A clean sync withdraws whatever the sync
// itself put up, leaving an action's error alone.
func (m *Model) settleSyncFailure(prev, failed []data.SectionError, at time.Time) {
	if len(failed) == 0 {
		// Recognised by its own text, so whatever an action wrote since stays put.
		if strings.HasPrefix(m.errTitle, syncErrTitle) {
			m.errTitle, m.errDetail, m.errOpen = "", "", false
		}
		if strings.HasPrefix(m.status, syncErrStatus) {
			m.status, m.statusFg, m.statusHint = "", nil, ""
		}
		return
	}
	m.syncFailedAt = at
	if slices.Equal(prev, failed) || m.errOpen {
		return
	}
	var b strings.Builder
	for _, f := range failed {
		fmt.Fprintf(&b, "%s: %s\n\n", f.Section, f.Err)
	}
	b.WriteString("log: " + data.SyncLogPath())
	m.errTitle = syncErrTitle + at.Format("3:04:05pm")
	m.errDetail = b.String()
	m.setStatus(syncErrStatus+failed[0].Section+": "+oneLine(failed[0].Err), cRed)
	m.statusHint = "e for detail"
}

// syncErrTitle / syncErrStatus open the `e` title and the status line a sync
// failure writes — how settleSyncFailure tells its own text from an action's.
const (
	syncErrTitle  = "sync failed at "
	syncErrStatus = "sync failed: "
)

// oneLine folds a multi-line error onto one line for a row that has only one.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// resetCursors sends every cursor back to the top — used when the filter changes.
// The clamp then snaps the active tab's cursor onto the first selectable row.
func (m *Model) resetCursors() {
	*m.cur(m.active) = 0
	m.repoTab.paneCursor = [numPanes]int{}
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
	if m.alone || m.tabIndex(t) < 0 {
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
	if m.tabIndex(m.active) >= 0 {
		return
	}
	m.active = tabLocal
	if tabs := m.visibleTabs(); m.tabIndex(tabLocal) < 0 && len(tabs) > 0 {
		m.active = tabs[0]
	}
	m.clampCursor()
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
	withIssues := m.hasTracker("github")
	if !m.wants(srcSync) || m.syncing || !(force || m.remote.Stale(withIssues)) {
		return nil
	}
	m.syncing = true
	m.inflight++
	return syncCmd(m.ctx, m.checkoutDir(), m.nwo, withIssues, force)
}

// refocus resumes the GitHub poll after the terminal regained focus. Before the
// launch sync has been considered it starts nothing — launchSyncCmd owns the
// first sync and waits for local git, which the first PR-row paint needs.
// After that, startSyncCmd's TTL gate makes it one sync when the cache is stale
// and nothing at all when it is fresh.
func (m *Model) refocus(source string) tea.Cmd {
	m.blurred = false
	var cmd tea.Cmd
	if m.synced {
		cmd = m.startSyncCmd(false)
	}
	data.LogSync(m.nwo, source, 0, fmt.Sprintf("poll=resumed sync=%t", cmd != nil), nil)
	return cmd
}

// refocusOnInput resumes a paused poll on a key press or mouse event, the
// fallback for a focus-in event that never reached the dashboard.
func (m *Model) refocusOnInput() tea.Cmd {
	if !m.blurred {
		return nil
	}
	return m.refocus("input")
}

// launchSyncCmd fires the one-time launch sync, but only once local git has
// landed (so a PR row paints with its worktree overlay the first time it is
// drawn) — and, for a repo that declares "beads" among its trackers, only
// once the probe that workspace's data comes from has also answered. Whether
// the sync itself asks GitHub for issues is decided by the declared tracker
// list alone (startSyncCmd), so a repo with no "beads" tracker — or a shell
// with no mounted tab declaring the beads source — never waits on the probe.
func (m *Model) launchSyncCmd() tea.Cmd {
	if m.synced || !m.haveLocal {
		return nil
	}
	if m.sourceOn(srcBeads) && !m.feeds.beads.probed {
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
		loadLocalCmd(m.ctx, m.repoRoot, m.checkoutDir(), m.current, m.def),
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

// currentRow returns no row at all while focus is on the tab bar — the single
// lever admin-70hb uses to make every row-scoped key (the Work tab's
// left/right submenu interception included) an automatic no-op from there,
// with no per-call-site focus check needed.
func (m Model) currentRow() (rowItem, bool) {
	if m.focus == focusTabs {
		return rowItem{}, false
	}
	if m.active == tabLocal {
		return m.repo().current()
	}
	return m.rowAt(m.visibleRows(), *m.cur(m.active))
}

// bodyRowCount is the active tab's selectable row count — used to decide
// whether the tab bar may hand focus into the body at all.
func (m Model) bodyRowCount() int {
	switch m.active {
	case tabLocal:
		return m.repo().bodyRowCount()
	default:
		n := 0
		for _, r := range m.visibleRows() {
			if r.selectable() {
				n++
			}
		}
		return n
	}
}

// atBodyTop reports whether the body's cursor is already on its first
// selectable row — the gesture `up`/`k` uses to hand focus back to the tab
// bar rather than moving (or wrapping) the cursor.
func (m Model) atBodyTop() bool {
	if m.active == tabLocal {
		return m.repo().atTop()
	}
	rows := m.visibleRows()
	for i, r := range rows {
		if r.selectable() {
			return *m.cur(m.active) <= i
		}
	}
	return true
}

// enterBody moves focus into the body — `j`/`down`/`enter` from the tab bar.
// A tab with nothing selectable in it (an empty list) declines rather than
// stranding focus somewhere no row key can reach.
func (m *Model) enterBody() {
	if m.bodyRowCount() == 0 {
		return
	}
	m.focus = focusBody
	m.clampCursor()
}

// leaveBody hands focus back to the tab bar. It never touches m.cursor or
// the Repo tab's paneCursor — those persist untouched, which is the whole of what makes a
// trip back into the body land on the same row again.
func (m *Model) leaveBody() {
	if m.alone {
		return
	}
	m.focus = focusTabs
}

// Update handles messages and key input. It wraps update with two pieces of
// shared bookkeeping: it sizes an attached terminal view's child to the body
// region (fitTerminal), and whenever update's changes bump m.toastGen and
// leave a non-error status on screen, it schedules that status's expiry — the
// one place each decision is made, so no individual case inside update has to
// remember either.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	genBefore := m.toastGen
	updated, cmd := m.update(msg)
	mm := updated.(Model)
	mm.fitTerminal()
	if mm.toastGen != genBefore && mm.status != "" && mm.statusFg != cRed {
		cmd = tea.Batch(cmd, statusExpireCmd(mm.toastGen))
	}
	return mm, cmd
}

// update is Update's actual message handling — see Update's own doc comment
// for why status-toast expiry is scheduled around this rather than inside
// it.
func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case localDataMsg:
		// A read that started before one already applied is older than what is on
		// screen; replacing the model with it would bring back a branch a delete
		// just removed. Everything below still runs, so the flags a reload sets
		// and clears stay in step.
		if msg.seq == 0 || msg.seq >= m.localApplied {
			m.local = msg.d
			if msg.seq != 0 {
				m.localApplied = msg.seq
			}
		}
		m.haveLocal = true
		m.clampCursor()
		// The first local load is one of the two gates on the launch sync. Waiting
		// for git rather than firing at Init is what lets PR rows paint with their
		// worktree overlay (enter → cd) the first time they are drawn, instead of
		// after a re-render — and every later local reload leaves synced set, so the
		// reload each sync ends with cannot start another one.
		m.fsReloading = false
		if m.fsStale {
			m.fsStale, m.fsReloading = false, true
			return m, tea.Batch(m.launchSyncCmd(), loadLocalCmd(m.ctx, m.repoRoot, m.checkoutDir(), m.current, m.def))
		}
		return m, m.launchSyncCmd()

	case beadsDetectedMsg:
		cmd := m.feeds.beads.update(msg, m.headless)
		m.clampCursor()
		return m, tea.Batch(cmd, m.launchSyncCmd())

	case beadsMsg, beadsReprobeMsg, beadsTickMsg:
		cmd := m.feeds.beads.update(msg, m.headless)
		m.clampCursor()
		return m, cmd

	case cacheDataMsg:
		if msg.ok {
			m.remote = msg.r
			m.haveData = true
		}
		m.settleActiveTab()
		m.clampCursor()
		return m, nil

	case identityMsg:
		// The zero identity (gh failed, nothing fresh cached) is applied too:
		// ownership goes back to unknown rather than keeping a stale account.
		m.identity = msg.id // the identity feed, shared with the tabs that declared it
		return m, nil

	case watcherReadyMsg:
		m.fsWatcher = msg.w
		m.fsCh = msg.ch
		return m, waitForFSCmd(m.fsCh)

	case fsChangedMsg:
		// A file changed on disk. Reload local git so the header's dirty state and
		// the branch list's unsaved-work marking stay live — this fires on every
		// tracked-dir change, not just admin.toml, which is exactly why the
		// manifest reload below is gated: re-parsing and overwriting the status
		// line on a source-file save or a build artifact would clobber whatever
		// the status line was showing for something the user didn't cause.
		// Compare admin.toml's own stat against the stamp loadManifest captured
		// last time; only a real difference (including a manifest edited to be
		// broken, whose mtime/size changed) reloads and announces.
		var reloadCmds []tea.Cmd
		if st := statManifest(m.repoRoot); st != m.manifestStamp {
			m.loadManifest()
			m.settleActiveTab()
			if m.manifestErr != "" {
				m.setStatus("admin.toml: "+m.manifestErr, cRed)
			} else if m.manifestOK {
				m.setStatus("manifest reloaded", cDim)
			}
			// Init only starts the sources that are ready at launch — an
			// admin.toml that doesn't exist yet, or that gains
			// tracker="beads", after this dashboard launches would leave the
			// Work and Beads tabs on their zero state forever. Start whatever
			// the reloaded manifest made ready; claimSources' started set is
			// what keeps a manifest that flaps invalid and back valid from
			// starting a second work tick or a redundant `bd where`.
			reloadCmds = append(reloadCmds, m.sourceCmds(m.claimSources()))
		}
		reloadCmds = append(reloadCmds, waitForFSCmd(m.fsCh))
		if m.fsReloading {
			m.fsStale = true
		} else {
			m.fsReloading = true
			reloadCmds = append(reloadCmds, loadLocalCmd(m.ctx, m.repoRoot, m.checkoutDir(), m.current, m.def))
		}
		return m, tea.Batch(reloadCmds...)

	case syncDoneMsg:
		m.syncing = false
		m.syncGen++
		m.fetchDone()
		// ApplyTo overlays each section that came back clean and leaves a failed
		// one's cached rows and stamp alone, so one timed-out `gh` call empties
		// nothing on screen.
		prevFailed := m.remote.FetchErrors()
		msg.r.ApplyTo(&m.remote)
		m.settleSyncFailure(prevFailed, msg.r.FetchErrors(), msg.at)
		m.haveData = true
		m.settleActiveTab()
		m.clampCursor()
		// The sync pruned remote-tracking refs, so "gone" and "on origin" need
		// recomputing — re-read local git rather than trying to patch the maps.
		return m, loadLocalCmd(m.ctx, m.repoRoot, m.checkoutDir(), m.current, m.def)

	case tea.BlurMsg:
		m.blurred = true
		data.LogSync(m.nwo, "blur", 0, "poll=paused", nil)
		return m, nil

	case tea.FocusMsg:
		return m, m.refocus("focus")

	case syncTickMsg:
		if m.blurred {
			// Re-arm so the poll resumes on its own cadence after a refocus, but
			// spend no gh call while nobody is looking.
			data.LogSync(m.nwo, "tick", 0, "skipped=blurred", nil)
			return m, m.syncTickCmd()
		}
		// The poll. It fires regardless of which tab is on screen — the merged set
		// behind the Repo tab and the PR list behind the PRs tab go stale at the
		// same rate whether or not you are looking at them. Re-arm unconditionally
		// so the next tick still comes even when this one found a sync in flight.
		cmds := []tea.Cmd{m.startSyncCmd(false), m.syncTickCmd()}
		if m.wants(srcIdentity) {
			cmds = append(cmds, fetchIdentityCmd(m.ctx))
		}
		return m, tea.Batch(cmds...)

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
		if !m.repoTab.naming {
			return m, nil
		}
		m.repoTab.resolving = false
		if msg.err != nil {
			// Keep the prompt and the typed text so the input can be corrected.
			m.repoTab.nameErr = msg.err.Error()
			return m, nil
		}
		m.repoTab.closeNamePrompt()
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

	case openManifestMsg:
		if msg.err != nil {
			if msg.name == "" {
				m.setStatus("open failed: "+msg.err.Error(), cRed)
			} else {
				m.setStatus("open failed: "+msg.name+": "+msg.err.Error(), cRed)
			}
			return m, nil
		}
		// Nothing suspended, so there is no return signal to reload on — the
		// launched app owns its own window, and the fsnotify watcher
		// (fsChangedMsg) is what notices the save and reloads the manifest.
		m.setStatus("opened "+msg.target+" in "+msg.name, cDim)
		return m, nil

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
		return m, loadLocalCmd(m.ctx, m.repoRoot, m.checkoutDir(), m.current, m.def)

	case settingsMsg:
		m.cfgMerged = msg.cfg
		return m, m.updateTabs(msg)

	case workDispatchStartedMsg:
		// A hosted dispatch reports "started" immediately rather than an
		// exit status — the command keeps running on its own pty long after
		// this message lands. Handling it repaints the Commands tab, so the
		// live-work entry StartHostedPTY just registered shows up without
		// waiting on a keypress; the work source's own tick keeps it current
		// from there. Starting another tick here would add a second 1s chain
		// per dispatch for the life of the process.
		//
		// The Commands tab ends the row's "starting" window and runs an
		// attach queued during it (commandsTab.dispatchStarted) — after the
		// status line is set, so a failed attach's own red line is the one
		// left on screen.
		if msg.err != nil {
			m.setStatus(msg.name+" failed to start: "+msg.err.Error(), cRed)
			return m, m.updateTabs(msg)
		}
		m.setStatus(msg.name+" started", cGreen)
		m.refreshDetailProbes()
		return m, tea.Batch(m.updateTabs(msg), recordUsageCmd(m.nwo, resolvedInvocation(msg.name, msg.args)))

	case liveWorkSyncMsg:
		m.setStatus(msg.err.Error(), cRed)
		return m, nil

	case workflowDispatchDoneMsg:
		m.workflowDispatchDone(msg)
		return m, nil

	case workTickMsg:
		// Re-arm unconditionally, exactly like syncTickMsg — this is the Work
		// tab's only wake source for a livework.Entry another goroutine mutated
		// (see Init's own comment). Also the steady heartbeat for the detail
		// pane's reachability probe and the per-agent status poll: kicked from
		// here, never from View, so starting either stays a side effect and
		// rendering stays pure.
		m.refreshDetailProbes()
		return m, tea.Batch(m.workTickCmd(), syncLiveWorkCmd(m.repoRoot), pollLivenessCmd(m.cfgMerged.Terminal.Status), reapWorkersCmd(m.repoRoot, m.cfgMerged.Terminal), m.pollPaneTailCmd(m.cfgMerged.Terminal.Read))

	case livenessMsg:
		// The only writer of the "pane closed out from under us" and the
		// agent's waiting/running transitions; the next workTickMsg's repaint
		// picks them up through the livework.Default.State reads workview.go
		// already makes.
		kinds.ApplyLiveness(msg.results)
		return m, nil

	case paneTailMsg:
		applyPaneTail(msg)
		return m, nil

	case reapedMsg:
		// The ended rows are already gone from the Workers tab; this line is
		// the only on-screen trace of what happened to their worktrees.
		m.setStatus(strings.Join(msg.outcomes, " · "), cDim)
		return m, nil

	case termTickMsg:
		if !m.termAttached {
			return m, nil
		}
		return m, termTickCmd()

	case tea.KeyMsg:
		// Input proves someone is looking, whether or not the focus-in event that
		// should have come first ever arrived.
		resume := m.refocusOnInput()
		next, cmd := m.handleKey(msg)
		return next, tea.Batch(resume, cmd)

	case tea.MouseMsg:
		resume := m.refocusOnInput()
		next, cmd := m.handleMouse(msg)
		return next, tea.Batch(resume, cmd)

	case selWatchdogMsg:
		if msg.gen != m.selGen || !m.sel.inProgress() {
			return m, nil
		}
		return m.finishStaleSelection()

	case statusExpiredMsg:
		// An error status (fg == cRed) never expires on the timer — it
		// clears on the next keypress instead (handleKey) — so even a tick
		// that matches the current gen leaves it alone.
		if msg.gen == m.toastGen && m.statusFg != cRed {
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
//
// Press and motion both (re)arm the watchdog: cell-motion mouse reporting
// stops emitting events entirely once the cursor leaves the terminal's grid,
// so a drag that walks off-window neither advances nor sees the release that
// would normally end it. Without this, that gesture's highlight would freeze
// in place and then outlive the mouse-up forever; see finishStaleSelection
// for how the watchdog resolves it instead.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Action {
	case tea.MouseActionPress:
		switch msg.Button {
		case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
			return m.handleWheel(msg.Y, msg.Button == tea.MouseButtonWheelDown)
		case tea.MouseButtonLeft:
		default:
			return m, nil
		}
		m.sel = anchorAt(msg.Y, msg.X)
		m.selGen++
		return m, selWatchdogCmd(m.selGen)
	case tea.MouseActionMotion:
		if !m.sel.inProgress() {
			return m, nil
		}
		m.sel.drag(msg.Y, msg.X)
		m.selGen++
		return m, selWatchdogCmd(m.selGen)
	case tea.MouseActionRelease:
		if !m.sel.inProgress() {
			return m, nil
		}
		return m.finishMouse(msg)
	}
	return m, nil
}

// selWatchdogMsg fires selIdleTimeout after the mouse event that scheduled it
// (a press or a drag motion). gen ties it to that exact event: if a newer
// press or motion has arrived since, m.selGen has moved on and this msg is a
// no-op instead of cutting off a selection that is still live.
type selWatchdogMsg struct{ gen int }

// selIdleTimeout is how long a drag-to-copy gesture may go without a fresh
// mouse event before it's assumed abandoned — the cursor left the terminal
// window and no further motion or release will ever arrive for it.
const selIdleTimeout = 500 * time.Millisecond

func selWatchdogCmd(gen int) tea.Cmd {
	return tea.Tick(selIdleTimeout, func(time.Time) tea.Msg { return selWatchdogMsg{gen: gen} })
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

// statusExpiredMsg clears the status toast once it has had its
// statusToastDuration on screen — but only if gen still matches: a toast a
// newer status line has already replaced must not be blanked out from under
// it. Update schedules one of these around every non-error setStatus call.
type statusExpiredMsg struct{ gen int }

// finishStaleSelection ends a drag-to-copy gesture the watchdog caught mid-
// flight — no fresh mouse event in selIdleTimeout, almost always because the
// cursor walked off the terminal window and cell-motion reporting has
// nothing left to send. A bare press that never became a drag is just
// dropped, the same as a real mouse-up would drop it. A real drag is pushed
// out to the frame edge in the direction its last motion sample was heading
// (extendToEdge) — the best available stand-in for wherever the release that
// will now never arrive actually happened — then copied exactly like a real
// release.
func (m Model) finishStaleSelection() (tea.Model, tea.Cmd) {
	sel := m.sel
	m.sel = selection{}
	if !sel.active() {
		return m, nil
	}
	lines, _ := m.frameRaw()
	if maxRow := len(lines) - 1; maxRow >= 0 {
		sel.extendToEdge(maxRow)
	}
	return m.copySelection(sel)
}

// statusToastDuration is how long a non-error status toast stays up.
const statusToastDuration = 3 * time.Second

func statusExpireCmd(gen int) tea.Cmd {
	return tea.Tick(statusToastDuration, func(time.Time) tea.Msg { return statusExpiredMsg{gen: gen} })
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
	m.setStatus("copied to clipboard", cGreen)
	return m, nil
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
				m.focus = focusTabs
				m.switchTab(t.tab)
				return m, nil
			}
		}
		return m, nil
	case hitRow:
		if v := m.repo(); m.herdr && h.pane == paneBranches && x == 1+gutterWidth(v.selectMode())-brGutterW {
			if id, ok := v.spaceAtRow(h.row); ok {
				return m.focusSpace(id)
			}
		}
		return m.clickRow(h.pane, h.row)
	}
	return m, nil
}

// focusSpace jumps herdr to an already-open workspace and leaves the dashboard
// where it is.
func (m Model) focusSpace(id string) (tea.Model, tea.Cmd) {
	return m, func() tea.Msg {
		if err := shim.FocusWorkspace(id); err != nil {
			return actionMsg{text: "focus failed", err: fmt.Errorf("herdr focus failed: %w", err)}
		}
		return actionMsg{text: "focused herdr space", color: cGreen}
	}
}

// clickRow is a click resolved to one pane's row: the first click on a row
// moves the cursor (and, on the Local tab, focus) there; a second click on
// the row already under the cursor activates it, exactly as enter would.
func (m Model) clickRow(pane localPane, row int) (tea.Model, tea.Cmd) {
	if row < 0 {
		return m, nil
	}
	// A click arriving while focus was on the tab bar is always a first
	// click — even onto the row the (stale, tab-bar-owned) cursor already
	// sat on — since nothing was current there for it to be a repeat of.
	fromTabs := m.focus == focusTabs
	m.focus = focusBody
	if m.active == tabLocal {
		if !fromTabs && m.repoTab.pane == pane && m.repoTab.paneCursor[pane] == row {
			return m.activate()
		}
		m.repoTab.pane = pane
		m.repoTab.paneCursor[pane] = row
		return m, nil
	}
	if !fromTabs && *m.cur(m.active) == row {
		return m.activate()
	}
	*m.cur(m.active) = row
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// An error status (fg == cRed) doesn't expire on the toast timer — Update
	// only schedules an expiry for a non-error status — it clears on the next
	// keypress instead, whatever that key turns out to do. errDetail (the
	// `e`-for-detail overlay's own content) is a separate field and survives
	// this; only the status line itself clears.
	if m.statusFg == cRed {
		m.status, m.statusFg, m.statusHint = "", nil, ""
	}

	// The terminal view (admin-3rgp.65, narrowed by admin-3rgp.71) owns the
	// keyboard completely while attached: only esc detaches, so everything
	// else — including "q" — goes to the child, and nothing below this
	// (including the global "q" quit) ever sees a keystroke meant for the
	// process being watched. It still sits ahead of the quit confirm: "q"
	// means two different things depending on where you are (write it to
	// the child while attached, raise the quit confirm from the list), and
	// detaching first is what makes the list's meaning reachable at all —
	// the confirm can only be opened from the list, which you are not on
	// while attached.
	if m.termAttached {
		return m.terminalKey(msg)
	}

	// The quit-confirm overlay (admin-3rgp.63) owns the keyboard the instant
	// it is up — ahead of every other overlay — so nothing else can shadow
	// the one question left to answer: kill the hosted processes and quit,
	// or cancel and leave them running.
	if m.quitOpen {
		return m.quitConfirmKey(msg.String())
	}

	// Confirm phase of the delete modal swallows everything but y/n.
	if m.confirming {
		switch msg.String() {
		case "y", "Y":
			return m.startDelete()
		default:
			m.confirming = false
			m.confirmTargets = nil
			m.setStatus("cancelled", cDim)
			return m, nil
		}
	}

	// The worktree-target picker overlay (admin-76tv) owns the keyboard the
	// same way the workflow picker above does — a plain list, nothing to
	// resolve, so it sits right beside it rather than behind any state the
	// workflow picker itself depends on.
	if m.wtPickerOpen {
		return m.worktreePickerKey(msg.String())
	}

	// A tab's own overlay (the Beads tab's bead box) owns the keyboard while
	// it is up, the same way the error-detail overlay does below: the tab
	// takes every key rather than letting one move the list it is drawn over.
	if tm := m.tabModel(m.active); tm != nil && tm.modal() {
		_, cmd := tm.key(&m.shell, msg)
		return m, cmd
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

	// A row-scoped key binding (ADR-0018, admin-l6kb.2) is consulted after
	// every overlay/modal/naming/filter early-return above — so a bound key
	// can never steal a keystroke meant for one of those — but before the
	// dashboard's own built-in switch below, so a HIT preempts the built-in
	// entirely and a MISS falls through to it completely unchanged.
	if mm, cmd, ok := m.rowKeyDispatch(msg.String()); ok {
		return mm, cmd
	}

	// A tab with a model of its own gets the key next: the keys it owns
	// preempt the built-in switch, and everything else falls through to it.
	if tm := m.tabModel(m.active); tm != nil {
		if handled, cmd := tm.key(&m.shell, msg); handled {
			return m, cmd
		}
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m.requestQuit()
	case "right":
		// Arrows step along the drawn tab order, whatever this repo draws.
		// The Work tab's targets always render now, so right has no submenu
		// left to open — it's pure tab-switching everywhere, same as l.
		m.filter = ""
		m.stepTab(1)
	case "l":
		m.filter = ""
		m.stepTab(1)
	case "left":
		m.filter = ""
		m.stepTab(-1)
	case "h":
		m.filter = ""
		m.stepTab(-1)
	case "esc":
		// No-op on the Work tab: there is no submenu left to close.
	case "1", "2", "3", "4", "5", "6", "7":
		// The digits number the tabs in the order they are drawn, so `3` is the
		// third tab on screen — Beads in a beads repo, Issues in a GitHub one.
		n, _ := strconv.Atoi(msg.String())
		m.switchTabAt(n - 1)
	case "j", "down":
		if m.focus == focusTabs {
			m.enterBody()
			return m, nil
		}
		m.moveCursor(1)
	case "up":
		if m.focus == focusTabs {
			return m, nil
		}
		if m.atBodyTop() {
			m.leaveBody()
			return m, nil
		}
		m.moveCursor(-1)
	case "k":
		// k is cursor-up everywhere in this dashboard, the Work tab included
		// (admin-3rgp.72.12) — a footer advertising `k kill` beside `j`/`k`
		// navigation was a collision waiting to fire on a row someone did not
		// mean to be on. Kill lives on `x` now (see case "x" below).
		if m.focus == focusTabs {
			return m, nil
		}
		if m.atBodyTop() {
			m.leaveBody()
			return m, nil
		}
		m.moveCursor(-1)
	case "/":
		m.filtering = true
		m.filter = ""
	case "r":
		// The Commands and Workers tabs take `r` first and swallow it
		// (workSurface.key). On an Issues/PRs row, refresh just that row — one targeted GraphQL call
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
		// The one key that means the same thing on every tab: open the
		// manifest every tab is rendering in the desktop's default handler
		// for its type. It used to open the row's page in a browser, which is
		// why no row kind advertises `o open` any more.
		return m, openManifestCmd(m.manifestDir())
	case "i":
		return m.openIssue()
	case "e":
		// Show the last failure's full git output. Available from any tab: the
		// action that failed may have been run from a different one.
		if m.errDetail != "" {
			m.errOpen = true
		}
	case "s":
		if m.active == tabBeads || m.active == tabIssues {
			m.trackSort = m.trackSort.next()
			*m.cur(m.active) = 0
			m.setStatus("sorted: "+m.trackSort.label(), cDim)
			if err := viewstate.SaveTrackSort(m.manifestDir(), m.trackSort.viewString()); err != nil {
				m.setStatus("view.toml: "+err.Error(), cRed)
			}
		}
	case "enter":
		if m.focus == focusTabs {
			m.enterBody()
			return m, nil
		}
		return m.activate()
	case "alt+enter", "W":
		// W is a plain-letter fallback for terminals that swallow alt+enter
		// (same class that collapses shift+enter to a bare CR); mnemonic pair
		// with w = new named worktree.
		if m.active == tabLocal && m.repoTab.pane == paneBranches {
			return m.worktreeCurrentBranch()
		}
	case "T":
		// Worktree-target picker (admin-76tv): global, not tab-scoped — the
		// same reason `q`/arrow navigation isn't either. Free of any existing
		// binding (not in keymap.Reserved, and every tab already treats a
		// bare "T" as a no-op).
		return m.startWorktreePicker()
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
		return m, checkoutInPlaceCmd(m.ctx, m.checkoutDir(), r.branch)
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
	case rowCommand, rowCommandTarget:
		return m, m.commandsTab.activate(&m.shell, r)
	case rowWorker:
		return m, m.workersTab.activate(&m.shell, r)
	case rowConfigValue, rowConfigPath:
		return m, m.configTab.openRow(&m.shell, r)
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
