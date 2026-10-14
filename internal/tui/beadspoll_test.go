package tui

import (
	"context"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
)

// TestPollBeadsCmdGuardsInFlight covers admin-fy79.1's first acceptance
// criterion: a slow or contended bd never accumulates more than one in-flight
// poll. pollBeadsCmd is the single gate the tick, the launch load, and the
// manual `r` refresh all go through — the guard has to live here, once, or
// one of those three callers could still stack a second `bd list` on top of
// a running one.
func TestPollBeadsCmdGuardsInFlight(t *testing.T) {
	m := seededModel()
	m.feeds.beads.present = true

	if m.feeds.beads.polling {
		t.Fatalf("fixture should start with no poll in flight")
	}
	first := m.feeds.beads.poll()
	if first == nil {
		t.Fatalf("first call: want a dispatched cmd, got nil")
	}
	if !m.feeds.beads.polling {
		t.Fatalf("first call: want beadsPolling=true after dispatch")
	}

	second := m.feeds.beads.poll()
	if second != nil {
		t.Fatalf("second call while one is in flight: want nil (skip the tick), got a cmd")
	}

	// Once the in-flight poll's answer lands, the guard must release so the
	// next tick isn't skipped forever.
	next, _ := m.Update(beadsMsg{beads: []bd.Bead{{ID: "x-1"}}})
	nm := next.(Model)
	if nm.feeds.beads.polling {
		t.Fatalf("beadsMsg landed: want beadsPolling=false, still true")
	}
	third := nm.feeds.beads.poll()
	if third == nil {
		t.Fatalf("after the guard releases: want a dispatched cmd, got nil")
	}
}

// TestBeadsMsgTimeoutKeepsLastKnownAndFlagsStale covers the second and third
// acceptance criteria: a poll abandoned at beadsPollTimeout must not wipe the
// last-known rows off the screen (that's what made the tracker read as
// unwritable — the fix is that a hung poll degrades to a stale indicator,
// not a blank or crashed dashboard), and it must clear the in-flight guard
// so the dashboard keeps polling rather than wedging forever.
func TestBeadsMsgTimeoutKeepsLastKnownAndFlagsStale(t *testing.T) {
	m := seededModel()
	m.feeds.beads.present = true
	m.feeds.beads.beads = []bd.Bead{{ID: "keep-1"}, {ID: "keep-2"}}
	m.feeds.beads.have = true
	m.feeds.beads.err = ""
	m.feeds.beads.polling = true // a poll is in flight when the timeout fires

	next, _ := m.Update(beadsMsg{err: context.DeadlineExceeded})
	nm := next.(Model)

	if nm.feeds.beads.polling {
		t.Errorf("timeout: want beadsPolling cleared so the next tick isn't skipped, still true")
	}
	if !nm.feeds.beads.stale {
		t.Errorf("timeout: want beadsStale=true so the header shows the abandoned poll")
	}
	if len(nm.feeds.beads.beads) != 2 || nm.feeds.beads.beads[0].ID != "keep-1" || nm.feeds.beads.beads[1].ID != "keep-2" {
		t.Errorf("timeout: want last-known beads left untouched, got %+v", nm.feeds.beads.beads)
	}
	if nm.feeds.beads.err != "" {
		t.Errorf("timeout: want no error surfaced (abandoned, not a real bd error), got %q", nm.feeds.beads.err)
	}

	// A subsequent successful poll must clear the stale flag.
	next2, _ := m.Update(beadsMsg{beads: []bd.Bead{{ID: "fresh-1"}}})
	nm2 := next2.(Model)
	if nm2.feeds.beads.stale {
		t.Errorf("fresh poll after a timeout: want beadsStale cleared, still true")
	}
	if len(nm2.feeds.beads.beads) != 1 || nm2.feeds.beads.beads[0].ID != "fresh-1" {
		t.Errorf("fresh poll after a timeout: want the new rows applied, got %+v", nm2.feeds.beads.beads)
	}
}

// TestBeadsTimeoutOnFirstLoadNamesTheTimeout covers admin-3rgp.56: with no rows
// yet, an abandoned poll replaces "loading…" with a timeout message, and the next
// answer clears it.
func TestBeadsTimeoutOnFirstLoadNamesTheTimeout(t *testing.T) {
	m := seededModel()
	m.feeds.beads.present = true
	m.feeds.beads.have = false
	m.feeds.beads.polling = true

	next, _ := m.Update(beadsMsg{err: context.DeadlineExceeded})
	nm := next.(Model)
	rows := nm.beadsTab.beadRows(&nm.shell)
	if len(rows) != 1 || rows[0].label != beadsTimeoutText("list") {
		t.Fatalf("first-load timeout: want the one timeout row, got %+v", rows)
	}

	next2, _ := nm.Update(beadsMsg{beads: []bd.Bead{{ID: "fresh-1"}}})
	if got := next2.(Model).feeds.beads.err; got != "" {
		t.Errorf("answer after a timeout: want the message cleared, got %q", got)
	}
}

// TestBeadsDetectTimeoutReprobes covers admin-3rgp.56's probe half: a `bd where`
// that never answers shows the timeout, starts no poll, and asks again.
func TestBeadsDetectTimeoutReprobes(t *testing.T) {
	m := seededModel()
	m.feeds.beads.probing = true

	next, cmd := m.Update(beadsDetectedMsg{present: true, err: context.DeadlineExceeded})
	nm := next.(Model)
	if nm.feeds.beads.err != beadsTimeoutText("where") {
		t.Errorf("detect timeout: want the timeout message, got %q", nm.feeds.beads.err)
	}
	if nm.feeds.beads.present {
		t.Errorf("detect timeout: want hasBeads=false so no poll reads an unprobed workspace")
	}
	if nm.feeds.beads.polling {
		t.Errorf("detect timeout: want no bd list dispatched")
	}
	if cmd == nil {
		t.Fatalf("detect timeout: want a re-probe scheduled, got no cmd")
	}

	next2, _ := nm.Update(beadsDetectedMsg{present: true, ws: bd.Workspace{Prefix: "x"}})
	nm2 := next2.(Model)
	if nm2.feeds.beads.err != "" || !nm2.feeds.beads.present || !nm2.feeds.beads.polling {
		t.Errorf("probe answers after a timeout: want error cleared and a poll started, got err=%q hasBeads=%v polling=%v",
			nm2.feeds.beads.err, nm2.feeds.beads.present, nm2.feeds.beads.polling)
	}
}

// TestBeadsFreshnessSegReflectsStale covers the header readout the stale
// flag exists to drive: freshnessSeg must say "stale", not "live", while a
// poll has been abandoned.
func TestBeadsFreshnessSegReflectsStale(t *testing.T) {
	m := seededModel()
	m.feeds.beads.present = true
	m.active = tabBeads

	live := m.freshnessSeg()
	if live.t != "live" {
		t.Errorf("no timeout yet: want freshnessSeg().t = %q, got %q", "live", live.t)
	}

	m.feeds.beads.stale = true
	stale := m.freshnessSeg()
	if stale.t != "stale" {
		t.Errorf("after a timeout: want freshnessSeg().t = %q, got %q", "stale", stale.t)
	}
}
