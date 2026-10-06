// workoutput_test.go covers the Work tab's live band output tail: a hosted
// entry's Screen fills the rest of the band below the fixed field block,
// bottom-aligned so a short tail leaves its gap above rather than below,
// with no "output" section header or rule.
package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/charmbracelet/x/vt"
)

// TestRenderWorkBandTailsLiveScreenBottomAligned is the core acceptance
// criterion: a worker with a live Screen shows its last lines
// bottom-aligned against the floor of the band, with the field block (a
// worker's own five real rows) and the title rule above it untouched.
func TestRenderWorkBandTailsLiveScreenBottomAligned(t *testing.T) {
	freshLivework(t)
	screen := hostedScreen(t, "line one\nline two\nline three\n")
	e := &livework.Entry{
		ID: "test-tail-worker", Name: "test-tail-worker",
		State: livework.StateRunning, StartedAt: time.Now(),
		Screen: screen,
	}

	mm := workTestModel(nil)
	row := rowItem{kind: rowWorker, work: &workRowData{worker: e}}
	const h = 20
	out := mm.renderWorkBand(row, 60, h)
	if len(out) != h {
		t.Fatalf("renderWorkBand with a live screen produced %d lines, want exactly %d (fills the band)", len(out), h)
	}

	// Bottom-aligned against the band's own last row: the three output lines
	// are the very last three rows, with no rule or header between the
	// field block and the tail.
	if strings.TrimSpace(out[h-1]) != "line three" || strings.TrimSpace(out[h-2]) != "line two" || strings.TrimSpace(out[h-3]) != "line one" {
		t.Fatalf("tail not bottom-aligned against the band's floor, rows h-3..h-1 = %q", out[h-3:])
	}
	// The field block occupies exactly 1 (title) + 5 (a worker's own
	// log/branch/worktree/session/now rows) and the dashed rule one more;
	// everything between the rule and the tail must be blank.
	for i := 1 + fieldRowsH + 1; i < h-3; i++ {
		if strings.TrimSpace(out[i]) != "" {
			t.Errorf("row %d between the field block and the tail = %q, want blank (the bottom-align gap)", i, out[i])
		}
	}
}

// TestRenderWorkBandNoRuleBetweenFieldsAndTail proves the open band draws no
// rule between the fixed field slot and the tail, matching
// commands-running.txt and swarm.txt: the tail starts on the row right after
// the slot and ends on the band's last row.
func TestRenderWorkBandNoRuleBetweenFieldsAndTail(t *testing.T) {
	freshLivework(t)
	screen := hostedScreen(t, "tail line\n")
	wd := &workRowData{
		cmdName: "dev", cmdKind: "shell",
		urls: []frame.URLEntry{{Label: "local", URL: "http://localhost:1"}},
		live: &livework.Entry{ID: "dev-rule", StartedAt: time.Now(), State: livework.StateRunning, Screen: screen},
	}
	out := workTestModel(nil).renderWorkBand(rowItem{kind: rowCommand, work: wd}, 60, 12)
	if !strings.Contains(out[1], "log") || !strings.Contains(out[2], "url") {
		t.Fatalf("rows 1-2 = %q, want the log and url field rows", out[1:3])
	}
	for i, l := range out {
		if strings.Contains(l, "╌") {
			t.Errorf("row %d = %q, want no dashed rule", i, l)
		}
	}
	if strings.TrimSpace(out[11]) != "tail line" {
		t.Errorf("last row = %q, want the tail", out[11])
	}
}

// TestRenderWorkBandEmptyTailWithoutLiveScreen is the negative case: a worker
// with no Screen and no pane snapshot (dispatched through a [terminal]
// launcher that declares no read command) shows the attach hint as its last
// row, everything else below the field block blank, still exactly h lines.
func TestRenderWorkBandEmptyTailWithoutLiveScreen(t *testing.T) {
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
	for i := 1 + fieldRowsH + 1; i < h-1; i++ {
		if strings.TrimSpace(out[i]) != "" {
			t.Errorf("row %d with no live screen = %q, want blank", i, out[i])
		}
	}
	if got := strings.TrimSpace(out[h-1]); got != noLocalOutput {
		t.Errorf("last row = %q, want %q", got, noLocalOutput)
	}
}

