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
	m.hasBeads = true

	if m.beadsPolling {
		t.Fatalf("fixture should start with no poll in flight")
	}
	first := m.pollBeadsCmd()
	if first == nil {
		t.Fatalf("first call: want a dispatched cmd, got nil")
	}
	if !m.beadsPolling {
		t.Fatalf("first call: want beadsPolling=true after dispatch")
	}

	second := m.pollBeadsCmd()
	if second != nil {
		t.Fatalf("second call while one is in flight: want nil (skip the tick), got a cmd")
	}

	// Once the in-flight poll's answer lands, the guard must release so the
	// next tick isn't skipped forever.
	next, _ := m.Update(beadsMsg{beads: []bd.Bead{{ID: "x-1"}}})
	nm := next.(Model)
	if nm.beadsPolling {
		t.Fatalf("beadsMsg landed: want beadsPolling=false, still true")
	}
	third := nm.pollBeadsCmd()
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
	m.hasBeads = true
	m.beads = []bd.Bead{{ID: "keep-1"}, {ID: "keep-2"}}
	m.haveBeads = true
	m.beadsErr = ""
	m.beadsPolling = true // a poll is in flight when the timeout fires

	next, _ := m.Update(beadsMsg{err: context.DeadlineExceeded})
	nm := next.(Model)

	if nm.beadsPolling {
		t.Errorf("timeout: want beadsPolling cleared so the next tick isn't skipped, still true")
	}
	if !nm.beadsStale {
		t.Errorf("timeout: want beadsStale=true so the header shows the abandoned poll")
	}
	if len(nm.beads) != 2 || nm.beads[0].ID != "keep-1" || nm.beads[1].ID != "keep-2" {
		t.Errorf("timeout: want last-known beads left untouched, got %+v", nm.beads)
	}
	if nm.beadsErr != "" {
		t.Errorf("timeout: want no error surfaced (abandoned, not a real bd error), got %q", nm.beadsErr)
	}

	// A subsequent successful poll must clear the stale flag.
	next2, _ := m.Update(beadsMsg{beads: []bd.Bead{{ID: "fresh-1"}}})
	nm2 := next2.(Model)
	if nm2.beadsStale {
		t.Errorf("fresh poll after a timeout: want beadsStale cleared, still true")
	}
	if len(nm2.beads) != 1 || nm2.beads[0].ID != "fresh-1" {
		t.Errorf("fresh poll after a timeout: want the new rows applied, got %+v", nm2.beads)
	}
}

// TestBeadsTimeoutOnFirstLoadNamesTheTimeout covers admin-3rgp.56: with no rows
// yet, an abandoned poll replaces "loading…" with a timeout message, and the next
// answer clears it.
func TestBeadsTimeoutOnFirstLoadNamesTheTimeout(t *testing.T) {
	m := seededModel()
	m.hasBeads = true
	m.haveBeads = false
	m.beadsPolling = true

	next, _ := m.Update(beadsMsg{err: context.DeadlineExceeded})
	nm := next.(Model)
	rows := nm.beadRows()
	if len(rows) != 1 || rows[0].label != beadsTimeoutText("list") {
		t.Fatalf("first-load timeout: want the one timeout row, got %+v", rows)
	}

	next2, _ := nm.Update(beadsMsg{beads: []bd.Bead{{ID: "fresh-1"}}})
	if got := next2.(Model).beadsErr; got != "" {
		t.Errorf("answer after a timeout: want the message cleared, got %q", got)
	}
}

// TestBeadsDetectTimeoutReprobes covers admin-3rgp.56's probe half: a `bd where`
// that never answers shows the timeout, starts no poll, and asks again.
func TestBeadsDetectTimeoutReprobes(t *testing.T) {
	m := seededModel()
	m.beadsProbing = true

	next, cmd := m.Update(beadsDetectedMsg{present: true, err: context.DeadlineExceeded})
	nm := next.(Model)
	if nm.beadsErr != beadsTimeoutText("where") {
		t.Errorf("detect timeout: want the timeout message, got %q", nm.beadsErr)
	}
	if nm.hasBeads {
		t.Errorf("detect timeout: want hasBeads=false so no poll reads an unprobed workspace")
	}
	if nm.beadsPolling {
		t.Errorf("detect timeout: want no bd list dispatched")
	}
	if cmd == nil {
		t.Fatalf("detect timeout: want a re-probe scheduled, got no cmd")
	}

	next2, _ := nm.Update(beadsDetectedMsg{present: true, ws: bd.Workspace{Prefix: "x"}})
	nm2 := next2.(Model)
	if nm2.beadsErr != "" || !nm2.hasBeads || !nm2.beadsPolling {
		t.Errorf("probe answers after a timeout: want error cleared and a poll started, got err=%q hasBeads=%v polling=%v",
			nm2.beadsErr, nm2.hasBeads, nm2.beadsPolling)
	}
}

// TestBeadsFreshnessSegReflectsStale covers the header readout the stale
// flag exists to drive: freshnessSeg must say "stale", not "live", while a
// poll has been abandoned.
func TestBeadsFreshnessSegReflectsStale(t *testing.T) {
	m := seededModel()
	m.hasBeads = true
	m.active = tabBeads

	live := m.freshnessSeg()
	if live.t != "live" {
		t.Errorf("no timeout yet: want freshnessSeg().t = %q, got %q", "live", live.t)
	}

	m.beadsStale = true
	stale := m.freshnessSeg()
	if stale.t != "stale" {
		t.Errorf("after a timeout: want freshnessSeg().t = %q, got %q", "stale", stale.t)
	}
}
