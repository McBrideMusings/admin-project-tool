package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/data"
)

// source is one background feed the dashboard runs on behalf of its tabs
// (ADR-0026). A tab with a model of its own declares the sources it reads by
// asking its sourceSet for their handles (feeds.go); a tab whose state still
// lives on Model declares them in tabSources. The shell starts each source
// once, for every mounted tab that declares it, and never starts one that no
// mounted tab declares. Local git and the merged config are
// not sources: every tab reads them, so they load unconditionally in Init.
type source int

const (
	// srcSync is the whole-repo GitHub sync: the on-disk cache read, the poll
	// tick, and every sync it fires (open PRs, open issues, the merged-PR
	// head set, `git fetch --prune`).
	srcSync source = iota
	// srcBeads is the `bd where` probe and, once it finds a workspace, the
	// `bd list` poll.
	srcBeads
	// srcWork is the 1s work tick: the shared live-work sync, the [status]
	// probes, the liveness probe, the reaper and the pane tail.
	srcWork
	// srcIdentity is the signed-in GitHub account (`gh api user`), re-read
	// on every sync tick.
	srcIdentity
	numSources
)

// tabSources is the declaration of the sources each tab reads, for the tabs
// whose state still lives on Model. The Repo tab reads the sync (the
// merged-PR head set), beads (the `w` naming prompt's bead search) and
// identity (which branches are yours). A tab that has moved into its own model
// has no row here: its constructor's handles are its declaration
// (Model.declared).
var tabSources = map[tab][]source{
	tabLocal: {srcSync, srcBeads, srcIdentity},
}

// declared is the sources tab t reads: its model's declaration, or its
// tabSources row while it has no model.
func (m Model) declared(t tab) []source {
	if tm := m.tabModel(t); tm != nil {
		return tm.sources()
	}
	return tabSources[t]
}

// dataSource is one row of the registry: whether the project has what the
// source reads, and the commands that start it.
type dataSource struct {
	name string
	// ready reports whether the project has what the source reads — a
	// manifest for the work tick, a declared beads tracker for the probe.
	// A source a mounted tab declares but the project cannot feed stays
	// off until a manifest reload makes it ready.
	ready func(m Model) bool
	// start returns the commands that begin the source. Each one re-arms
	// itself from its own message case in Update, so start runs once.
	start func(m Model) tea.Cmd
}

var sources = [numSources]dataSource{
	srcSync: {
		name:  "sync",
		ready: func(Model) bool { return true },
		// The cache read paints the first frame off disk; the tick is the
		// poll. The launch sync itself waits for local git (launchSyncCmd).
		start: func(m Model) tea.Cmd { return tea.Batch(loadCacheCmd(m.nwo), m.syncTickCmd()) },
	},
	srcBeads: {
		name: "beads",
		// The `bd where` probe only runs for a repo that declared "beads"
		// among its trackers — its answer feeds the tab's content (which bead
		// workspace, or an error if none is found), never whether the tab is
		// drawn at all.
		ready: func(m Model) bool { return m.hasTracker("beads") },
		start: func(m Model) tea.Cmd { return m.feeds.beads.start() },
	},
	srcWork: {
		name:  "work",
		ready: func(m Model) bool { return m.manifestOK },
		start: func(m Model) tea.Cmd {
			// Start the [status] checks at launch so the first frame is not a
			// second of empty readouts; later refreshes ride workTickMsg.
			if m.manifest != nil {
				statusProbes.Refresh(m.manifest.Status, m.manifest.Root)
			}
			// A herdr pane or agent worker changes state (running → waiting →
			// failed, a new Activity line) from goroutines this process never
			// hears from otherwise — livework.Default has no notification of
			// its own. This tick is the Work tab's only wake source for that.
			return tea.Batch(m.workTickCmd(), syncLiveWorkCmd(m.repoRoot))
		},
	},
	srcIdentity: {
		name:  "identity",
		ready: func(Model) bool { return true },
		start: func(m Model) tea.Cmd { return fetchIdentityCmd(m.ctx) },
	},
}

// wants reports whether any mounted, drawn tab declares s.
func (m Model) wants(s source) bool {
	for _, t := range m.visibleTabs() {
		for _, v := range m.declared(t) {
			if v == s {
				return true
			}
		}
	}
	return false
}

// sourceOn reports whether s should be running: a mounted tab declares it and
// the project has what it reads.
func (m Model) sourceOn(s source) bool {
	return m.wants(s) && sources[s].ready(m)
}

// claimSources marks every source that is on and not yet started as started,
// and returns them. New claims the sources Init starts; a manifest reload
// claims any source the new manifest made ready.
func (m *Model) claimSources() []source {
	var due []source
	for s := source(0); s < numSources; s++ {
		if m.started[s] || !m.sourceOn(s) {
			continue
		}
		m.started[s] = true
		if s == srcBeads {
			m.feeds.beads.probing = true
		}
		due = append(due, s)
	}
	return due
}

// startedSources is every source claimed so far, in registry order.
func (m Model) startedSources() []source {
	var out []source
	for s := source(0); s < numSources; s++ {
		if m.started[s] {
			out = append(out, s)
		}
	}
	return out
}

// sourceCmds returns the start commands for ss and writes one sync-log line
// per source naming it and the mounted tabs that declare it.
func (m Model) sourceCmds(ss []source) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(ss))
	for _, s := range ss {
		data.LogSync(m.nwo, "source", 0, "start="+sources[s].name+" for="+m.declarers(s), nil)
		cmds = append(cmds, sources[s].start(m))
	}
	return tea.Batch(cmds...)
}

// declarers names the mounted, drawn tabs that declare s, comma-separated.
func (m Model) declarers(s source) string {
	var names []string
	for _, t := range m.visibleTabs() {
		for _, v := range m.declared(t) {
			if v == s {
				names = append(names, strings.ToLower(tabDisplay(t)))
			}
		}
	}
	return strings.Join(names, ",")
}
