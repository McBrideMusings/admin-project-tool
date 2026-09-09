// workoutput_test.go covers the Work tab's live band output tail
// (admin-3rgp.72.6): a hosted entry's Output ring, once non-nil, fills the
// rest of the band below the fixed field block, bottom-aligned so a short
// tail leaves its gap above rather than below — with no "output" section
// header or rule, unlike the old right-hand detail pane this replaced (that
// framing lived in appendLiveTail/outputRule, both deleted with this
// ticket).
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// TestRenderWorkBandTailsLiveRingBottomAligned is the core acceptance
// criterion: a worker with a live Output ring shows its last lines
// bottom-aligned against the floor of the band, with the fixed field block
// (fieldRowsH rows) and the title rule above it untouched.
func TestRenderWorkBandTailsLiveRingBottomAligned(t *testing.T) {
	freshLivework(t)
	ring := livework.NewRing(4096)
	ring.Write([]byte("line one\nline two\nline three\n"))
	e := &livework.Entry{
		ID: "test-tail-worker", Name: "test-tail-worker",
		State: livework.StateRunning, StartedAt: time.Now(),
		Output: ring,
	}

	mm := workTestModel(nil)
	row := rowItem{kind: rowWorker, work: &workRowData{worker: e}}
	const h = 20
	out := mm.renderWorkBand(row, 60, h)
	if len(out) != h {
		t.Fatalf("renderWorkBand with a live ring produced %d lines, want exactly %d (fills the band)", len(out), h)
	}

	// Bottom-aligned against the band's own last row: the three ring lines
	// are the very last three rows, with no rule or header between the
	// field block and the tail.
	if strings.TrimSpace(out[h-1]) != "line three" || strings.TrimSpace(out[h-2]) != "line two" || strings.TrimSpace(out[h-3]) != "line one" {
		t.Fatalf("tail not bottom-aligned against the band's floor, rows h-3..h-1 = %q", out[h-3:])
	}
	// The field block occupies exactly 1 (title) + fieldRowsH rows above the
	// tail; everything between it and the tail must be blank.
	for i := 1 + fieldRowsH; i < h-3; i++ {
		if strings.TrimSpace(out[i]) != "" {
			t.Errorf("row %d between the field block and the tail = %q, want blank (the bottom-align gap)", i, out[i])
		}
	}
}

// TestRenderWorkBandEmptyTailWithoutLiveRing is the negative case: a worker
// with no Output ring (nil — e.g. dispatched through a [terminal] launcher
// rather than hosted in-process, admin-spjp.6's known capability gap)
// renders an empty tail: every row below the field block blank, still
// exactly h lines.
func TestRenderWorkBandEmptyTailWithoutLiveRing(t *testing.T) {
	freshLivework(t)
	e := &livework.Entry{
		ID: "test-no-tail-worker", Name: "test-no-tail-worker",
		State: livework.StateRunning, StartedAt: time.Now(),
		Via: "terminal", Handle: "pane-1",
	}

	mm := workTestModel(nil)
	row := rowItem{kind: rowWorker, work: &workRowData{worker: e}}
	const h = 20
	out := mm.renderWorkBand(row, 60, h)
	if len(out) != h {
		t.Fatalf("renderWorkBand produced %d lines, want exactly %d", len(out), h)
	}
	for i := 1 + fieldRowsH; i < h; i++ {
		if strings.TrimSpace(out[i]) != "" {
			t.Errorf("row %d with no live ring = %q, want blank", i, out[i])
		}
	}
}

