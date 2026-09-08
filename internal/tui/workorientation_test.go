// workorientation_test.go covers the Work tab's orientation switch: on a
// tall, narrow terminal renderWorkBody stacks the row list on top of a
// full-width detail pane rather than truncating the detail to a ~30-column
// side column (the bug a live-tail line like "command-test:cou…" made
// visible); on a wide terminal the pre-existing side-by-side layout is
// untouched. Ported from the design prototype's workBody/renderPaletteScreen
// (docs/spikes/admin-tui-desktop/variants.go) — see renderWorkBody's own doc
// comment for the geometry this mirrors.
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// firstPlainRuleRow finds the stacked layout's own list/detail divider — a
// row of nothing but "─" — as distinct from the palette box's top/bottom
// border rows (admin-3rgp.72.2), which also carry runs of "─" but mixed with
// "┌┐│└┘" box-drawing characters a plain rule never contains.
func firstPlainRuleRow(lines []string) int {
	for i, l := range lines {
		s := strings.TrimSpace(stripANSI(l))
		if s == "" {
			continue
		}
		allDash := true
		for _, r := range s {
			if r != '─' {
				allDash = false
				break
			}
		}
		if allDash {
			return i
		}
	}
	return -1
}

// buildRunningWorkModel is this file's shared fixture: one live agent
// worker whose Output ring holds real pty-shaped bytes (ONLCR "\r\n" line
// endings, exactly what internal/kinds/hostedpty.go actually drains off a
// child's master fd — internal/tui.TestProbeRealPtyBytes confirmed this
// shape empirically during development), cursor parked on that row so
// renderWorkBody's detail pane is the live tail.
//
// A genuine worker, not a dispatched command, is what this file's geometry
// assertions (stacked vs. side-by-side, the output block's rules) actually
// need — they exercise renderWorkBody/renderWorkDetail/appendLiveTail's
// LAYOUT, which is identical for either row kind. A dispatched command
// itself has no row to park the cursor on any more (admin-3rgp.72.2 retired
// the flat Commands list, and giving a live command a row of its own again
// — the design's "Running" section — is admin-3rgp.72.4's job, not this
// file's).
func buildRunningWorkModel(t *testing.T, w, h int) Model {
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
		Worker: &livework.WorkerInfo{Branch: "command-test", Worktree: "/tmp/command-test"},
	}
	if err := livework.Default.Add(entry); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rows := mm.workRows()
	for i, r := range rows {
		if r.kind == rowWorker && r.work.worker.ID == entry.ID {
			mm.cursor[tabWork] = i
		}
	}
	return mm
}

// TestRenderWorkBodyStacksOnTallNarrowTerminal is the main ticket: at
// 60x100 (float64(60)/2.3 ≈ 26.1 < 100), the row list sits on top, a
// full-width rule follows it, and the live tail — no longer squeezed into a
// ~20-column side column — renders at full width with none of its lines
// truncated to a stub like "command-test:cou…".
func TestRenderWorkBodyStacksOnTallNarrowTerminal(t *testing.T) {
	const w, h = 60, 100
	mm := buildRunningWorkModel(t, w, h)

	out, hits := mm.renderWorkBody(w, h)
	if len(out) != h {
		t.Fatalf("renderWorkBody produced %d lines, want exactly %d", len(out), h)
	}
	if len(hits) != h {
		t.Fatalf("renderWorkBody produced %d hit targets, want exactly %d", len(hits), h)
	}

	ruleIdx := firstPlainRuleRow(out)
	if ruleIdx == -1 {
		t.Fatalf("no full-width rule found in stacked output:\n%s", strings.Join(out, "\n"))
	}

	// The list is never handed more than half the body height.
	if ruleIdx > h/2 {
		t.Errorf("rule at row %d, want at or before the midpoint (row %d) — the list got more than half the body", ruleIdx, h/2)
	}

	// The row itself (not truncated to a ~30-column stub) is somewhere above
	// the rule.
	foundRow := false
	for _, l := range out[:ruleIdx] {
		if strings.Contains(l, "command-test") {
			foundRow = true
		}
	}
	if !foundRow {
		t.Errorf("command-test's own row not found above the rule:\n%s", strings.Join(out[:ruleIdx], "\n"))
	}

	// The tail lines render below the rule, at full width, complete — not
	// truncated the way a ~30-column side column forced them to be.
	below := strings.Join(out[ruleIdx+1:], "\n")
	for _, want := range []string{"collecting tests", "test_a ... ok", "test_b ... ok"} {
		if !strings.Contains(below, want) {
			t.Errorf("stacked detail band missing tail line %q:\n%s", want, below)
		}
	}
}

