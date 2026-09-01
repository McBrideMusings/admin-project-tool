// terminalview_test.go exercises admin-3rgp.65's terminal view through the
// dashboard's headless frame renderer (frame.go's Frame, and — where a test
// needs to inject bytes into the ring between keypresses, which Frame's
// fixed Keys sequence can't do — its own unexported pump type, the exact
// mechanism Frame itself uses). A fake pty-addressed Entry (a real
// livework.Ring plus recorder Ops.Write/Ops.Resize closures) stands in for
// a real pty throughout, per the ticket's own note that this slice needs no
// real pty to prove any of its acceptance criteria.
package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// writeFixtureManifest writes a minimal admin.toml declaring one command
// named cmdName, mirroring work_enter_test.go's own fixture — New() reads a
// real file off disk, and this is the cheapest way to get a manifestOK
// Model without duplicating archetype-resolution plumbing in this file.
func writeFixtureManifest(t *testing.T, dir, cmdName string) {
	t.Helper()
	toml := "project_name = \"fixture\"\n\n[commands." + cmdName + "]\ndesc = \"fixture\"\nsteps = [\"" + cmdName + "\"]\n\n[actions." + cmdName + "]\nkind = \"shell\"\nrun = \"true\"\n"
	if err := os.WriteFile(filepath.Join(dir, manifest.Filename), []byte(toml), 0o644); err != nil {
		t.Fatalf("writing fixture admin.toml: %v", err)
	}
}

// termRecorder stands in for a real pty's write/resize side, recording
// every call under a mutex so a test's assertions and the pump's own
// background goroutines never race each other.
type termRecorder struct {
	mu      sync.Mutex
	writes  [][]byte
	resizes [][2]int
}

func (f *termRecorder) write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, append([]byte{}, p...))
	return len(p), nil
}

func (f *termRecorder) resize(rows, cols int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]int{rows, cols})
	return nil
}

func (f *termRecorder) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

func (f *termRecorder) lastWrite() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) == 0 {
		return nil
	}
	return f.writes[len(f.writes)-1]
}

func (f *termRecorder) resizeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.resizes)
}

func (f *termRecorder) lastResize() [2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.resizes) == 0 {
		return [2]int{}
	}
	return f.resizes[len(f.resizes)-1]
}

// registerFakePTYEntry adds a pty-addressed live-work Entry named cmdName
// (matching the fixture manifest's own command/step name, so the Work tab's
// row for it shows live), with Ops.Write/Ops.Resize wired to rec and Kill a
// harmless no-op. No real pty is created anywhere.
func registerFakePTYEntry(t *testing.T, cmdName string, ring *livework.Ring, rec *termRecorder) {
	t.Helper()
	e := &livework.Entry{
		ID:     cmdName,
		Name:   cmdName,
		Kind:   "hosted-pty",
		PTYID:  cmdName + "-pty",
		State:  livework.StateRunning,
		Output: ring,
		Ops: livework.Ops{
			Kill:   func() error { return nil },
			Write:  rec.write,
			Resize: rec.resize,
		},
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}
}

// newAttachablePump builds a Model over a real fixture admin.toml, runs it
// through Init and settles it — exactly what Frame does — and returns the
// pump so a test can interleave a raw Ring.Write between keypresses, which
// Frame's own fixed Keys sequence has no hook for.
func newAttachablePump(t *testing.T, dir string, width, height int) *pump {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := New(ctx, dir, "owner/repo", "main", "main")
	m.herdr = false

	p := &pump{msgs: make(chan tea.Msg, 64), model: m}
	p.apply(tea.WindowSizeMsg{Width: width, Height: height})
	p.run(p.model.Init())
	p.drain(2 * time.Second)
	return p
}

// TestTerminalViewAttachedEmptyShowsWaitingCopy is the AC's "before any
// output arrives the view renders the copy 'waiting for output…'" — pinned
// against docs/spikes/admin-tui-desktop/hosted-empty.txt's own line 7.
func TestTerminalViewAttachedEmptyShowsWaitingCopy(t *testing.T) {
	const cmdName = "termfixture-empty"
	dir := t.TempDir()
	writeFixtureManifest(t, dir, cmdName)
	registerFakePTYEntry(t, cmdName, livework.NewRing(0), &termRecorder{})

	out := Frame(context.Background(), New(context.Background(), dir, "owner/repo", "main", "main"), FrameOptions{
		Width: 100, Height: 24,
		Keys:   []string{"a"},
		Settle: 300 * time.Millisecond,
	})

	if !strings.Contains(out, "waiting for output…") {
		t.Errorf("attached-empty frame missing the waiting copy: %q", out)
	}
	if !strings.Contains(out, "detach") || !strings.Contains(out, "goes to the process") {
		t.Errorf("attached frame missing its own footer keymap: %q", out)
	}
}

