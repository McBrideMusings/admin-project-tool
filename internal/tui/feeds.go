package tui

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
)

// feeds holds the state of every data source a tab model reads through a
// handle (ADR-0026). The shell owns each feed: it starts the feed, routes the
// feed's messages to it, and hands a typed handle to every tab whose
// constructor asks for one. Each feed is a pointer so that every holder of a
// handle sees the one copy the shell keeps current, across the Model copies
// Bubble Tea makes on every Update.
type feeds struct {
	beads *beadsFeed
}

// newFeeds builds every feed, idle. A feed starts only when sources.go's
// claimSources finds a mounted tab declaring it.
func newFeeds(ctx context.Context, repoRoot string) feeds {
	return feeds{beads: &beadsFeed{ctx: ctx, root: repoRoot}}
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
	// update takes every message the shell does not consume itself.
	update(msg tea.Msg) tea.Cmd
}

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
