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
// full body width, live band beneath it spanning that same width, with
// bandH(height) rows given to the band and the rest to the list. No
// side-by-side divider ("│") ever appears, and every rendered row is
// exactly width cells wide.
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

		// The band takes exactly bandH(height) rows off the bottom, fixed by
		// height alone — the row list gives up HEIGHT to the band and never
		// width.
		wantBandH := bandH(geo.h)
		wantListH := geo.h - wantBandH
		joined := strings.Join(out, "\n")
		if !strings.Contains(joined, "command-test") {
			t.Fatalf("%dx%d: command-test's own row not found anywhere in the body:\n%s", geo.w, geo.h, joined)
		}
		above := strings.Join(out[:wantListH], "\n")
		if !strings.Contains(above, "command-test") {
			t.Errorf("%dx%d: command-test's row not found in the list's %d rows above the band:\n%s", geo.w, geo.h, wantListH, above)
		}

		// The live tail — the band's own content — renders in exactly the
		// bottom bandH(height) rows, at full width, not squeezed into a
		// side column.
		below := strings.Join(out[wantListH:], "\n")
		for _, want := range []string{"collecting tests", "test_a ... ok", "test_b ... ok"} {
			if !strings.Contains(below, want) {
				t.Errorf("%dx%d: band (bottom %d rows) missing live tail line %q:\n%s", geo.w, geo.h, wantBandH, want, below)
			}
		}
	}
}
