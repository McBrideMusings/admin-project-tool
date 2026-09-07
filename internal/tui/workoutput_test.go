// workoutput_test.go covers the Work tab detail pane's live-output tail: a
// hosted entry's Output ring, once non-nil, fills the rest of the column
// below the existing field block rather than leaving it blank. Follows
// workurls_test.go's pattern of calling workerDetail/commandDetail directly
// rather than standing up a full Frame.
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// TestWorkerDetailTailsLiveRingBottomAligned is the core acceptance
// criterion: a worker with a live Output ring shows its last lines under a
// quiet "output" header, bottom-aligned against the pane's height so a short
// tail leaves its gap above rather than below.
func TestWorkerDetailTailsLiveRingBottomAligned(t *testing.T) {
	freshLivework(t)
	ring := livework.NewRing(4096)
	ring.Write([]byte("line one\nline two\nline three\n"))
	e := &livework.Entry{
		ID: "test-tail-worker", Name: "test-tail-worker",
		State: livework.StateRunning, StartedAt: time.Now(),
		Output: ring,
	}

	const h = 20
	out := workerDetail(e, 60, h)
	if len(out) != h {
		t.Fatalf("workerDetail with a live ring produced %d lines, want exactly %d (fills the column)", len(out), h)
	}

	headerIdx := -1
	for i, l := range out {
		if strings.Contains(l, "output") {
			headerIdx = i
			break
		}
	}
	if headerIdx == -1 {
		t.Fatalf("no \"output\" section header found:\n%s", strings.Join(out, "\n"))
	}

	// Bottom-aligned: the three ring lines are the LAST three rows of the
	// whole pane, and the row right before "line one" (if any gap remains)
	// is blank, not more text.
	if out[h-1] != "line three" || out[h-2] != "line two" || out[h-3] != "line one" {
		t.Fatalf("tail not bottom-aligned, last rows = %q", out[h-3:])
	}
	if headerIdx >= h-3 {
		t.Fatalf("header at row %d overlaps the tail content starting at row %d", headerIdx, h-3)
	}
	for i := headerIdx + 1; i < h-3; i++ {
		if out[i] != "" {
			t.Errorf("row %d between header and tail = %q, want blank (the bottom-align gap)", i, out[i])
		}
	}
}

// TestWorkerDetailNoTailWithoutLiveRing is the negative case: a worker with
// no Output ring (nil — e.g. dispatched through a [terminal] launcher rather
// than hosted in-process) renders exactly as before, with no "output"
// section, no header, no placeholder.
func TestWorkerDetailNoTailWithoutLiveRing(t *testing.T) {
	freshLivework(t)
	e := &livework.Entry{
		ID: "test-no-tail-worker", Name: "test-no-tail-worker",
		State: livework.StateRunning, StartedAt: time.Now(),
		Via: "terminal", Handle: "pane-1",
	}

	withoutHeight := workerDetail(e, 60, 8)
	withTallHeight := workerDetail(e, 60, 40)

	if len(withoutHeight) != len(withTallHeight) {
		t.Fatalf("a taller pane changed a nil-ring worker's output: %d lines vs %d", len(withoutHeight), len(withTallHeight))
	}
	for _, out := range [][]string{withoutHeight, withTallHeight} {
		for _, l := range out {
			if strings.Contains(l, "output") {
				t.Errorf("nil-ring worker detail shows an \"output\" section:\n%s", strings.Join(out, "\n"))
			}
		}
	}
}

// TestWorkerDetailAnsiStrippedAndTruncated proves the tail is a raw peek:
// ANSI escapes are stripped (not re-colorized) and each line is truncated to
// the column width rather than wrapped or left overflowing.
func TestWorkerDetailAnsiStrippedAndTruncated(t *testing.T) {
	freshLivework(t)
	ring := livework.NewRing(4096)
	ring.Write([]byte("\x1b[31mred error\x1b[0m\n" + strings.Repeat("x", 200) + "\n"))
	e := &livework.Entry{
		ID: "test-ansi-worker", Name: "test-ansi-worker",
		State: livework.StateRunning, StartedAt: time.Now(),
		Output: ring,
	}

	out := workerDetail(e, 20, 20)
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "\x1b[") {
		t.Errorf("tail retained a raw ANSI escape:\n%q", joined)
	}
	if !strings.Contains(joined, "red error") {
		t.Errorf("tail lost the plain text under the stripped escape:\n%q", joined)
	}
	for _, l := range out {
		if len(l) > 20*4 { // generous byte ceiling; vlen is what's enforced
			t.Errorf("line %q looks untruncated for a width-20 pane", l)
		}
	}
}

