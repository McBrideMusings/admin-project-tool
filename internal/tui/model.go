// Package tui is the Bubble Tea dashboard: a tabbed shell over three views
// (Local, PRs, Issues) sharing one row/cursor/filter mechanic. Cleanup lives as
// a focusable pane in the Repo (Local) tab's right column.
package tui

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fsnotify/fsnotify"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/keymap"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
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

// Model is the root dashboard model.
type Model struct {
	ctx      context.Context
	repoRoot string
	nwo      string
	// remoteHost and remoteAddr describe a non-GitHub origin (see WithRemote);
	// both are empty when origin is GitHub, absent, or a local path.
	remoteHost string
	remoteAddr string
	current    string
	def        string

	width, height int

	active tab
	cursor [numTabs]int
	// focus is which zone on screen owns the keyboard — the tab bar or the
	// body. New() sets it to focusTabs after settling active, so a fresh
	// session opens on the tab bar; every other Model literal built directly
	// by a test gets focusBody for free from the zero value. See focusZone.
	focus focusZone

	// Repo (local) tab: lazygit-style panes with independent cursors.
	pane       localPane
	paneCursor [numPanes]int
	// historyStashes is which list the strip under Branches shows: false is
	// Commits, true is Stashes. The strip shows one at a time, so the hidden list's
	// pane is never focusable (paneShown).
	historyStashes bool

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
	// tracker list alone (trackers/hasTracker), never from these fields. The
	// probe they hold only runs for a repo that already declared "beads"
	// among its trackers (Init), and decides what's inside that tab: hasBeads is whether the `bd
	// where` probe actually found a workspace, beadsProbed records that the
	// probe has answered — the launch sync waits for it, so a beads repo
	// never spends the `gh` issues call before knowing whether it needs one.
	// beadsErr holds either "declared but not found" or a workspace this
	// build can't read (a schema the `bd` on PATH doesn't agree with), which
	// the pane shows in place of rows rather than passing off as an empty
	// backlog.
	hasBeads    bool
	beadsProbed bool
	// beadsProbing is true from the moment detectBeadsCmd is dispatched until
	// beadsDetectedMsg lands and sets beadsProbed — closing the window where a
	// second dispatch (Init, then a fsChangedMsg firing before the first probe's
	// goroutine returns) would launch a redundant `bd where` subprocess.
	beadsProbing bool
	// beadsPolling is true from the moment loadBeadsCmd is dispatched until its
	// beadsMsg lands — the in-flight guard that keeps the 15s tick, the launch
	// load, and the manual `r` refresh from ever stacking a second `bd list`
	// process on top of one still running. A busy embedded Dolt (one writer at
	// a time) can otherwise wedge every poll indefinitely, piling up one
	// process per tick and making the tracker unwritable for every other
	// session on the machine (admin-fy79).
	beadsPolling bool
	// beadsStale is set when a poll is abandoned at beadsPollTimeout rather
	// than answering — the child was killed, not merely slow, so there is no
	// real data to apply. The last-known m.beads/m.beadsErr are left
	// untouched in that case; this only flips the header's freshness readout
	// to a tinted "stale" so a wedged poll is visible without a working
	// dashboard ever wiping rows it can no longer refresh.
	beadsStale bool
	beadPrefix string // bd workspace prefix, stripped off every displayed id
	// beadTreeMode is which of the three list shapes `g` has cycled to — flat,
	// dependency tree, or family tree. See beadTreeMode in beadtree.go.
	beadTreeMode beadTreeMode
	beads        []bd.Bead
	// trackSort is the active order for the tracker list — the Beads tab's flat
	// list and the Issues tab — cycled by `s` (see tracksort.go). It is shared
	// across both tabs rather than kept per-tab: one key, one order, named in
	// the header. Persisted to view.toml's tracker.sort (admin-y5lc.1): New()
	// loads it through viewstate's cascade and every `s` press saves the new
	// order to the project-level file. It does not reach beadTreeRows — the
	// dependency tree's order already comes from the graph itself, and an
	// arbitrary sort would scramble the blocker-above-blocked shape that view
	// exists to show.
	trackSort trackSort
	haveBeads bool
	beadsErr  string
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

	// identity is who "you" are — the GitHub account `gh api user` reports,
	// cached per host (data.FetchIdentity). It decides which branches the
	// Branches pane treats as your work; see ownership.go. Zero until the cache
	// read or the first fetch lands, which only means the first frame files every
	// attributed branch as Unsorted rather than mis-filing any of them.
	identity data.Identity
	// minesOnly (G) narrows the Branches pane to your work. Clean Up is exempt —
	// see branchRows. Persisted to view.toml's branches.mine_only (admin-y5lc.1):
	// New() loads it through viewstate's cascade and every `G` press saves it
	// to the project-level file.
	minesOnly bool

	// sel is the in-progress or just-finished drag-to-copy gesture — anchor on
	// mouse-down, drag on motion. It is reset to its zero value (selection{},
	// phase selNone) in the same Update that finishes a real drag, so the
	// highlight is gone on the very next frame; selection never persists.
	//
	// That invariant depends on a release event actually arriving. Cell-motion
	// mouse reporting (the mode this program enables) only emits escape
	// sequences for positions inside the terminal's grid — a button released
	// after the cursor has left the window generates no SGR sequence at all,
	// so finishMouse's tea.MouseActionRelease case never runs. selGen/the
	// selWatchdogMsg cycle below is the self-heal for exactly that: a gesture
	// with no fresh mouse event in selIdleTimeout is finished the way a real
	// release would have — see finishStaleSelection — so a lost release
	// can't leave the highlight stuck, and a lost motion frozen mid-drag
	// still resolves to a real copy instead of nothing.
	sel selection
	// selGen counts drag-to-copy presses/motions; a scheduled selWatchdogMsg
	// only cancels the gesture if its gen still matches, so an older
	// watchdog from before the most recent motion is a no-op.
	selGen int
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
	// workTicking is true once workTickCmd's self-perpetuating loop (Init, or a
	// later manifestOK false→true transition in fsChangedMsg) has been started —
	// checked instead of manifestOK itself so a manifest that flaps invalid and
	// back valid across several edits starts exactly one loop for the life of
	// the process, not one per transition.
	workTicking bool
	// manifestRoot is the directory whose admin.toml this dashboard reads, and
	// it is not always repoRoot. git.RepoRoot deliberately returns the MAIN
	// worktree from inside a linked one (internal/git/git.go), which is right
	// for branches, worktrees and PRs — they are repo-wide — but wrong for the
	// manifest: admin.toml is per-checkout and never committed, so a worktree
	// that declares one had its Work tab silently dropped while `admin <cmd>`
	// in that same directory ran the commands fine. cmd.resolveProjectManifest
	// walks up from the working directory (manifest.FindRoot); this mirrors it,
	// so the Work tab lists exactly the commands `admin <name>` would run here.
	// Empty means "fall back to repoRoot" — see manifestDir.
	manifestRoot string
	// checkout is the top level of the checkout the dashboard was opened in,
	// which differs from repoRoot inside a linked worktree for the same reason
	// manifestRoot does. Every git read and write runs there except the main
	// checkout's own working status: the current branch, its commit list, pull
	// and checkout all belong to the checkout the user is standing in. Empty
	// means "fall back to repoRoot" — see checkoutDir.
	checkout string
	// manifestStamp is admin.toml's mtime+size as of the last loadManifest
	// call — the gate fsChangedMsg checks before reloading, so a save
	// anywhere else in the repo (a source file, a build artifact) doesn't
	// re-parse the manifest or overwrite the status line.
	manifestStamp manifestStamp
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
	// rowKeys is the resolved [keys.<row-kind>] lookup (ADR-0018,
	// admin-l6kb.2), built once at New from validate.RowKeys over
	// m.manifest.Keys — see rowkeys.go. Nil (not just empty) when there is
	// no manifest at all, which is what makes rowKeyDispatch a clean miss
	// on every project written before this feature existed.
	rowKeys map[keymap.RowKind]map[string]string
	// pendingDispatch holds the command names whose dispatchCommandCmd is in
	// flight — the window between the enter keypress and the
	// workDispatchStartedMsg that reports either a livework.Entry now exists
	// or the spawn failed. commandRowItem reads it to set workRowData.pending
	// so the row renders "starting" instead of idle, and activateCommand
	// consults it to refuse a second dispatch of the same command during
	// that window (admin-qk77). A map, not a single string, because more
	// than one command's dispatch can be in flight at once. Shared state
	// across every copy of this value-type Model (map identity, not the map
	// itself, is what's copied) — mutate it only from Update, never from
	// View.
	pendingDispatch map[string]bool
	// pendingAttach holds the command names whose enter arrived WHILE their
	// dispatch was still pending — activateCommand records the intent to
	// attach here instead of dispatching a second run, and the
	// workDispatchStartedMsg handler drains it once the entry the attach
	// needs actually exists.
	pendingAttach map[string]bool
	// pendingTarget holds, for a command name currently in pendingDispatch,
	// WHICH target key that in-flight dispatch was for — admin-2y6p follow-
	// up (a user report that dispatching one target of a multi-target
	// command, e.g. "counter" under "command-test", showed every sibling
	// target as "starting" too). pendingDispatch/pendingAttach stay keyed by
	// command name alone (at most one dispatch in flight per command — the
	// existing pending guard), but a multi-target command's OWN target rows
	// (targetRows) must only mark the ONE row actually being dispatched, not
	// every target sharing that command name. Absent or "" means either no
	// dispatch is pending for that command, or it was a flat (non-target)
	// dispatch — targetRows treats both the same way: nothing lights up.
	pendingTarget map[string]string
	// workCollapsed is the Work tab's display mode, toggled by `c` (admin-4zz9):
	// false (the zero value, and the launch default when no view.toml sets it)
	// is expanded — every command's targets render, byte-identical to the
	// dashboard's original behavior. true is collapsed — only the command
	// named by workOpenCmd shows its targets; see workRows' showTargets gate.
	// Persisted to view.toml's work.collapsed (admin-y5lc.1): New() loads it
	// through viewstate's cascade — seeding workOpenCmd from the cursor row
	// when it starts collapsed, since toggleWorkCollapse isn't what set it
	// this time — and toggleWorkCollapse saves every flip to the
	// project-level file.
	workCollapsed bool
	// workOpenCmd is which command's targets show while workCollapsed is
	// true: the command owning whatever row the cursor is currently on.
	// toggleWorkCollapse seeds it the instant collapse turns on (from the
	// row under the cursor at that moment); moveCursorWork keeps it in sync
	// as the cursor moves through a collapsed row list. New() seeds it the
	// same way when the dashboard launches already collapsed (view.toml's
	// work.collapsed = true), since there is no toggle keypress to do it.
	// Ignored entirely while workCollapsed is false.
	workOpenCmd string

	// Config tab (admin-3rgp.21): the resolved config.Config — repoRoot's
	// admin.toml [worktrees]/[[populate]]/[beads]/[workflow]
	// tables, merged with its declared archetypes. Each field's origin
	// (admin.toml itself, or the contributing archetype) comes from
	// keyOrigin, the same mechanism the manifest-declared keys use — see
	// work.go's computeOrigins. Loaded once at launch by loadConfigCmd; zero
	// until settingsMsg lands, zero like identity is.
	cfgMerged config.Config
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
	wfRun       string
	wfCeiling   int

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

	// Quit confirm overlay (admin-3rgp.63): quitting while the dashboard is
	// hosting a running process on its own pty must not kill it silently —
	// see requestQuit in quitconfirm.go. quitHosted is the snapshot taken
	// when the confirm opened, so accepting kills exactly what was shown
	// even if the registry changes under it.
	quitOpen   bool
	quitHosted []*livework.Entry

	// Terminal view (admin-3rgp.65): attaching to a pty-addressed live-work
	// row replaces the whole tab with that entry's Screen rather than the
	// list/detail layout — see terminalview.go. termAttached is the switch
	// view.go and handleKey both read; the rest describe the one open view,
	// all zeroed together by detachTerminal.
	termAttached bool
	termEntryID  string
	termRows     int
	termCols     int
}