// TestRenderWorkBodyWideStaysSideBySide is the ticket's own "when bottomOrient
// is false, the layout must be byte-for-byte what it is today": a wide
// terminal (200x50 — float64(200)/2.3 ≈ 87 > 50) keeps the row list and
// detail pane side by side, joined by the existing "│" divider, with no rule
// row and no stacked geometry.
func TestRenderWorkBodyWideStaysSideBySide(t *testing.T) {
	const w, h = 200, 50
	mm := buildRunningWorkModel(t, w, h)

	out, hits := mm.renderWorkBody(w, h)
	if len(out) != h {
		t.Fatalf("renderWorkBody produced %d lines, want exactly %d", len(out), h)
	}
	if len(hits) != h {
		t.Fatalf("renderWorkBody produced %d hit targets, want exactly %d", len(hits), h)
	}

	// A rule row can legitimately appear here now — the detail pane's own
	// live-tail block (appendLiveTail) draws one — but only alongside the
	// side-by-side divider. The stacked-layout separator this guards against
	// is a bare full-width rule with no divider at all.
	for _, l := range out {
		if strings.Contains(l, "───") && !strings.Contains(l, "│") {
			t.Fatalf("found a stacked-layout rule (no side-by-side divider) in a wide render:\n%s", strings.Join(out, "\n"))
		}
	}
	dividerRows := 0
	for _, l := range out {
		if strings.Contains(l, "│") {
			dividerRows++
		}
	}
	if dividerRows != h {
		t.Errorf("%d of %d rows carried the side-by-side divider, want every row", dividerRows, h)
	}

	joined := strings.Join(out, "\n")
	for _, want := range []string{"collecting tests", "test_a ... ok", "test_b ... ok"} {
		if !strings.Contains(joined, want) {
			t.Errorf("side-by-side detail column missing tail line %q:\n%s", want, joined)
		}
	}
}

// TestRenderWorkBodyStackedDetailSpansFullWidth is the full-width-band
// regression: at 140 wide (well over the old 100-column cap on previewW),
// every rendered row of the stacked layout — list rows, the list/detail
// divider, and every row of the detail band including the output block's
// two rules — is exactly the body width, w. The visible content inside a
// detail-band row (the leading space plus lw columns of dashes/text) still
// stops one column short of the right edge — the "one column left blank"
// margin — with the padding out to w making that blank explicit rather
// than leaving it to whatever painted the row before.
func TestRenderWorkBodyStackedDetailSpansFullWidth(t *testing.T) {
	const w, h = 140, 100
	mm := buildRunningWorkModel(t, w, h)

	out, _ := mm.renderWorkBody(w, h)
	if len(out) != h {
		t.Fatalf("renderWorkBody produced %d lines, want exactly %d", len(out), h)
	}

	for i, l := range out {
		if got := vlen(l); got != w {
			t.Fatalf("row %d: rendered width %d, want exactly %d (body width) — line %q", i, got, w, l)
		}
	}

	dividerIdx := firstPlainRuleRow(out)
	if dividerIdx == -1 {
		t.Fatalf("no list/detail divider found in stacked output:\n%s", strings.Join(out, "\n"))
	}

	// The output block's two rules (opening "─── output ──…──" and plain
	// closing rule) render below the divider, each with visible dashes
	// reaching to within one column of the right edge (w-1, 0-indexed —
	// the trailing blank margin column) rather than the old cap's
	// fifteen-odd columns short.
	ruleCount := 0
	for _, l := range out[dividerIdx+1:] {
		if !strings.Contains(l, "───") {
			continue
		}
		ruleCount++
		trimmed := strings.TrimRight(l, " ")
		if got := vlen(trimmed); got < w-1 {
			t.Errorf("output rule reaches only column %d, want at least column %d (one short of the %d-wide body): %q", got, w-1, w, l)
		}
	}
	if ruleCount < 2 {
		t.Fatalf("expected the output block's opening and closing rules below the divider, found %d rule rows:\n%s", ruleCount, strings.Join(out[dividerIdx+1:], "\n"))
	}
}

// TestRingTailLinesNoBlankRowBetweenLines is the third defect: a hosted
// pty's raw bytes carry ONLCR "\r\n" line endings, and splitting on "\n"
// alone left a stray "\r" glued to the end of every line — which bubbletea's
// own line-diffing renderer turns into a visibly blank row underneath every
// real line of tail output (see ringTailLines' own doc comment for the
// mechanism). Every rendered line must be exactly its plain content, with no
// blank entries interleaved and no trailing "\r" surviving onto any of them.
func TestRingTailLinesNoBlankRowBetweenLines(t *testing.T) {
	r := livework.NewRing(4096)
	r.Write([]byte("line one\r\nline two\r\nline three\r\n"))

	lines := ringTailLines(r, 40)
	want := []string{"line one", "line two", "line three"}
	if len(lines) != len(want) {
		t.Fatalf("ringTailLines = %q (%d lines), want %q (%d lines) — no blank row between each",
			lines, len(lines), want, len(want))
	}
	for i, l := range lines {
		if l != want[i] {
			t.Errorf("line %d = %q, want %q", i, l, want[i])
		}
		if strings.Contains(l, "\r") {
			t.Errorf("line %d = %q retained a raw \\r", i, l)
		}
	}
}

// TestWorkerDetailNoBlankRowBetweenTailLines is the same defect proven
// through the full pane assembly (appendLiveTail), not just ringTailLines in
// isolation — the shape a real live pane actually renders.
func TestWorkerDetailNoBlankRowBetweenTailLines(t *testing.T) {
	freshLivework(t)
	ring := livework.NewRing(4096)
	ring.Write([]byte("alpha\r\nbeta\r\ngamma\r\n"))
	e := &livework.Entry{
		ID: "test-crlf-worker", Name: "test-crlf-worker",
		State: livework.StateRunning, StartedAt: time.Now(), Output: ring,
	}

	const h = 20
	out := workerDetail(e, 40, h)
	// Bottom-aligned against the closing rule (the pane's own final row),
	// not the pane's last row itself.
	if out[h-2] != "gamma" || out[h-3] != "beta" || out[h-4] != "alpha" {
		t.Fatalf("expected the three tail lines with no blank rows between them, got %q", out[h-4:h-1])
	}
	if !strings.Contains(out[h-1], "─") {
		t.Fatalf("last row = %q, want the output block's closing rule", out[h-1])
	}
}
