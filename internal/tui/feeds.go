package tui

import (
	"context"
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// feeds holds the state of every data source a tab model reads through a
// handle (ADR-0026). The shell owns each feed: it starts the feed, routes the
// feed's messages to it, and hands a typed handle to every tab whose
// constructor asks for one. Each feed is a pointer so that every holder of a
// handle sees the one copy the shell keeps current, across the Model copies
// Bubble Tea makes on every Update.
type feeds struct {
	beads  *beadsFeed
	config *configFeed
	work   *workFeed
	// syncFeed is what the whole-repo GitHub sync last answered, and localFeed
	// what the last local git read found. Model embeds feeds, so it reads both
	// as its own fields; tabs read them through syncSource and localSource.
	*syncFeed
	*localFeed
}

// syncFeed is the sync source's state: the open PRs, open issues and merged-PR
// head set the last sync (or the on-disk cache) answered, and whether any of
// them has arrived yet. The merged-PR head-branch set (branches a merged PR
// had as its head) is remote.MergedHeads, cached on disk beside the PR and
// issue sections. It comes from GitHub, not git, so it must not live in
// localData — that is replaced wholesale every time local git is re-read, and
// every sync ends with exactly that reload, which would blank a set only the
// slower gh call can refill. branchstate.go joins it with the git-derived
// signals at read time; nothing merges the two sources into a stored value.
type syncFeed struct {
	remote   data.Remote
	haveData bool
}

// localFeed is the last local git read. Local git is not a startable source —
// every tab reads it, so it loads unconditionally in Init.
type localFeed struct {
	local localData
}

// newFeeds builds every feed, idle. A feed starts only when sources.go's
// claimSources finds a mounted tab declaring it.
func newFeeds(ctx context.Context, repoRoot string) feeds {
	return feeds{beads: &beadsFeed{ctx: ctx, root: repoRoot}, syncFeed: &syncFeed{}, localFeed: &localFeed{}, config: &configFeed{}, work: &workFeed{}}
}

// sourceSet is how a tab model's constructor obtains handles to feeds.
// Asking for a handle records the source as declared, so a tab's declaration
// is exactly the handles it asked for — a tab that reads the beads list holds
// a beadsSource, and holding one is what makes the shell start the beads feed
// for it. Each constructor builds its own set from the feeds it is given, so
// one tab's handles never land in another tab's declaration.
//
// The tab's own code reads its data only through the handle fields it holds;
// it never touches Model or feeds after construction. Go cannot enforce that
// inside one package, so it is the rule each tab model keeps, and the reason
// a tab's methods take the shell and nothing else.
type sourceSet struct {
	f        feeds
	declared []source
}

func (d *sourceSet) declare(s source) {
	for _, v := range d.declared {
		if v == s {
			return
		}
	}
	d.declared = append(d.declared, s)
}

// beads declares srcBeads and returns the handle to the beads feed.
func (d *sourceSet) beads() beadsSource {
	d.declare(srcBeads)
	return beadsSource{f: d.f.beads}
}

// work declares srcWork and returns the handle to what the work tick keeps
// current. The live-work registry itself is process-wide state, not a feed
// the shell owns; the handle is the declaration, and the way a tab reads the
// registry's worker rows.
func (d *sourceSet) work() workSource {
	d.declare(srcWork)
	return workSource{f: d.f.work}
}

// config returns the handle to the manifest the shell loads. It declares no
// source: the manifest is read synchronously at launch and re-read on every
// save, for every tab, so there is nothing to start.
func (d *sourceSet) config() configSource { return configSource{f: d.f.config} }

// sync declares srcSync and returns the handle to the sync's last answer.
func (d *sourceSet) sync() syncSource {
	d.declare(srcSync)
	return syncSource{f: d.f.syncFeed}
}

// local returns the handle to the last local git read. Local git is read by
// every tab and loads unconditionally, so asking for it declares nothing.
func (d *sourceSet) local() localSource { return localSource{f: d.f.localFeed} }

// tabModel is a tab that owns its own rows, cursor, keys and overlays
// (ADR-0026). The shell keeps everything every tab shares — size, focus,
// filter, status line, popups — and calls into the tab with it.
type tabModel interface {
	// sources is the declaration the tab's constructor recorded.
	sources() []source
	// cursorRef is the tab's list cursor, which the shell's shared list
	// machinery (moveCursor, clampCursor, renderList) moves and reads.
	cursorRef() *int
	// rows is the tab's full row list, before the shell's filter.
	rows(s *shell) []rowItem
	// modal reports an overlay of the tab's own that owns the keyboard and
	// the body until it closes.
	modal() bool
	// key offers the tab a key the shell's overlays and the row bindings did
	// not take; unhandled falls through to the shell's built-ins.
	key(s *shell, k string) (handled bool, cmd tea.Cmd)
	// body draws the tab into width×height.
	body(s *shell, width, height int) ([]string, []hitTarget)
	// footer replaces the keybar while the tab needs it to (an overlay's own
	// help line); ok false keeps the keybar.
	footer() (line string, ok bool)
	// count is the tab-bar badge.
	count() int
	// loaded reports whether the tab has what it draws.
	loaded() bool
	// freshness is the header's age readout for the tab's data.
	freshness() seg
	// update takes the messages the shell hands its tabs (updateTabs).
	update(s *shell, msg tea.Msg) tea.Cmd
}

// syncSource is a tab's read handle on the GitHub sync.
type syncSource struct{ f *syncFeed }

// issues is every open issue the last sync answered.
func (h syncSource) issues() []gh.IssueItem { return h.f.remote.Issues }

// viewerLogin is the signed-in GitHub account's login, or "".
func (h syncSource) viewerLogin() string { return h.f.remote.ViewerLogin }

// prs is every open PR the last sync answered.
func (h syncSource) prs() []gh.OpenPR { return h.f.remote.PRs }

// prsFetchedAt is when the PRs section was last fetched cleanly.
func (h syncSource) prsFetchedAt() time.Time { return h.f.remote.PRsFetchedAt }

// issuesFetchedAt is when the issues section was last fetched cleanly.
func (h syncSource) issuesFetchedAt() time.Time { return h.f.remote.IssuesFetchedAt }

// issuesFetchError is why the issues section's last fetch failed, or "".
func (h syncSource) issuesFetchError() string { return h.f.remote.IssuesFetchError }

// loaded reports whether any sync or cache read has answered.
func (h syncSource) loaded() bool { return h.f.haveData }

// localSource is a tab's read handle on the local git read.
type localSource struct{ f *localFeed }

// branches is every local branch.
func (h localSource) branches() []git.BranchInfo { return h.f.local.branches }

// branchUnsafe reports a branch with unpushed commits.
func (h localSource) branchUnsafe(name string) bool { return h.f.local.branchUnsafe[name] }

// worktrees is every worktree of the repository.
func (h localSource) worktrees() []git.Worktree { return h.f.local.wts }

// wtDirty reports a worktree whose working tree is dirty.
func (h localSource) wtDirty(path string) bool { return h.f.local.wtState[path].dirty }

// beadsSource is a tab's read handle on the beads feed. Its methods are the
// whole of what a tab can learn about the beads workspace.
type beadsSource struct{ f *beadsFeed }

// list is every bead the last answered poll returned.
func (h beadsSource) list() []bd.Bead { return h.f.beads }

// prefix is the workspace prefix stripped off every displayed id.
func (h beadsSource) prefix() string { return h.f.prefix }

// loaded reports whether any poll has answered (or failed outright).
func (h beadsSource) loaded() bool { return h.f.have }

// err is the text the pane shows in place of rows, or "".
func (h beadsSource) err() string { return h.f.err }

// stale reports that the last poll was abandoned at beadsPollTimeout, so what
// list returns is the poll before it.
func (h beadsSource) stale() bool { return h.f.stale }

// poll asks for a fresh read now, unless one is already in flight.
func (h beadsSource) poll() tea.Cmd { return h.f.poll() }

// beadsFeed is the beads source: the `bd where` probe and, once it finds a
// workspace, the `bd list` poll, plus what they last answered.
type beadsFeed struct {
	ctx  context.Context
	root string

	// present is whether the `bd where` probe found a workspace; probed
	// records that it has answered — the launch sync waits for it, so a
	// beads repo's first sync goes out once the workspace is known. probing
	// is true from the moment the probe is dispatched until it answers,
	// closing the window where a manifest reload could start a second one.
	present bool
	probed  bool
	probing bool
	// polling is true from the moment loadBeadsCmd is dispatched until its
	// beadsMsg lands — the in-flight guard that keeps the tick, the launch
	// load and a manual `r` from stacking a second `bd list` on a slow one.
	polling bool
	// stale is set when a poll is abandoned at beadsPollTimeout rather than
	// answered: there is no real data to apply, so the last-known beads/err
	// are left on screen and only this flag changes.
	stale  bool
	prefix string
	beads  []bd.Bead
	have   bool
	err    string
}

// start dispatches the probe; its answer starts the poll. claimSources has
// already marked the probe in flight.
func (f *beadsFeed) start() tea.Cmd {
	return detectBeadsCmd(f.ctx, f.root)
}

// poll dispatches loadBeadsCmd, but only when no poll is already in flight —
// the single gate every beads read goes through (the launch load, the
// repeating tick, and the manual `r` refresh), so a slow or contended `bd`
// never accumulates more than one outstanding `bd list` process.
func (f *beadsFeed) poll() tea.Cmd {
	if f.polling {
		return nil
	}
	f.polling = true
	return loadBeadsCmd(f.ctx, f.root)
}

// update applies one of the feed's own messages and returns what it starts
// next. headless suppresses the re-arming ticks for a one-shot frame.
func (f *beadsFeed) update(msg tea.Msg, headless bool) tea.Cmd {
	switch msg := msg.(type) {
	case beadsDetectedMsg:
		// The probe answered: does a beads workspace actually exist here. This
		// only fires for a repo that already declared tracker = "beads", so the
		// Beads tab itself is never at stake — it's drawn on the declaration
		// alone. This decides what's inside it: real rows, or the "declared but
		// not found" state below.
		f.probed = true
		f.probing = false
		f.present = msg.present
		f.prefix = msg.ws.Prefix
		f.err = ""
		var cmds []tea.Cmd
		switch {
		case errors.Is(msg.err, context.DeadlineExceeded):
			// bd where never answered. Say so, and ask again: unlike a schema
			// mismatch, a wedged bd can recover on its own. present stays false
			// until a probe answers, so no poll or `r` refresh reads a
			// workspace whose prefix is still unknown.
			f.present = false
			f.err = beadsTimeoutText("where")
			cmds = append(cmds, beadsReprobeCmd(headless))
		case msg.err != nil:
			f.err = msg.err.Error()
		case !msg.present:
			f.err = `tracker = "beads" is declared, but no beads workspace was found here`
		}
		if f.present && f.err == "" {
			cmds = append(cmds, f.poll(), beadsTickCmd(headless))
		}
		return tea.Batch(cmds...)

	case beadsMsg:
		f.polling = false
		if !errors.Is(msg.err, context.DeadlineExceeded) {
			f.have = true
		}
		switch {
		case errors.Is(msg.err, context.DeadlineExceeded):
			// The poll was abandoned at beadsPollTimeout, not answered — bd.List's
			// exec.CommandContext killed the child, so msg carries no real data.
			// Keep the last-known beads/err untouched and only flag staleness: a
			// slow tick must never wipe good rows off the screen. With no rows
			// yet there is nothing to keep, so the pane names the timeout instead
			// of reading "loading…" forever; the next answer clears it.
			f.stale = true
			if !f.have {
				f.err = beadsTimeoutText("list")
			}
		case msg.err != nil:
			f.err = msg.err.Error()
		default:
			f.beads = msg.beads
			f.err = ""
			f.stale = false
		}
		return nil

	case beadsReprobeMsg:
		return detectBeadsCmd(f.ctx, f.root)

	case beadsTickMsg:
		// Beads live in a local database, so re-reading them costs a subprocess
		// and no network at all — nothing like the GitHub sync this dashboard
		// rations to one every five minutes. The tick is short enough that a
		// bead an agent creates mid-session shows up while you are still
		// looking at the pane.
		if !f.present {
			return nil
		}
		return tea.Batch(f.poll(), beadsTickCmd(headless))
	}
	return nil
}

// configFeed is the manifest as the shell last loaded it. loadManifest
// rewrites it on every read, failures included, so a manifest that was valid
// and is now broken clears what it used to declare.
type configFeed struct {
	manifest *manifest.Manifest
	ok       bool
	err      string
	// origin names which archetype (or "(manifest)") declared each top-level
	// key; a key missing from the map was never declared.
	origin map[string]string
	// cmdOrigin/actionOrigin name which archetype (or "(manifest)") last
	// contributed each command/action name — the Commands band's "archetype"
	// and each step's "from <archetype>" line. archetype.Resolve's own merge
	// log only records a COLLISION, not "here is who owns every key", so
	// these come from a second, read-only walk of the same search path
	// (computeOrigins).
	cmdOrigin    map[string]string
	actionOrigin map[string]string
}

// configSource is a tab's read handle on the loaded manifest.
type configSource struct{ f *configFeed }

// manifest is the merged manifest, nil when the project has none or it is
// broken.
func (h configSource) manifest() *manifest.Manifest { return h.f.manifest }

// ok reports whether the project has a manifest that resolved.
func (h configSource) ok() bool { return h.f.ok }

// err is a present-but-broken manifest's parse/resolve error, or "".
func (h configSource) err() string { return h.f.err }

// origin is who declared key, or "" when nothing did.
func (h configSource) origin(key string) string { return h.f.origin[key] }

// cmdOrigin is who declared command name, "(manifest)" when no walk saw it.
func (h configSource) cmdOrigin(name string) string {
	if o, ok := h.f.cmdOrigin[name]; ok {
		return o
	}
	return "(manifest)"
}

// actionOrigin is who declared action name, "(manifest)" when no walk saw it.
func (h configSource) actionOrigin(name string) string {
	if o := h.f.actionOrigin[name]; o != "" {
		return o
	}
	return "(manifest)"
}

// trackers is the manifest's declared tracker list, nil when it declares none.
func (h configSource) trackers() []string { return manifestTrackers(h.f.manifest) }

// workFeed is what the work source keeps beside the live-work registry: the
// empty Workers tab's hint, which names the dispatches this project's
// manifest declares and so can only be worded by the Model that holds the
// commands and key bindings. loadManifest rewrites it on every read.
type workFeed struct {
	emptyHint string
}

// workSource is a tab's read handle on the work source.
type workSource struct{ f *workFeed }

// workers is every live agent worker, in the registry's insertion order.
func (h workSource) workers() []*livework.Entry { return liveWorkers() }

// emptyHint is the line the Workers tab shows when no worker is running.
func (h workSource) emptyHint() string { return h.f.emptyHint }