// manifestDir is the directory to read admin.toml, and anything co-located with
// it, out of. Zero-value Models (every table-driven test that sets only
// repoRoot) keep their old behaviour.
func (m Model) manifestDir() string {
	if m.manifestRoot != "" {
		return m.manifestRoot
	}
	return m.repoRoot
}

// checkoutDir is the directory the dashboard's git commands run in — see the
// checkout field.
func (m Model) checkoutDir() string {
	if m.checkout != "" {
		return m.checkout
	}
	return m.repoRoot
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
		ctx:             ctx,
		repoRoot:        repoRoot,
		nwo:             nwo,
		current:         current,
		def:             def,
		active:          tabLocal,
		herdr:           shim.Available(),
		selected:        map[string]bool{},
		pendingDispatch: map[string]bool{},
		pendingAttach:   map[string]bool{},
		pendingTarget:   map[string]string{},
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
	m.workCollapsed = vs.WorkCollapsed
	if mode, ok := beadTreeModeFromView(vs.BeadTree); ok {
		m.beadTreeMode = mode
	}
	if sort, ok := trackSortFromView(vs.TrackSort); ok {
		m.trackSort = sort
	}
	m.minesOnly = vs.MinesOnly
	m.loadManifest()
	if m.manifestOK {
		// AC: "the dashboard opens on the Work tab when the project has a
		// manifest."
		m.active = tabWork
	}
	if m.workCollapsed {
		// Launching already collapsed (view.toml's work.collapsed = true) has
		// no toggle keypress to seed workOpenCmd the way toggleWorkCollapse
		// does, so do it here from the row the cursor starts on. loadManifest
		// has already run, so workRows() (gated on m.manifestOK) reflects the
		// real command list; clampCursor moves the zero-value cursor off row
		// 0's section header onto the first real row. m.focus is still its
		// zero value (focusBody) here — m.focus = focusTabs runs later below
		// — so currentRow() reads the same row a keypress would at this
		// point, same as toggleWorkCollapse's own before, ok := m.currentRow().
		m.clampCursor()
		if row, ok := m.currentRow(); ok {
			if name := ownerCmdName(row); name != "" {
				m.workOpenCmd = name
			}
		}
	}
	// Init dispatches workTickCmd/detectBeadsCmd exactly when these are true at
	// boot — seed the guard flags to match so fsChangedMsg's later gates don't
	// double-start either chain.
	m.workTicking = m.manifestOK
	m.beadsProbing = m.hasTracker("beads")
	// admin-70hb: a new session opens with focus on the tab bar, not on a
	// body row — set after active settles so it doesn't interact with which
	// tab that is.
	m.focus = focusTabs
	return m
}