// TestTerminalViewDoesNotReplayPreAttachOutput proves attach records the
// ring's CURRENT total-written as its offset: bytes already sitting in the
// ring before attach must never reach the emulator — the ticket's explicit
// "output produced before attaching is not replayed."
//
// MUTATION EVIDENCE: change openTerminal to seed m.termOff at 0 instead of
// e.Output.Total() and this test fails — the pre-attach line shows up in
// the attached frame instead of "waiting for output…".
func TestTerminalViewDoesNotReplayPreAttachOutput(t *testing.T) {
	const cmdName = "termfixture-noreplay"
	dir := t.TempDir()
	writeFixtureManifest(t, dir, cmdName)
	ring := livework.NewRing(0)
	if _, err := ring.Write([]byte("this line predates the attach\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	registerFakePTYEntry(t, cmdName, ring, &termRecorder{})

	p := newAttachablePump(t, dir, 100, 24)
	p.apply(keyMsg("a"))
	p.drain(300 * time.Millisecond)

	out := p.model.View()
	if strings.Contains(out, "predates the attach") {
		t.Errorf("attached frame replayed pre-attach output, want none: %q", out)
	}
	if !strings.Contains(out, "waiting for output…") {
		t.Errorf("attached frame with only pre-attach output should still read as empty: %q", out)
	}
}

// TestTerminalViewRendersColoredOutputAfterAttach proves output written to
// the entry's Ring AFTER attach reaches the emulator and keeps its SGR
// color codes — the log formatter's own [ERROR]/[WARN]/[INFO]/[DEBUG] and
// scope-tag colors survive because the child emits them as real SGR
// sequences into what is, from its point of view, a real terminal.
func TestTerminalViewRendersColoredOutputAfterAttach(t *testing.T) {
	const cmdName = "termfixture-color"
	dir := t.TempDir()
	writeFixtureManifest(t, dir, cmdName)
	ring := livework.NewRing(0)
	registerFakePTYEntry(t, cmdName, ring, &termRecorder{})

	p := newAttachablePump(t, dir, 100, 24)
	p.apply(keyMsg("a"))
	p.drain(150 * time.Millisecond)
	if !p.model.termAttached {
		t.Fatal("attach did not enter the terminal view")
	}

	if _, err := ring.Write([]byte("\x1b[31mERROR\x1b[0m boom\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p.drain(300 * time.Millisecond)

	out := p.model.View()
	if !strings.Contains(out, "ERROR") {
		t.Fatalf("attached frame missing the child's own output: %q", out)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("attached frame lost the child's SGR color codes: %q", out)
	}
}

// TestTerminalViewKeystrokeReachesChild is the AC's "a keystroke typed while
// attached reaches the child" as read through Ops.Write rather than a real
// prompt/response (no real pty here) — the same seam a real answer to a
// prompting process goes through.
func TestTerminalViewKeystrokeReachesChild(t *testing.T) {
	const cmdName = "termfixture-keys"
	dir := t.TempDir()
	writeFixtureManifest(t, dir, cmdName)
	rec := &termRecorder{}
	registerFakePTYEntry(t, cmdName, livework.NewRing(0), rec)

	p := newAttachablePump(t, dir, 100, 24)
	p.apply(keyMsg("a"))
	p.drain(150 * time.Millisecond)
	if !p.model.termAttached {
		t.Fatal("attach did not enter the terminal view")
	}

	p.apply(keyMsg("2"))
	p.drain(50 * time.Millisecond)
	p.apply(tea.KeyMsg{Type: tea.KeyEnter})
	p.drain(50 * time.Millisecond)

	if rec.writeCount() < 2 {
		t.Fatalf("Ops.Write called %d times, want at least 2 (the digit and enter)", rec.writeCount())
	}
	if got := rec.lastWrite(); string(got) != "\r" {
		t.Errorf("last write = %q, want the encoded enter key (\"\\r\")", got)
	}
}

// TestTerminalViewWindowResizeResizesChildPty is the AC's "resizing the
// terminal resizes the child's pty."
func TestTerminalViewWindowResizeResizesChildPty(t *testing.T) {
	const cmdName = "termfixture-resize"
	dir := t.TempDir()
	writeFixtureManifest(t, dir, cmdName)
	rec := &termRecorder{}
	registerFakePTYEntry(t, cmdName, livework.NewRing(0), rec)

	p := newAttachablePump(t, dir, 100, 24)
	p.apply(keyMsg("a"))
	p.drain(150 * time.Millisecond)
	if !p.model.termAttached {
		t.Fatal("attach did not enter the terminal view")
	}
	beforeResizes := rec.resizeCount()
	if beforeResizes == 0 {
		t.Fatal("attach itself never resized the child's pty")
	}

	p.apply(tea.WindowSizeMsg{Width: 140, Height: 40})
	p.drain(50 * time.Millisecond)

	if rec.resizeCount() <= beforeResizes {
		t.Fatalf("resizeCount after WindowSizeMsg = %d, want more than %d", rec.resizeCount(), beforeResizes)
	}
	rows, cols := p.model.termRegion()
	if got := rec.lastResize(); got != [2]int{rows, cols} {
		t.Errorf("last Resize call = %v, want %v (the region computed for the new size)", got, [2]int{rows, cols})
	}
}

// TestTerminalViewQAndEscBothDetach is the AC's "q and esc both detach
// while attached" — the process stays registered (still running) and the
// dashboard returns to the list.
func TestTerminalViewQAndEscBothDetach(t *testing.T) {
	for _, key := range []string{"q", "esc"} {
		t.Run(key, func(t *testing.T) {
			cmdName := "termfixture-detach-" + key
			dir := t.TempDir()
			writeFixtureManifest(t, dir, cmdName)
			registerFakePTYEntry(t, cmdName, livework.NewRing(0), &termRecorder{})

			p := newAttachablePump(t, dir, 100, 24)
			p.apply(keyMsg("a"))
			p.drain(150 * time.Millisecond)
			if !p.model.termAttached {
				t.Fatal("attach did not enter the terminal view")
			}

			p.apply(keyMsg(key))
			p.drain(50 * time.Millisecond)

			if p.model.termAttached {
				t.Errorf("%q did not detach", key)
			}
			if _, ok := livework.Default.Get(cmdName); !ok {
				t.Error("detaching removed the live-work entry — the process must stay registered as still running")
			}
			out := p.model.View()
			if !strings.Contains(out, cmdName) {
				t.Errorf("detached frame lost the command list: %q", out)
			}
		})
	}
}
