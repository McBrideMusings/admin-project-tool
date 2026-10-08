// workbody_test.go covers admin-3rgp.72.15: renderWorkBody has one layout at
// every geometry — the row list fills the body width, and the live band
// spans that same full width beneath it, with the list giving up HEIGHT
// (never width) to the band. There is no side-by-side split chosen by
// comparing width against height; this file proves the app renders the
// single, unconditional shape at three geometries.
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// buildRunningWorkModelAt is workbody_test.go's own fixture: one declared
// command, backed by a live hosted entry whose Screen holds real
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

	screen := hostedScreen(t, "collecting tests\r\ntest_a ... ok\r\ntest_b ... ok\r\n")
	entry := &livework.Entry{
		ID: "command-test", Name: "command-test", Handle: "test-command-test",
		StartedAt: time.Now(), Screen: screen,
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

// TestWorkBodyBandRulesAreEdgeToEdge pins commands-running.txt:16 and :28's
// column: the band's title rule starts at column 0 and runs to the last
// column, the same span as the footer rule beneath it, instead of sitting one
// cell inside each edge.
func TestWorkBodyBandRulesAreEdgeToEdge(t *testing.T) {
	for _, geo := range []struct{ w, h int }{{80, 24}, {96, 30}, {178, 46}} {
		mm := buildRunningWorkModelAt(t, geo.w, geo.h)
		out, _ := mm.renderWorkBody(geo.w, geo.h)

		title := ""
		for _, l := range out {
			if p := stripANSI(l); strings.HasPrefix(p, "──") && strings.HasSuffix(p, "──") {
				title = p
				break
			}
		}
		if title == "" {
			t.Fatalf("%dx%d: no band title rule starting and ending in \"──\" at the screen edges:\n%s", geo.w, geo.h, strings.Join(out, "\n"))
		}
		if got := vlen(title); got != geo.w {
			t.Errorf("%dx%d: band title rule is %d cells wide, want %d", geo.w, geo.h, got, geo.w)
		}
	}
}

// TestWorkBodyBandStaysOpen proves the band is bandH rows at the bottom of
// the body whatever state the cursor row is in: running, waiting, exited or
// failed.
func TestWorkBodyBandStaysOpen(t *testing.T) {
	const w, h = 96, 30
	for _, state := range []livework.State{
		livework.StateRunning, livework.StateWaiting, livework.StateExited, livework.StateFailed,
	} {
		mm := buildRunningWorkModelAt(t, w, h)
		livework.Default.SetState("command-test", state, nil)

		out, _ := mm.renderWorkBody(w, h)
		titleIdx := -1
		for i, l := range out {
			if strings.Contains(l, "── ") && strings.Contains(stripANSI(l), "command-test") {
				titleIdx = i
				break
			}
		}
		if want := h - bandH(h); titleIdx != want {
			t.Errorf("%s: band title at row %d, want %d", state, titleIdx, want)
		}
	}
}

// TestWorkBodyBandIsFixedHeightAtBottom proves a short list does not grow
// the band: buildRunningWorkModelAt's fixture produces 5 list rows at 96x30,
// well short of the 30-bandH(30)=17 the list is given, and the band still
// starts at row 17 with blank rows between it and the list.
func TestWorkBodyBandIsFixedHeightAtBottom(t *testing.T) {
	const w, h = 96, 30
	mm := buildRunningWorkModelAt(t, w, h)
	rows := mm.workRows()
	wantListH := len(rows)

	out, _ := mm.renderWorkBody(w, h)
	if len(out) != h {
		t.Fatalf("renderWorkBody produced %d lines, want exactly %d", len(out), h)
	}

	wantTitle := h - bandH(h)
	if wantListH >= wantTitle {
		t.Fatalf("test fixture invalid: a %d-row list leaves no spare rows above row %d", wantListH, wantTitle)
	}

	titleIdx := -1
	for i, l := range out {
		if strings.Contains(l, "── ") && strings.Contains(stripANSI(l), "command-test") {
			titleIdx = i
			break
		}
	}
	if titleIdx != wantTitle {
		t.Errorf("band title rule found at row %d, want %d (h-bandH(h), independent of the %d-row list)", titleIdx, wantTitle, wantListH)
	}
	for i := wantListH; i < wantTitle; i++ {
		if strings.TrimSpace(stripANSI(out[i])) != "" {
			t.Errorf("row %d between the list and the band = %q, want blank", i, out[i])
		}
	}
}