// Init kicks off the local git load (instant), the cache read (instant), the file
// watcher, and the sync poll. The first sync itself waits for local git to land
// (see the localDataMsg case), so the first frame paints off git and cache with
// nothing blocking on the network.
func (m Model) Init() tea.Cmd {
	// No per-model state — see installTerminalOpener's own comment on why
	// calling it again on a second Init in the same process is harmless.
	installTerminalOpener()
	cmds := []tea.Cmd{
		loadLocalCmd(m.ctx, m.repoRoot, m.checkoutDir(), m.current, m.def),
		loadCacheCmd(m.nwo),
		m.startWatchCmd(),
		m.syncTickCmd(),
		loadConfigCmd(m.ctx, m.manifestDir()),
		fetchIdentityCmd(m.ctx),
	}
	if m.hasTracker("beads") {
		// The `bd where` probe only runs for a repo that declared "beads"
		// among its trackers — its answer feeds the tab's content (which
		// bead workspace, or an error if none is found), never whether the
		// tab is drawn at all. A repo that didn't declare beads has no
		// reason to pay for this subprocess.
		cmds = append(cmds, detectBeadsCmd(m.ctx, m.repoRoot))
	}
	if m.manifestOK {
		// Start the [status] checks at launch so the first frame is not a
		// second of empty readouts; later refreshes ride workTickMsg.
		if m.manifest != nil {
			statusProbes.Refresh(m.manifest.Status, m.manifest.Root)
		}
		// A herdr pane or agent worker changes state (running → waiting →
		// failed, a new Activity line) from goroutines this process never
		// hears from otherwise — livework.Default has no notification of
		// its own. This tick is the Work tab's only wake source for that,
		// mirroring beadsTickCmd's own reason for existing.
		cmds = append(cmds, m.workTickCmd())
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
	if m.manifest == nil || !m.manifest.HasTracker {
		return nil
	}
	return m.manifest.Trackers
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
// true, and the tracker tabs are Issues and/or Beads depending on the
// declared list — both draw, in declared order, when both are declared
// (admin-j00d). Both PRs and Issues still need a GitHub remote to mean
// anything; Beads does not, since beads is a local database. None of these
// infers itself from live repo state — not from how many PRs happen to be
// open, and not from a `bd where` probe of the repo — that inference is
// exactly what admin-3rgp.22 replaced with the declared keys admin-3rgp.21
// added. The digit keys index this list, so `3` is whatever the third drawn
// tab happens to be.
func (m Model) visibleTabs() []tab {
	var tabs []tab
	if m.manifestOK {
		tabs = append(tabs, tabWork, tabWorkers)
	}
	tabs = append(tabs, tabLocal)
	if m.nwo != "" && m.wantsPRs() {
		tabs = append(tabs, tabPRs)
	}
	for _, t := range m.trackers() {
		switch t {
		case "github":
			if m.nwo != "" {
				tabs = append(tabs, tabIssues)
			}
		case "beads":
			tabs = append(tabs, tabBeads)
		}
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
	case tabWorkers:
		return m.workerRows()
	case tabPRs:
		return m.prRows()
	case tabIssues:
		return m.issueRows()
	case tabBeads:
		return m.beadRows()
	case tabConfig:
		return m.configRows()
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
	// Collapsed Work-tab mode needs its own walk: the row list is a function
	// of (m.workCollapsed, m.workOpenCmd), and workOpenCmd itself has to
	// track where the cursor is heading, so this can't reuse the generic
	// fixed-list skip loop below. Expanded mode (workCollapsed false, the
	// default) renders every row exactly as it always has, so it keeps using
	// the generic walk.
	if m.active == tabWork && m.workCollapsed {
		m.moveCursorWork(delta)
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

// moveCursorWork is moveCursor's collapsed-Work-tab variant. The generic
// walk above assumes the row list is fixed for the whole walk; here it
// isn't, in two ways:
//
//   - Stepping onto a closed command's group header (unselectable, since a
//     header never gets a cursor marker) is the signal to open that
//     command, which inserts its target rows into the list right where the
//     header sits. This lands directly on the first selectable row after
//     the header (moving down) or the last one before it (moving up) —
//     never on the header itself, and never by continuing to add delta to
//     an index whose meaning changed out from under it once the rebuild ran.
//   - Stepping onto an ALREADY-selectable row that belongs to a different
//     command than the one currently open also changes which command is
//     open (to that row's own owner) — and opening or closing a command
//     resizes the list by however many target rows it has. The row just
//     landed on is still in the rebuilt list (opening/closing some OTHER
//     command never removes it), so it's relocated there by identity
//     (sameRowIdentity) rather than by reusing the old index i, which was
//     computed against the list from before the resize.
//
// This is the reason the row list can be treated as "a pure function of
// (mode, cursor)" per the ticket: the cursor here is workOpenCmd (which
// command is open) plus the row index, and moveCursorWork is what keeps the
// two in lockstep as the index moves across a command boundary.
func (m *Model) moveCursorWork(delta int) {
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
		r := rows[i]
		if r.selectable() {
			owner := ownerCmdName(r)
			if owner == m.workOpenCmd {
				m.cursor[m.active] = i
				return
			}
			m.workOpenCmd = owner
			newRows := m.visibleRows()
			for j, rr := range newRows {
				if sameRowIdentity(rr, r) {
					m.cursor[m.active] = j
					return
				}
			}
			m.cursor[m.active] = i // defensive fallback; should be unreachable
			return
		}
		if r.kind == rowCommand && r.group && r.work != nil && r.work.cmdName != m.workOpenCmd {
			name := r.work.cmdName
			m.workOpenCmd = name
			newRows := m.visibleRows()
			idx := indexOfCommandRow(newRows, name)
			if idx < 0 {
				return
			}
			// A command's target rows always sit immediately after its own
			// header, whichever direction opened it — targetRows never
			// inserts anything before the header it belongs to. So the
			// block to land in is the contiguous run starting right after
			// idx, and the direction only picks which end of that run: the
			// first row (moving down, arriving from above) or the last
			// (moving up, arriving from below).
			start := idx + 1
			end := start
			for end < len(newRows) && newRows[end].kind == rowCommandTarget &&
				newRows[end].work != nil && newRows[end].work.cmdName == name {
				end++
			}
			if end == start {
				return // defensive: a group row always has at least one target
			}
			if delta > 0 {
				m.cursor[m.active] = start
			} else {
				m.cursor[m.active] = end - 1
			}
			return
		}
	}
}

// setStatus sets the transient status line and its color in one place.
// toastGen bumps on every call, error included, so Update can tell a status
// change happened and schedule that status's expiry itself — see Update's
// wrapper around update. A caller never needs to thread an expiry cmd of its
// own back out; setting m.status here is enough.
func (m *Model) setStatus(text string, fg lipgloss.TerminalColor) {
	m.status = text
	m.statusFg = fg
	m.statusHint = ""
	m.toastGen++
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
	withIssues := m.hasTracker("github")
	if m.syncing || !(force || m.remote.Stale(withIssues)) {
		return nil
	}
	m.syncing = true
	m.inflight++
	return syncCmd(m.ctx, m.checkoutDir(), m.nwo, withIssues)
}

// launchSyncCmd fires the one-time launch sync, but only once local git has
// landed (so a PR row paints with its worktree overlay the first time it is
// drawn) — and, for a repo that declares "beads" among its trackers, only
// once the probe that workspace's data comes from has also answered. Whether
// the sync itself asks GitHub for issues is decided by the declared tracker
// list alone (startSyncCmd), so a repo with no "beads" tracker never waits on
// the probe at all.
func (m *Model) launchSyncCmd() tea.Cmd {
	if m.synced || !m.haveLocal {
		return nil
	}
	if m.hasTracker("beads") && !m.beadsProbed {
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

// pollBeadsCmd dispatches loadBeadsCmd, but only when no poll is already in
// flight — the single gate every beads read goes through (the launch load in
// beadsDetectedMsg, the repeating beadsTickMsg, and the manual `r` refresh),
// so a slow or contended `bd` can never accumulate more than one outstanding
// `bd list` process no matter which of the three triggers it.
func (m *Model) pollBeadsCmd() tea.Cmd {
	if m.beadsPolling {
		return nil
	}
	m.beadsPolling = true
	return loadBeadsCmd(m.ctx, m.repoRoot)
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
		return m.localCurrent()
	}
	rows := m.visibleRows()
	i := m.cursor[m.active]
	if i >= 0 && i < len(rows) && rows[i].selectable() {
		return rows[i], true
	}
	return rowItem{}, false
}

// bodyRowCount is the active tab's selectable row count — used to decide
// whether the tab bar may hand focus into the body at all.
func (m Model) bodyRowCount() int {
	switch m.active {
	case tabLocal:
		n := 0
		for p := localPane(0); p < numPanes; p++ {
			if m.paneShown(p) {
				n += m.paneCount(p)
			}
		}
		return n
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
		return m.paneCursor[m.pane] == 0
	}
	rows := m.visibleRows()
	for i, r := range rows {
		if r.selectable() {
			return m.cursor[m.active] <= i
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

// toggleHistory switches the strip under Branches between Commits and Stashes.
// Focus follows it when it was on the strip, so the key a user pressed with the
// cursor in Commits leaves the cursor in Stashes, not on a pane that just vanished
// — unless the list it switched to is empty, which has no row to focus, and then
// clampCursor hands focus to Branches.
func (m *Model) toggleHistory() {
	onStrip := m.pane == m.historyPane()
	m.historyStashes = !m.historyStashes
	if onStrip {
		m.pane = m.historyPane()
	}
	m.clampCursor()
}

// leaveBody hands focus back to the tab bar. It never touches m.cursor or
// m.paneCursor — those persist untouched, which is the whole of what makes a
// trip back into the body land on the same row again.
func (m *Model) leaveBody() {
	m.focus = focusTabs
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

// Update handles messages and key input. It wraps update with one piece of
// shared bookkeeping: whenever update's changes bump m.toastGen and leave a
// non-error status on screen, this schedules that status's expiry — the one
// place that decision is made, so no individual case inside update has to
// remember to return its own expiry cmd.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	genBefore := m.toastGen
	updated, cmd := m.update(msg)
	mm := updated.(Model)
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
		if m.termAttached {
			rows, cols := m.termRegion()
			m.termRows, m.termCols = rows, cols
			if e := m.termEntry(); e != nil && e.Ops.Resize != nil {
				_ = e.Ops.Resize(rows, cols)
			}
		}
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
		m.fsReloading = false
		if m.fsStale {
			m.fsStale, m.fsReloading = false, true
			return m, tea.Batch(m.launchSyncCmd(), loadLocalCmd(m.ctx, m.repoRoot, m.checkoutDir(), m.current, m.def))
		}
		return m, m.launchSyncCmd()

	case beadsDetectedMsg:
		// The probe answered: does a beads workspace actually exist here. This
		// only fires for a repo that already declared tracker = "beads" (see
		// Init), so the Beads tab itself is never at stake — it's already
		// drawn on the declaration alone. This decides what's inside it: real
		// rows, or the "declared but not found" state below.
		m.beadsProbed = true
		m.beadsProbing = false
		m.hasBeads = msg.present
		m.beadPrefix = msg.ws.Prefix
		m.beadsErr = ""
		var cmds []tea.Cmd
		switch {
		case errors.Is(msg.err, context.DeadlineExceeded):
			// bd where never answered. Say so, and ask again: unlike a schema
			// mismatch, a wedged bd can recover on its own.
			// hasBeads stays false until a probe answers, so no poll or `r`
			// refresh reads a workspace whose prefix is still unknown.
			m.hasBeads = false
			m.beadsErr = beadsTimeoutText("where")
			cmds = append(cmds, m.beadsReprobeCmd())
		case msg.err != nil:
			m.beadsErr = msg.err.Error()
		case !msg.present:
			m.beadsErr = `tracker = "beads" is declared, but no beads workspace was found here`
		}
		if m.hasBeads && m.beadsErr == "" {
			cmds = append(cmds, m.pollBeadsCmd(), m.beadsTickCmd())
		}
		cmds = append(cmds, m.launchSyncCmd())
		m.clampCursor()
		return m, tea.Batch(cmds...)

	case beadsMsg:
		m.beadsPolling = false
		if !errors.Is(msg.err, context.DeadlineExceeded) {
			m.haveBeads = true
		}
		switch {
		case errors.Is(msg.err, context.DeadlineExceeded):
			// The poll was abandoned at beadsPollTimeout, not answered — bd.List's
			// exec.CommandContext killed the child, so msg carries no real data.
			// Keep the last-known beads/beadsErr untouched and only flag staleness:
			// a slow tick must never wipe good rows off the screen. With no rows
			// yet there is nothing to keep, so the pane names the timeout instead
			// of reading "loading…" forever; the next answer clears it.
			m.beadsStale = true
			if !m.haveBeads {
				m.beadsErr = beadsTimeoutText("list")
			}
		case msg.err != nil:
			m.beadsErr = msg.err.Error()
		default:
			m.beads = msg.beads
			m.beadsErr = ""
			m.beadsStale = false
		}
		m.clampCursor()
		return m, nil

	case beadsReprobeMsg:
		return m, detectBeadsCmd(m.ctx, m.repoRoot)

	case beadsTickMsg:
		// Beads live in a local database, so re-reading them costs a subprocess and
		// no network at all — nothing like the GitHub sync this dashboard rations to
		// one every five minutes. The tick is short enough that a bead an agent
		// creates mid-session shows up while you are still looking at the pane.
		if !m.hasBeads {
			return m, nil
		}
		return m, tea.Batch(m.pollBeadsCmd(), m.beadsTickCmd())

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
		m.identity = msg.id
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
			// Init only fires workTickCmd's first link (line ~584) and
			// detectBeadsCmd (line ~576) when manifestOK/tracker="beads" are
			// already true at startup — an admin.toml that doesn't exist yet,
			// or that gains tracker="beads", when this dashboard launches never
			// gets that one-shot kickoff, so the Work tab and Beads tab painted
			// (fsChangedMsg above already reloads and repaints the manifest
			// itself) but stayed on their zero state forever: nothing ever
			// re-armed workTickCmd's self-perpetuating loop, and
			// detectBeadsCmd/loadBeadsCmd/beadsTickCmd never ran, since both
			// chains only start from Init or from these same guarded spots.
			// Restarting the dashboard re-ran Init and picked it up, which is
			// what made "close and reopen" look like the only fix. Kick each
			// chain here, gated on the dedicated workTicking/beadsProbing flags
			// rather than manifestOK/beadsProbed themselves — a manifest that
			// flaps invalid and back valid across several edits (or two
			// fsChangedMsg events arriving before the first beads probe's
			// goroutine returns) would otherwise start a new concurrent
			// workTickCmd chain, or launch a redundant `bd where`, on every
			// occurrence.
			if !m.workTicking && m.manifestOK {
				m.workTicking = true
				reloadCmds = append(reloadCmds, m.workTickCmd())
			}
			if m.hasTracker("beads") && !m.beadsProbed && !m.beadsProbing {
				m.beadsProbing = true
				reloadCmds = append(reloadCmds, detectBeadsCmd(m.ctx, m.repoRoot))
			}
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
		msg.r.ApplyTo(&m.remote)
		m.haveData = true
		m.settleActiveTab()
		m.clampCursor()
		// The sync pruned remote-tracking refs, so "gone" and "on origin" need
		// recomputing — re-read local git rather than trying to patch the maps.
		return m, loadLocalCmd(m.ctx, m.repoRoot, m.checkoutDir(), m.current, m.def)

	case syncTickMsg:
		// The poll. It fires regardless of which tab is on screen — the merged set
		// behind the Repo tab and the PR list behind the PRs tab go stale at the
		// same rate whether or not you are looking at them. Re-arm unconditionally
		// so the next tick still comes even when this one found a sync in flight.
		return m, tea.Batch(m.startSyncCmd(false), m.syncTickCmd(), fetchIdentityCmd(m.ctx))

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
		m.beadCols = msg.beads.Columns
		m.cfgMerged, m.cfgErr = msg.cfg, msg.loadErr
		return m, nil

	case workDispatchStartedMsg:
		// A hosted dispatch reports "started" immediately rather than an
		// exit status — the command keeps running on its own pty long after
		// this message lands. workTickCmd is what repaints the Work tab so
		// the live-work entry StartHostedPTY just registered shows up
		// without waiting on a keypress.
		//
		// Clearing pendingDispatch here — on both the error and success
		// paths — is what ends the "starting" window a row shows from the
		// moment enter was pressed (admin-qk77): a failed spawn falls back
		// to idle (the red status line below is the only trace it left),
		// and a successful one either shows running (once the entry exists)
		// or idle again if the command already exited before this landed.
		delete(m.pendingDispatch, msg.name)
		delete(m.pendingTarget, msg.name)
		if msg.err != nil {
			delete(m.pendingAttach, msg.name)
			m.setStatus(msg.name+" failed to start: "+msg.err.Error(), cRed)
			return m, nil
		}
		m.setStatus(msg.name+" started", cGreen)
		record := recordUsageCmd(m.nwo, resolvedInvocation(msg.name, msg.args))
		m.refreshDetailProbes()
		if m.pendingAttach[msg.name] {
			delete(m.pendingAttach, msg.name)
			if e := m.liveEntryForCommand(msg.name); e != nil {
				am, acmd := m.attachEntry(e)
				return am, tea.Batch(acmd, m.workTickCmd(), record)
			}
		}
		return m, tea.Batch(m.workTickCmd(), record)

	case workflowResolvedMsg:
		m.workflowResolved(msg)
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
		return m, tea.Batch(m.workTickCmd(), pollAgentStatusCmd(), pollPaneStatusCmd(m.cfgMerged.Terminal.Status), reapWorkersCmd(m.repoRoot, m.cfgMerged.Terminal), m.pollPaneTailCmd(m.cfgMerged.Terminal.Read))

	case agentStatusMsg:
		// applyAgentStatuses is the only writer of the herdr-derived state
		// transition; the next workTickMsg's repaint picks it up through the
		// existing livework.Default.State(e.ID) reads workview.go already makes.
		applyAgentStatuses(msg)
		return m, nil

	case paneTailMsg:
		applyPaneTail(msg)
		return m, nil

	case paneStatusMsg:
		// applyPaneStatuses is the only writer of the "pane closed out from
		// under us" transition; same repaint path as agentStatusMsg above.
		applyPaneStatuses(msg)
		return m, nil

	case termTickMsg:
		if !m.termAttached {
			return m, nil
		}
		return m, termTickCmd()

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)

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
		return m.clickRow(h.pane, h.row)
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
	// A click arriving while focus was on the tab bar is always a first
	// click — even onto the row the (stale, tab-bar-owned) cursor already
	// sat on — since nothing was current there for it to be a repeat of.
	fromTabs := m.focus == focusTabs
	m.focus = focusBody
	if m.active == tabLocal {
		if !fromTabs && m.pane == pane && m.paneCursor[pane] == row {
			return m.activate()
		}
		m.pane = pane
		m.paneCursor[pane] = row
		return m, nil
	}
	if !fromTabs && m.cursor[m.active] == row {
		return m.activate()
	}
	m.cursor[m.active] = row
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
			return m, resolveNameCmd(m.ctx, m.checkoutDir(), m.nwo, name)
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

	// A row-scoped key binding (ADR-0018, admin-l6kb.2) is consulted after
	// every overlay/modal/naming/filter early-return above — so a bound key
	// can never steal a keystroke meant for one of those — but before the
	// dashboard's own built-in switch below, so a HIT preempts the built-in
	// entirely and a MISS falls through to it completely unchanged.
	if mm, cmd, ok := m.rowKeyDispatch(msg.String()); ok {
		return mm, cmd
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
	case "a":
		if m.onWorkSurface() {
			return m.attachCurrentWork()
		}
	case "c":
		// Cursor-following collapse toggle (admin-4zz9): Work-tab only, a
		// no-op everywhere else — every other tab already uses "c" for
		// nothing, so there is nothing to preserve there.
		//
		// On the Repo tab it switches the strip under Branches between Commits and
		// Stashes.
		switch m.active {
		case tabWork:
			m.toggleWorkCollapse()
		case tabLocal:
			m.toggleHistory()
		}
	case "/":
		m.filtering = true
		m.filter = ""
	case "r":
		// The Work tab has no `r`. It must not become "refresh" by falling
		// through either: the Work tab renders the live
		// registry and the resolved manifest, neither of which a refresh
		// fetches, so `r` there would spend a network sync to redraw the same
		// rows. `R` still syncs everything from any tab.
		if m.onWorkSurface() {
			return m, nil
		}
		// The Beads tab has no per-row refresh: re-reading the whole workspace is
		// one local query, so there is nothing a single bead would save. Routed
		// through pollBeadsCmd like the tick and the launch load, so a manual
		// refresh can't stack a second `bd list` on top of one already in flight.
		if m.active == tabBeads {
			return m, m.pollBeadsCmd()
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
			return m, pullCmd(m.ctx, m.checkoutDir())
		}
	case "s":
		if m.active == tabBeads || m.active == tabIssues {
			m.trackSort = m.trackSort.next()
			m.cursor[m.active] = 0
			m.setStatus("sorted: "+m.trackSort.label(), cDim)
			if err := viewstate.SaveTrackSort(m.manifestDir(), m.trackSort.viewString()); err != nil {
				m.setStatus("view.toml: "+err.Error(), cRed)
			}
		}
	case "g":
		if m.active == tabBeads {
			m.beadTreeMode = m.beadTreeMode.next()
			m.cursor[tabBeads] = 0
			switch m.beadTreeMode {
			case beadListBlocksTree:
				m.setStatus("dependency tree — blockers sit above what they block", cDim)
			case beadListFamilyTree:
				m.setStatus("family tree — parents sit above their children", cDim)
			default:
				m.setStatus("flat list, grouped by what's ready", cDim)
			}
			if err := viewstate.SaveBeadTree(m.manifestDir(), m.beadTreeMode.viewString()); err != nil {
				m.setStatus("view.toml: "+err.Error(), cRed)
			}
			break
		}
	case "G":
		if m.active == tabLocal && m.pane == paneBranches {
			m.minesOnly = !m.minesOnly
			m.paneCursor[paneBranches] = 0
			if m.minesOnly {
				if !m.identity.Known() {
					// Narrowing to "yours" with nothing defining yours would empty the pane and
					// look like a repo with no work in it. Say which it is.
					m.minesOnly = false
					m.setStatus("your GitHub account is not known yet — nothing is known to be yours", cYellow)
					break
				}
				m.setStatus("showing your branches (Clean Up still shows everything)", cDim)
			} else {
				m.setStatus("showing every branch", cDim)
			}
			m.clampCursor()
			if err := viewstate.SaveMinesOnly(m.manifestDir(), m.minesOnly); err != nil {
				m.setStatus("view.toml: "+err.Error(), cRed)
			}
		}
	case "w":
		if m.active == tabLocal && m.pane == paneBranches {
			m.naming = true
			m.nameBuf = ""
			m.nameCands = nil
			m.nameSel = -1
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
		if m.active == tabLocal && m.pane == paneBranches {
			return m.worktreeCurrentBranch()
		}
	case " ":
		// Select mode is the selection, so a refused toggle changes nothing: on
		// the protected main row or a branch checked out elsewhere, `space`
		// leaves the pane exactly as it found it.
		if m.active == tabLocal && m.pane == paneBranches {
			m.toggleBranchSelection()
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
		// The Work tab's kill, moved off k (admin-3rgp.72.12). A manifest
		// [keys.command]/[keys.worker] binding for x shadows it (reported by
		// admin check). killCurrentWork is already a no-op with no live row
		// under the cursor.
		if m.onWorkSurface() {
			return m.killCurrentWork()
		}
	case "X":
		// Clean-all works from any Repo-tab pane: the collapsed Cleanup box advertises
		// it from all of them, and it needs no per-row cursor.
		if m.active == tabLocal {
			return m.cleanAll()
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
	case rowCommand:
		return m.activateCommand(r.work)
	case rowCommandTarget:
		return m.activateCommandTarget(r.work)
	case rowWorker:
		// Every rowWorker row carries a non-nil work.worker (workRows()
		// always sets it), so resolveRowAction never returns actionNone
		// here — it's either actionAttach (still running) or
		// actionTranscript (finished); attachEntry's own Attach call is
		// what unifies both into "open its pane" (livework.go's Attach
		// doc comment).
		return m.attachEntry(r.work.worker)
	case rowConfigValue:
		m.setStatus("nothing to open on this row", cDim)
		return m, nil
	case rowConfigPath:
		if r.cfg == nil {
			return m, nil
		}
		if r.cfg.absent {
			m.setStatus(r.cfg.label+" does not exist", cRed)
			return m, nil
		}
		return m, openPathCmd(r.cfg.path, r.cfg.label)
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