// TestRenderWorkBandAnsiStrippedAndTruncated proves the tail is a raw peek:
// ANSI escapes are stripped (not re-colorized) and each line is truncated to
// the column width rather than wrapped or left overflowing.
func TestRenderWorkBandAnsiStrippedAndTruncated(t *testing.T) {
	freshLivework(t)
	screen := hostedScreen(t, "\x1b[31mred error\x1b[0m\n"+strings.Repeat("x", 200)+"\n")
	e := &livework.Entry{
		ID: "test-ansi-worker", Name: "test-ansi-worker",
		State: livework.StateRunning, StartedAt: time.Now(),
		Screen: screen,
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

// TestRenderWorkBandCommandTailsLiveScreen proves the same tail behavior on
// the live-command branch (a hosted command's own Screen), not just
// the worker branch.
func TestRenderWorkBandCommandTailsLiveScreen(t *testing.T) {
	freshLivework(t)
	screen := hostedScreen(t, "building...\ndone\n")
	wd := &workRowData{
		cmdName: "dev", cmdKind: "shell", cmdDesc: "start dev",
		live: &livework.Entry{ID: "dev-tail", StartedAt: time.Now(), Screen: screen},
	}

	mm := workTestModel(nil)
	row := rowItem{kind: rowCommand, work: wd}
	const h = 25
	out := mm.renderWorkBand(row, 60, h)
	if len(out) != h {
		t.Fatalf("renderWorkBand with a live screen produced %d lines, want exactly %d", len(out), h)
	}
	if strings.TrimSpace(out[h-1]) != "done" || strings.TrimSpace(out[h-2]) != "building..." {
		t.Fatalf("tail not bottom-aligned against the band's floor, rows h-2..h-1 = %q", out[h-2:])
	}
}

// TestRenderWorkBandNoURLsKeepsFieldSlot: a running command with no
// configured urls still occupies the whole fieldRowsH slot (log row, then
// blank rows), so the tail sits in the same rows as for a
// command with urls and nothing under the block moves.
func TestRenderWorkBandNoURLsKeepsFieldSlot(t *testing.T) {
	freshLivework(t)
	screen := hostedScreen(t, strings.Repeat("line\n", 30))
	wd := &workRowData{
		cmdName: "dev", cmdKind: "shell", cmdDesc: "start dev",
		live: &livework.Entry{ID: "dev-no-urls", StartedAt: time.Now(), Screen: screen},
	}

	const h = 20
	out := workTestModel(nil).renderWorkBand(rowItem{kind: rowCommand, work: wd}, 60, h)
	if len(out) != h {
		t.Fatalf("renderWorkBand produced %d lines, want exactly %d", len(out), h)
	}
	if !strings.Contains(out[1], "log") {
		t.Fatalf("row 1 = %q, want the sole log field row", out[1])
	}
	for i := 2; i <= fieldRowsH; i++ {
		if strings.TrimSpace(out[i]) != "" {
			t.Errorf("row %d = %q, want a blank slot row", i, out[i])
		}
	}
	if strings.TrimSpace(out[1+fieldRowsH]) != "line" {
		t.Errorf("row %d = %q, want tail content right after the slot", 1+fieldRowsH, out[1+fieldRowsH])
	}
}

// hostedScreen is a hosted entry's Screen after its child wrote out through
// the pty: every "\n" goes through the pty's ONLCR translation to "\r\n"
// (a child's own "\r\n" arrives as "\r\r\n"), the way
// internal/kinds/hostedpty.go's 80x24 emulator receives it.
func hostedScreen(t *testing.T, out string) *vt.SafeEmulator {
	t.Helper()
	screen := vt.NewSafeEmulator(80, 24)
	out = strings.ReplaceAll(out, "\n", "\r\n")
	if _, err := screen.Write([]byte(out)); err != nil {
		t.Fatalf("screen.Write: %v", err)
	}
	return screen
}

// TestScreenTailLinesNoControlBytes feeds the band the line a docker build of
// ffmpeg printed — make's "INSTALL\t<file>" behind buildkit's "#34 493.7"
// prefix — plus a progress line redrawn with bare "\r" and a stray
// backspace. A tab measures zero cells but the terminal jumps to the next
// tab stop, so a row carrying one paints wider than its width and wraps,
// scrolling the dashboard and leaving the line drawn over the header. Every
// rendered line must be plain printable text no wider than w.
func TestScreenTailLinesNoControlBytes(t *testing.T) {
	screen := hostedScreen(t, "#34 493.7 INSTALL\tlibavfilter/libavfilter.a\n"+
		"pull 10%\rpull 55%\rpull 100%\n"+
		"abc\bd\n")

	const w = 30
	lines := screenTailLines(screen, w, 24)
	want := []string{
		"#34 493.7 INSTALL       libav…",
		"pull 100%",
		"abd",
	}
	if len(lines) != len(want) {
		t.Fatalf("screenTailLines = %q, want %q", lines, want)
	}
	for i, l := range lines {
		if l != want[i] {
			t.Errorf("line %d = %q, want %q", i, l, want[i])
		}
		if strings.IndexFunc(l, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			t.Errorf("line %d = %q carries a control byte", i, l)
		}
		if vlen(l) > w {
			t.Errorf("line %d = %q is %d cells, wider than %d", i, l, vlen(l), w)
		}
	}
}

// TestScreenTailLinesCursorUpRedraw is docker buildx's progress block: it
// prints its step rows, then moves the cursor up and repaints them in place.
// The band shows each row once, at its latest state — never the stale row
// and the repaint concatenated or stacked.
func TestScreenTailLinesCursorUpRedraw(t *testing.T) {
	screen := hostedScreen(t, "#5 [1/2] RUN make\n#6 [2/2] COPY out\n"+
		"\x1b[2A\x1b[2K#5 [1/2] RUN make 2.3s\n\x1b[2K#6 [2/2] COPY out DONE\n")

	got := screenTailLines(screen, 40, 24)
	want := []string{"#5 [1/2] RUN make 2.3s", "#6 [2/2] COPY out DONE"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("screenTailLines = %q, want %q", got, want)
	}
}

// TestScreenTailLinesReadsScrollback: a build prints more lines than the
// pty has rows, and the band is taller than the pty. The band fills from
// the emulator's scrollback, ending on the last line printed.
func TestScreenTailLinesReadsScrollback(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	screen := hostedScreen(t, b.String())

	got := screenTailLines(screen, 40, 40)
	if len(got) != 40 || got[0] != "line 21" || got[39] != "line 60" {
		t.Errorf("screenTailLines = %q, want line 21 through line 60", got)
	}
}

// TestScreenTailLinesAltScreenSkipsScrollback: a full-screen child on the
// alternate screen shows only its own grid, never the main screen's
// scrollback stacked above it.
func TestScreenTailLinesAltScreenSkipsScrollback(t *testing.T) {
	screen := hostedScreen(t, strings.Repeat("shell line\n", 30)+"\x1b[?1049h\x1b[Hagent view\n")

	got := screenTailLines(screen, 40, 40)
	if !reflect.DeepEqual(got, []string{"agent view"}) {
		t.Errorf("screenTailLines = %q, want only the alternate screen's row", got)
	}
}

// TestRenderWorkBandNoBlankRowBetweenTailLines: a child that writes "\r\n"
// itself reaches the emulator as "\r\r\n" after ONLCR. Through the full
// band assembly, its three lines are the band's last three rows with no
// blank row between them and no "\r" on any of them.
func TestRenderWorkBandNoBlankRowBetweenTailLines(t *testing.T) {
	freshLivework(t)
	screen := hostedScreen(t, "alpha\r\nbeta\r\ngamma\r\n")
	e := &livework.Entry{
		ID: "test-crlf-worker", Name: "test-crlf-worker",
		State: livework.StateRunning, StartedAt: time.Now(), Screen: screen,
	}

	mm := workTestModel(nil)
	row := rowItem{kind: rowWorker, work: &workRowData{worker: e}}
	const h = 20
	out := mm.renderWorkBand(row, 40, h)
	if strings.TrimSpace(out[h-1]) != "gamma" || strings.TrimSpace(out[h-2]) != "beta" || strings.TrimSpace(out[h-3]) != "alpha" {
		t.Fatalf("expected the three tail lines with no blank rows between them, got %q", out[h-3:])
	}
	for i, l := range out {
		if strings.Contains(l, "\r") {
			t.Errorf("row %d = %q carries a raw \\r", i, l)
		}
	}
}
