// workbody_test.go covers admin-3rgp.72.15: renderWorkBody has one layout at
// every geometry — the row list fills the body width, and the live band
// spans that same full width beneath it, with the list giving up HEIGHT
// (never width) to the band. The prototype's own two-orientation switch
// (docs/spikes/admin-tui-desktop/variants.go) picked between a side-by-side
// split and a stacked layout by comparing width against height; this file
// proves the app now renders the single, unconditional shape at three
// geometries the prototype's own reference frame (docs/spikes/
// admin-tui-desktop/commands.txt) is graded against.
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// buildRunningWorkModelAt is workbody_test.go's own fixture: one declared
// command, backed by a live hosted entry whose Output ring holds real
// pty-shaped bytes, cursor parked on that row so renderWorkBody's band is
// the live tail — the same shape the (now-deleted) orientation switch's own
// fixture used, sized to whatever geometry a test wants.
func buildRunningWorkModelAt(t *testing.T, w, h int) Model {
	t.Helper()
	freshLivework(t)
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"command-test": {Desc: "run the test suite", Steps: []string{"command-test"}},
		},
		Actions: map[string]manifest.Action{"command-test": {"kind": "shell"}},
	}
	mm := workTestModel(m)
	mm.active = tabWork
	mm.focus = focusBody
	mm.width, mm.height = w, h

	ring := livework.NewRing(4096)
	ring.Write([]byte("collecting tests\r\ntest_a ... ok\r\ntest_b ... ok\r\n"))
	entry := &livework.Entry{
		ID: "command-test", Name: "command-test", Handle: "test-command-test",
		State: livework.StateRunning, StartedAt: time.Now(), Output: ring,
	}
	if err := livework.Default.Add(entry); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rows := mm.workRows()
	for i, r := range rows {
		if r.kind == rowCommand && r.work.cmdName == "command-test" {
			mm.cursor[tabWork] = i
		}
	}
	return mm
}

// TestWorkBodyOneLayoutAtEveryGeometry is the ticket's core claim: at 80x24
// (narrow), 96x30 (the reference frame's own geometry) and 178x46 (wide),
// renderWorkBody produces the identical shape — list on top spanning the
// full body width, live band beneath it spanning that same width. No
// side-by-side divider ("│") ever appears, and every rendered row is
// exactly width cells wide. buildRunningWorkModelAt's fixture is a single
// row, so the band here is the SHORT-LIST case (admin-dky7): it grows past
// bandH(height) to take every row the one-row list doesn't need, rather than
// being clipped to bandH's floor — see TestWorkBodyShortListGrowsBandPastFloor
// for that behavior asserted directly against renderWorkBody's own listH/bh
// split.
func TestWorkBodyOneLayoutAtEveryGeometry(t *testing.T) {
	for _, geo := range []struct{ w, h int }{{80, 24}, {96, 30}, {178, 46}} {
		mm := buildRunningWorkModelAt(t, geo.w, geo.h)

		out, hits := mm.renderWorkBody(geo.w, geo.h)
		if len(out) != geo.h {
			t.Fatalf("%dx%d: renderWorkBody produced %d lines, want exactly %d", geo.w, geo.h, len(out), geo.h)
		}
		if len(hits) != geo.h {
			t.Fatalf("%dx%d: renderWorkBody produced %d hit targets, want exactly %d", geo.w, geo.h, len(hits), geo.h)
		}

		// No row ever carries the old side-by-side divider — there is no
		// branch left that could draw one.
		for i, l := range out {
			if strings.Contains(l, "│") {
				t.Errorf("%dx%d: row %d carries a side-by-side divider %q, want none — one layout, no split", geo.w, geo.h, i, l)
			}
		}

		// Every row spans exactly the body width — list rows and band rows
		// alike, proving the band is full width, not a right-hand column.
		for i, l := range out {
			if got := vlen(l); got != geo.w {
				t.Errorf("%dx%d: row %d rendered width %d, want exactly %d (body width): %q", geo.w, geo.h, i, got, geo.w, l)
			}
		}

		// The list is 2 rows (the "Commands" section header, then the sole
		// command-test row); every row below that belongs to the band.
		joined := strings.Join(out, "\n")
		if !strings.Contains(joined, "command-test") {
			t.Fatalf("%dx%d: command-test's own row not found anywhere in the body:\n%s", geo.w, geo.h, joined)
		}
		if !strings.Contains(out[1], "command-test") {
			t.Errorf("%dx%d: row 1 = %q, want the list's sole command-test row", geo.w, geo.h, out[1])
		}

		// The live tail — the band's own content — renders bottom-aligned in
		// the band (rows 2..end), at full width, not squeezed into a side
		// column.
		below := strings.Join(out[2:], "\n")
		for _, want := range []string{"collecting tests", "test_a ... ok", "test_b ... ok"} {
			if !strings.Contains(below, want) {
				t.Errorf("%dx%d: band (rows 2..%d) missing live tail line %q:\n%s", geo.w, geo.h, geo.h-1, want, below)
			}
		}
	}
}

// TestWorkBodyShortListGrowsBandPastFloor is admin-dky7's own acceptance
// criterion: a list short enough to fit in fewer rows than bandH(height)
// lets the band take every spare row rather than being clipped to bandH's
// floor and leaving blank rows above it. buildRunningWorkModelAt's fixture
// (one command, no workers) produces 5 real list rows at 96x30 — Commands
// header, the command-test row, a blank section separator, the Workers
// header, and its own "no workers" text — well short of bandH(30)=13.
func TestWorkBodyShortListGrowsBandPastFloor(t *testing.T) {
	const w, h = 96, 30
	mm := buildRunningWorkModelAt(t, w, h)
	rows := mm.workRows()
	wantListH := len(rows)

	out, _ := mm.renderWorkBody(w, h)
	if len(out) != h {
		t.Fatalf("renderWorkBody produced %d lines, want exactly %d", len(out), h)
	}

	if got := bandH(h); got >= h-wantListH {
		t.Fatalf("test fixture invalid: bandH(%d)=%d leaves no spare rows over a %d-row list to prove growth with", h, got, wantListH)
	}

	// The band's own title rule ("── ...") must start immediately after the
	// real list content (row wantListH), not at h-bandH(h), which would pad
	// the rows in between with blank list filler instead of handing them to
	// the band.
	titleIdx := -1
	for i, l := range out {
		if strings.Contains(l, "── ") && strings.Contains(stripANSI(l), "command-test") {
			titleIdx = i
			break
		}
	}
	if titleIdx != wantListH {
		t.Errorf("band title rule found at row %d, want exactly %d (immediately below the %d-row list, not padded out to h-bandH(h)=%d)",
			titleIdx, wantListH, wantListH, h-bandH(h))
	}
}
