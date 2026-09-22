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

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// TestRenderWorkBandTailsLiveRingBottomAligned is the core acceptance
// criterion: a worker with a live Output ring shows its last lines
// bottom-aligned against the floor of the band, with the field block (a
// worker's own five real rows) and the title rule above it untouched.
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
	// The field block occupies exactly 1 (title) + 5 (a worker's own
	// log/branch/worktree/session/now rows) above the tail; everything
	// between it and the tail must be blank.
	for i := 1 + 5; i < h-3; i++ {
		if strings.TrimSpace(out[i]) != "" {
			t.Errorf("row %d between the field block and the tail = %q, want blank (the bottom-align gap)", i, out[i])
		}
	}
}

// TestRenderWorkBandRuleSeparatesFieldsFromTail proves the open band draws
// the attached view's dashed rule on the row right after its field rows —
// here a command's log row and one url row — with the tail below it.
func TestRenderWorkBandRuleSeparatesFieldsFromTail(t *testing.T) {
	freshLivework(t)
	ring := livework.NewRing(4096)
	ring.Write([]byte("tail line\n"))
	wd := &workRowData{
		cmdName: "dev", cmdKind: "shell",
		urls: []frame.URLEntry{{Label: "local", URL: "http://localhost:1"}},
		live: &livework.Entry{ID: "dev-rule", StartedAt: time.Now(), State: livework.StateRunning, Output: ring},
	}
	out := workTestModel(nil).renderWorkBand(rowItem{kind: rowCommand, work: wd}, 60, 12)
	if !strings.Contains(out[1], "log") || !strings.Contains(out[2], "url") {
		t.Fatalf("rows 1-2 = %q, want the log and url field rows", out[1:3])
	}
	if got := strings.TrimSpace(stripANSI(out[3])); got != strings.Repeat("╌", 60) {
		t.Errorf("row 3 = %q, want a full-width dashed rule", got)
	}
	if strings.TrimSpace(out[11]) != "tail line" {
		t.Errorf("last row = %q, want the tail", out[11])
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
	for i := 1 + 5; i < h; i++ {
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

// TestRenderWorkBandNoURLsTailGetsFullSpareHeight is admin-dky7's own
// acceptance criterion: a running command with no configured urls has a
// one-row field block (log only, no padding), so the tail gets h-2 rows —
// h minus the title row minus that single field row.
func TestRenderWorkBandNoURLsTailGetsFullSpareHeight(t *testing.T) {
	freshLivework(t)
	ring := livework.NewRing(4096)
	for i := 0; i < 30; i++ {
		ring.Write([]byte("line\n"))
	}
	wd := &workRowData{
		cmdName: "dev", cmdKind: "shell", cmdDesc: "start dev",
		live: &livework.Entry{ID: "dev-no-urls", StartedAt: time.Now(), Output: ring},
	}

	mm := workTestModel(nil)
	row := rowItem{kind: rowCommand, work: wd}
	const h = 20
	out := mm.renderWorkBand(row, 60, h)
	if len(out) != h {
		t.Fatalf("renderWorkBand produced %d lines, want exactly %d", len(out), h)
	}
	// Row 0 is the title, row 1 is the sole "log" field row; every row from
	// 2 onward is tail — h-2 rows of it — so row 2 itself must already carry
	// tail content rather than a blank field-block gap.
	if strings.TrimSpace(stripANSI(out[1])) == "" || !strings.Contains(out[1], "log") {
		t.Fatalf("row 1 = %q, want the sole log field row", out[1])
	}
	if strings.TrimSpace(out[2]) == "" {
		t.Errorf("row 2 (first tail row, h-2 available) = %q, want tail content, not a leftover fixed-slot gap", out[2])
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