// TestRenderWorkBandAnsiStrippedAndTruncated proves the tail is a raw peek:
// ANSI escapes are stripped (not re-colorized) and each line is truncated to
// the column width rather than wrapped or left overflowing.
func TestRenderWorkBandAnsiStrippedAndTruncated(t *testing.T) {
	freshLivework(t)
	ring := livework.NewRing(4096)
	ring.Write([]byte("\x1b[31mred error\x1b[0m\n" + strings.Repeat("x", 200) + "\n"))
	e := &livework.Entry{
		ID: "test-ansi-worker", Name: "test-ansi-worker",
		State: livework.StateRunning, StartedAt: time.Now(),
		Output: ring,
	}

	mm := workTestModel(nil)
	row := rowItem{kind: rowWorker, work: &workRowData{worker: e}}
	out := mm.renderWorkBand(row, 20, 20)
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "\x1b[") {
		t.Errorf("tail retained a raw ANSI escape:\n%q", joined)
	}
	if !strings.Contains(joined, "red error") {
		t.Errorf("tail lost the plain text under the stripped escape:\n%q", joined)
	}
	for _, l := range out {
		if len(l) > 20*4 { // generous byte ceiling; vlen is what's enforced
			t.Errorf("line %q looks untruncated for a width-20 band", l)
		}
	}
}

// TestRenderWorkBandCommandTailsLiveRing proves the same tail behavior on
// the live-command branch (a hosted command's own Output ring), not just
// the worker branch.
func TestRenderWorkBandCommandTailsLiveRing(t *testing.T) {
	freshLivework(t)
	ring := livework.NewRing(4096)
	ring.Write([]byte("building...\ndone\n"))
	wd := &workRowData{
		cmdName: "dev", cmdKind: "shell", cmdDesc: "start dev",
		live: &livework.Entry{ID: "dev-tail", StartedAt: time.Now(), Output: ring},
	}

	mm := workTestModel(nil)
	row := rowItem{kind: rowCommand, work: wd}
	const h = 25
	out := mm.renderWorkBand(row, 60, h)
	if len(out) != h {
		t.Fatalf("renderWorkBand with a live ring produced %d lines, want exactly %d", len(out), h)
	}
	if strings.TrimSpace(out[h-1]) != "done" || strings.TrimSpace(out[h-2]) != "building..." {
		t.Fatalf("tail not bottom-aligned against the band's floor, rows h-2..h-1 = %q", out[h-2:])
	}
}

// TestRenderWorkBandNoTailWhenIdle is the idle-command negative case: no
// live entry at all, so the band's own idle placeholder — not a tail —
// occupies the last row (bandIdle's own contract, covered by workband_test.
// go).
func TestRenderWorkBandNoTailWhenIdle(t *testing.T) {
	wd := &workRowData{cmdName: "build", cmdKind: "shell", cmdDesc: "build it"}
	mm := workTestModel(nil)
	row := rowItem{kind: rowCommand, work: wd}
	out := mm.renderWorkBand(row, 60, 40)
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "output appears here while it runs") {
		t.Errorf("idle band missing the placeholder line:\n%s", joined)
	}
}

// TestRingTailLinesNoBlankRowBetweenLines is the CRLF defect: a hosted
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

// TestRenderWorkBandNoBlankRowBetweenTailLines is the same defect proven
// through the full band assembly (renderWorkBand), not just ringTailLines in
// isolation — the shape a real live band actually renders.
func TestRenderWorkBandNoBlankRowBetweenTailLines(t *testing.T) {
	freshLivework(t)
	ring := livework.NewRing(4096)
	ring.Write([]byte("alpha\r\nbeta\r\ngamma\r\n"))
	e := &livework.Entry{
		ID: "test-crlf-worker", Name: "test-crlf-worker",
		State: livework.StateRunning, StartedAt: time.Now(), Output: ring,
	}

	mm := workTestModel(nil)
	row := rowItem{kind: rowWorker, work: &workRowData{worker: e}}
	const h = 20
	out := mm.renderWorkBand(row, 40, h)
	if strings.TrimSpace(out[h-1]) != "gamma" || strings.TrimSpace(out[h-2]) != "beta" || strings.TrimSpace(out[h-3]) != "alpha" {
		t.Fatalf("expected the three tail lines with no blank rows between them, got %q", out[h-3:])
	}
}