// TestCommandDetailTailsLiveRing proves the same tail behavior on the
// command-detail branch (a hosted command's own Output ring), not just the
// worker branch.
func TestCommandDetailTailsLiveRing(t *testing.T) {
	freshLivework(t)
	ring := livework.NewRing(4096)
	ring.Write([]byte("building...\ndone\n"))
	wd := &workRowData{
		cmdName: "dev", cmdKind: "shell", cmdDesc: "start dev",
		live: &livework.Entry{ID: "dev-tail", StartedAt: time.Now(), Output: ring},
	}

	const h = 25
	out := commandDetail(wd, 60, h)
	if len(out) != h {
		t.Fatalf("commandDetail with a live ring produced %d lines, want exactly %d", len(out), h)
	}
	if out[h-1] != "done" || out[h-2] != "building..." {
		t.Fatalf("tail not bottom-aligned, last two rows = %q", out[h-2:])
	}
}

// TestCommandDetailNoTailWhenIdle is the idle-command negative case: no live
// entry at all, so no tail and no header, whatever height is passed.
func TestCommandDetailNoTailWhenIdle(t *testing.T) {
	wd := &workRowData{cmdName: "build", cmdKind: "shell", cmdDesc: "build it"}

	out := commandDetail(wd, 60, 40)
	for _, l := range out {
		if strings.Contains(l, "output") {
			t.Errorf("idle command detail shows an \"output\" section:\n%s", strings.Join(out, "\n"))
		}
	}
}

// TestWorkerDetailActionsRowSeparatedFromOutput is the actions-row-collision
// defect: with a live ring, the "a attach   k kill" actions row must be
// followed by a blank line before the "output" header, the same separator
// the pane already puts between its own blocks (log/elapsed/steps) — not
// butted directly against it. Total line count must still equal h.
func TestWorkerDetailActionsRowSeparatedFromOutput(t *testing.T) {
	freshLivework(t)
	ring := livework.NewRing(4096)
	ring.Write([]byte("line one\nline two\n"))
	e := &livework.Entry{
		ID: "test-actions-worker", Name: "test-actions-worker",
		State: livework.StateRunning, StartedAt: time.Now(),
		Output: ring,
	}

	const h = 20
	out := workerDetail(e, 44, h)
	if len(out) != h {
		t.Fatalf("workerDetail with a live ring produced %d lines, want exactly %d", len(out), h)
	}

	actionsIdx := -1
	for i, l := range out {
		if strings.Contains(l, "attach") {
			actionsIdx = i
			break
		}
	}
	if actionsIdx == -1 {
		t.Fatalf("no actions row found:\n%s", strings.Join(out, "\n"))
	}
	if out[actionsIdx+1] != "" {
		t.Errorf("row after actions row = %q, want a blank separator", out[actionsIdx+1])
	}
	if !strings.Contains(out[actionsIdx+2], "output") {
		t.Errorf("row after the separator = %q, want the \"output\" header", out[actionsIdx+2])
	}
}

// TestWorkerDetailActionsRowUnchangedWithoutRing is the negative case: with
// no Output ring, the actions row and everything around it must render
// byte-for-byte as it did before this fix — no separator inserted with
// nothing to separate it from.
func TestWorkerDetailActionsRowUnchangedWithoutRing(t *testing.T) {
	freshLivework(t)
	e := &livework.Entry{
		ID: "test-actions-no-ring", Name: "test-actions-no-ring",
		State: livework.StateRunning, StartedAt: time.Now(),
		Via: "terminal", Handle: "pane-1",
	}

	out := workerDetail(e, 44, 20)
	if out[len(out)-1] != faintStyle.Render("a attach   k kill") {
		t.Fatalf("last row = %q, want the actions row unchanged with no ring", out[len(out)-1])
	}
}
