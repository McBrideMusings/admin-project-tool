package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestDrainReturnsOnQuiescenceNotBackstop pins the actual fix for admin-3rgp.44:
// drain must return as soon as no command is in flight, not only when its
// backstop timer fires. Before this change drain(d) was a flat time.After(d)
// wait with no early exit, so under contention a command that took only a
// little longer than a short, tuned-for-idle-machine deadline (a
// 900ms settle) got cut off before it delivered — "repo screen did not
// render" in TestTabSwitchesTabs, reproduced 20/20 under real load (see
// admin-3rgp.44 notes). Reverting drain to a bare deadline wait makes this
// test fail: it would sit out the full two-second backstop instead of
// returning once the 120ms command lands.
func TestDrainReturnsOnQuiescenceNotBackstop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, t.TempDir(), "owner/repo", "main", "main")
	m.herdr = false

	p := &pump{msgs: make(chan tea.Msg, 64), model: m}
	// A command well short of the backstop but long enough that a machine
	// under contention could plausibly delay it past a short fixed deadline —
	// exactly the shape of a git load exceeding a 900ms settle.
	const work = 120 * time.Millisecond
	p.run(func() tea.Msg {
		time.Sleep(work)
		return tea.WindowSizeMsg{Width: 100, Height: 24}
	})

	const backstop = 2 * time.Second
	start := time.Now()
	p.drain(backstop)
	elapsed := time.Since(start)

	if elapsed >= backstop {
		t.Fatalf("drain waited the full %s backstop instead of returning on quiescence (took %s for a %s command) — this is the deadline-only regression", backstop, elapsed, work)
	}
	// Generous slack over `work` for scheduler jitter under a loaded machine —
	// the point is "nowhere near the backstop", not a tight timing bound.
	if elapsed > 750*time.Millisecond {
		t.Errorf("drain took %s to settle a %s command; expected it to return promptly once quiescent", elapsed, work)
	}
	if got := atomic.LoadInt32(&p.inFlight); got != 0 {
		t.Errorf("inFlight = %d after drain returned quiescent; the delivered command was never applied", got)
	}
}

// TestDrainBackstopBoundsAStuckCommand pins the other half: a command that
// never delivers must not hang the suite forever. The backstop is the
// ceiling, not the normal path, but it still has to fire.
func TestDrainBackstopBoundsAStuckCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, t.TempDir(), "owner/repo", "main", "main")
	m.herdr = false

	p := &pump{msgs: make(chan tea.Msg, 64), model: m}
	block := make(chan struct{}) // never closed: this command never returns
	p.run(func() tea.Msg {
		<-block
		return nil
	})

	const backstop = 100 * time.Millisecond
	start := time.Now()
	p.drain(backstop)
	elapsed := time.Since(start)

	if elapsed < backstop {
		t.Errorf("drain returned in %s, before its %s backstop, with the command still stuck", elapsed, backstop)
	}
	if elapsed > backstop+500*time.Millisecond {
		t.Errorf("drain took %s to honor a %s backstop", elapsed, backstop)
	}
}

// TestFrameOfARealInitReturnsOnQuiescence pins admin-3rgp.78: a headless
// frame of a real Init — file watcher, sync poll, beads poll and all — must
// return once the loads land, not sit out its Settle backstop. Each of those
// is a command that never delivers inside a frame, so left armed they kept
// the pump in flight forever: every Frame paid its full Settle, a test that
// asked for 500ms raced two `bd` subprocesses against it, and the watcher's
// per-directory descriptors piled up across the package until fork failed
// with "too many open files".
func TestFrameOfARealInitReturnsOnQuiescence(t *testing.T) {
	putFakeBdOnPath(t, fakeBdScript)
	m := beadsModel(t)

	const settle = 30 * time.Second
	start := time.Now()
	out := stripANSI(Frame(context.Background(), m, FrameOptions{Width: 140, Height: 30, Settle: settle, Keys: []string{"right"}}))
	elapsed := time.Since(start)

	if elapsed > settle/2 {
		t.Fatalf("Frame took %s against a %s backstop; a recurring command is keeping the pump in flight", elapsed, settle)
	}
	if !strings.Contains(out, "Waiting on a person") {
		t.Errorf("Frame returned before the beads list landed, got:\n%s", out)
	}
}
