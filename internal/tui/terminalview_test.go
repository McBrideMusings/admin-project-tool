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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/vt"

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

// fakePTY stands in for the hosted pty's drain (internal/kinds/hostedpty.go):
// Write feeds the entry's Output ring and Screen the same bytes, as that
// drain does.
type fakePTY struct{ e *livework.Entry }

func (f fakePTY) Write(p []byte) (int, error) {
	_, _ = f.e.Output.Write(p)
	return f.e.Screen.Write(p)
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// registerFakePTYEntry adds a pty-addressed live-work Entry named cmdName
// (matching the fixture manifest's own command/step name, so the Work tab's
// row for it shows live), with Ops.Write/Ops.Resize wired to rec and Kill a
// harmless no-op. Its Screen's replies to terminal queries reach rec as
// writes to the child, the way hostedpty.go copies them to the real pty. No
// real pty is created anywhere.
func registerFakePTYEntry(t *testing.T, cmdName string, ring *livework.Ring, rec *termRecorder) fakePTY {
	t.Helper()
	freshLivework(t)
	screen := vt.NewSafeEmulator(80, 24)
	go func() { _, _ = io.Copy(writerFunc(rec.write), screen) }()
	t.Cleanup(func() { _ = screen.Close() })
	e := &livework.Entry{
		ID:     cmdName,
		Name:   cmdName,
		Kind:   "hosted-pty",
		Handle: cmdName + "-pty",
		State:  livework.StateRunning,
		Output: ring,
		Screen: screen,
		Ops: livework.Ops{
			Kill:  func() error { return nil },
			Write: rec.write,
			Resize: func(rows, cols int) error {
				screen.Resize(cols, rows)
				return rec.resize(rows, cols)
			},
		},
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return fakePTY{e: e}
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
	// admin-70hb: a fresh session opens with focus on the tab bar, and `a`
	// (attach) is a row-scoped key that no-ops from there. Every caller of
	// this helper immediately presses `a` against the first Work-tab row, so
	// move focus into the body here once rather than at every call site.
	p.apply(keyMsg("down"))
	p.drain(150 * time.Millisecond)
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
		// admin-70hb: a fresh session opens with focus on the tab bar, so
		// "down" moves it into the body before "a" (attach) — a row-scoped
		// key — can act on the row under the cursor.
		Keys:   []string{"down", "a"},
		Settle: 300 * time.Millisecond,
	})

	if !strings.Contains(out, "waiting for output…") {
		t.Errorf("attached-empty frame missing the waiting copy: %q", out)
	}
	if !strings.Contains(out, "esc") || !strings.Contains(out, "detach") || !strings.Contains(out, "goes to the process") {
		t.Errorf("attached frame footer must name esc as the detach key: %q", out)
	}
	if strings.Contains(out, "q/esc") {
		t.Errorf("attached frame footer still advertises the superseded q/esc detach: %q", out)
	}
	if strings.Contains(out, "k kill") {
		t.Errorf("attached frame footer still advertises k kill, but k now reaches the child: %q", out)
	}
}

// TestTerminalViewShowsPreAttachOutput proves attach shows what the child
// already printed: a counter attached at 16 shows 1 through 16, not a blank
// screen waiting for 17.
func TestTerminalViewShowsPreAttachOutput(t *testing.T) {
	const cmdName = "termfixture-preattach"
	dir := t.TempDir()
	writeFixtureManifest(t, dir, cmdName)
	pty := registerFakePTYEntry(t, cmdName, livework.NewRing(0), &termRecorder{})
	if _, err := pty.Write([]byte("this line predates the attach\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	p := newAttachablePump(t, dir, 100, 24)
	p.apply(keyMsg("a"))
	p.drain(300 * time.Millisecond)

	out := p.model.View()
	if !strings.Contains(out, "predates the attach") {
		t.Errorf("attached frame is missing pre-attach output: %q", out)
	}
	if strings.Contains(out, "waiting for output…") {
		t.Errorf("attached frame with pre-attach output still reads as empty: %q", out)
	}

	if _, err := pty.Write([]byte("this line follows the attach\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p.drain(300 * time.Millisecond)
	out = p.model.View()
	if n := strings.Count(out, "predates the attach"); n != 1 {
		t.Errorf("pre-attach line appears %d times after the next poll, want 1: %q", n, out)
	}
	if !strings.Contains(out, "follows the attach") {
		t.Errorf("attached frame is missing post-attach output: %q", out)
	}
}

// TestTerminalViewAnswersTerminalQueries proves the child's terminal queries
// — the OSC 11 background query and ESC[6n cursor report every admin child
// sends at startup — get answered on its pty before and after attach, and
// never freeze the dashboard.
func TestTerminalViewAnswersTerminalQueries(t *testing.T) {
	const cmdName = "termfixture-query"
	dir := t.TempDir()
	writeFixtureManifest(t, dir, cmdName)
	rec := &termRecorder{}
	pty := registerFakePTYEntry(t, cmdName, livework.NewRing(0), rec)
	if _, err := pty.Write([]byte("\x1b]11;?\x1b\\\x1b[6nbefore attach\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p := newAttachablePump(t, dir, 100, 24)

	within := func(step string, f func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { f(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not return within 5s — the emulator blocked on its query reply", step)
		}
	}

	within("attach", func() {
		p.apply(keyMsg("a"))
		p.drain(300 * time.Millisecond)
	})
	within("write after attach", func() {
		if _, err := pty.Write([]byte("\x1b[6nafter attach\r\n")); err != nil {
			t.Errorf("Write: %v", err)
		}
		p.drain(300 * time.Millisecond)
	})

	out := p.model.View()
	for _, want := range []string{"before attach", "after attach"} {
		if !strings.Contains(out, want) {
			t.Errorf("attached frame missing %q: %q", want, out)
		}
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var sent []byte
	for _, w := range rec.writes {
		sent = append(sent, w...)
	}
	if !strings.Contains(string(sent), "\x1b]11;rgb:") {
		t.Errorf("child never got a background-color reply; pty writes = %q", sent)
	}
	if n := strings.Count(string(sent), "R"); n < 2 {
		t.Errorf("child got %d cursor-position replies, want 2 (one per ESC[6n); pty writes = %q", n, sent)
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
	pty := registerFakePTYEntry(t, cmdName, livework.NewRing(0), &termRecorder{})

	p := newAttachablePump(t, dir, 100, 24)
	p.apply(keyMsg("a"))
	p.drain(150 * time.Millisecond)
	if !p.model.termAttached {
		t.Fatal("attach did not enter the terminal view")
	}

	if _, err := pty.Write([]byte("\x1b[31mERROR\x1b[0m boom\r\n")); err != nil {
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

// TestTerminalViewEscDetachesAndQKReachChild supersedes
// TestTerminalViewQAndEscBothDetach (admin-3rgp.65's original name and
// contract: q and esc both detached, k killed). admin-3rgp.71 found that
// contract unanswerable for any child prompting with q or k — see its own
// reference frame's "Enter a number, or q to cancel:" — and narrowed detach
// to esc alone, so q and k now fall through to the child like every other
// key.
func TestTerminalViewEscDetachesAndQKReachChild(t *testing.T) {
	t.Run("esc detaches", func(t *testing.T) {
		const cmdName = "termfixture-detach-esc"
		dir := t.TempDir()
		writeFixtureManifest(t, dir, cmdName)
		registerFakePTYEntry(t, cmdName, livework.NewRing(0), &termRecorder{})

		p := newAttachablePump(t, dir, 100, 24)
		p.apply(keyMsg("a"))
		p.drain(150 * time.Millisecond)
		if !p.model.termAttached {
			t.Fatal("attach did not enter the terminal view")
		}

		p.apply(keyMsg("esc"))
		p.drain(50 * time.Millisecond)

		if p.model.termAttached {
			t.Error("esc did not detach")
		}
		if _, ok := livework.Default.Get(cmdName); !ok {
			t.Error("detaching removed the live-work entry — the process must stay registered as still running")
		}
		// A prefix, not the full cmdName: the detail pane's header now
		// shares its row with the live state and elapsed
		// (internal/tui/workview.go's detailHeader), so a long name
		// truncates there at a different width than the row list's own
		// fixed name column — neither is guaranteed to fit all 22 characters
		// of this fixture's name, but both fit well past this prefix.
		out := p.model.View()
		if !strings.Contains(out, cmdName[:10]) {
			t.Errorf("detached frame lost the command list: %q", out)
		}
	})

	for _, key := range []string{"q", "k"} {
		t.Run(key+" reaches the child", func(t *testing.T) {
			cmdName := "termfixture-passthrough-" + key
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

			p.apply(keyMsg(key))
			p.drain(50 * time.Millisecond)

			if !p.model.termAttached {
				t.Errorf("%q detached, want it to reach the child instead", key)
			}
			if got := string(rec.lastWrite()); got != key {
				t.Errorf("last write = %q, want %q written through to the child", got, key)
			}
		})
	}
}

// TestAttachedViewDrawsNoTabBar is the ticket's first acceptance criterion:
// no tab bar at any of the three named sizes, and the second row is the
// attach breadcrumb — verbatim "attached" — in the tab bar's place.
func TestAttachedViewDrawsNoTabBar(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{80, 24}, {96, 30}, {178, 46}} {
		t.Run(fmt.Sprintf("%dx%d", sz.w, sz.h), func(t *testing.T) {
			cmdName := "termfixture-nobar"
			dir := t.TempDir()
			writeFixtureManifest(t, dir, cmdName)
			registerFakePTYEntry(t, cmdName, livework.NewRing(0), &termRecorder{})

			out := Frame(context.Background(), New(context.Background(), dir, "owner/repo", "main", "main"), FrameOptions{
				Width: sz.w, Height: sz.h,
				Keys:   []string{"down", "a"},
				Settle: 300 * time.Millisecond,
			})
			lines := strings.Split(out, "\n")
			if len(lines) < 2 {
				t.Fatalf("frame at %dx%d has fewer than 2 lines", sz.w, sz.h)
			}
			for _, tabLabel := range []string{"Work", "PRs", "Issues", "Beads", "Repo"} {
				if strings.Contains(lines[1], tabLabel) {
					t.Errorf("%dx%d: second row still shows a tab bar (contains %q): %q", sz.w, sz.h, tabLabel, lines[1])
				}
			}
			if !strings.Contains(lines[1], "attached") {
				t.Errorf("%dx%d: second row is not the attach breadcrumb: %q", sz.w, sz.h, lines[1])
			}
		})
	}
}

// TestAttachedViewLogPathOnBreadcrumbOnly is the ticket's second acceptance
// criterion: the absolute log path is on the breadcrumb row and nowhere
// beneath the output.
func TestAttachedViewLogPathAndURLRowsUnderBreadcrumb(t *testing.T) {
	cmdName := "termfixture-logpath"
	dir := t.TempDir()
	toml := "project_name = \"fixture\"\n\n[commands." + cmdName + "]\ndesc = \"fixture\"\nsteps = [\"" + cmdName +
		"\"]\n\n[actions." + cmdName + "]\nkind = \"shell\"\nrun = \"true\"\nurls = [\"http://127.0.0.1:9/fixture-url\"]\n"
	if err := os.WriteFile(filepath.Join(dir, manifest.Filename), []byte(toml), 0o644); err != nil {
		t.Fatalf("writing fixture admin.toml: %v", err)
	}
	rec := &termRecorder{}
	registerFakePTYEntry(t, cmdName, livework.NewRing(0), rec)
	const logPath = "/Users/pierce/.admin/logs/worker-termfixture-logpath.log"
	livework.Default.SetLogPath(cmdName, logPath)
	// The file's own basename is what fitPath keeps regardless of how much
	// of the rest gets elided at any given width (see fitPath's own
	// mid-segment elision) — the substring every width still shows.
	const logBase = "worker-termfixture-logpath.log"

	out := Frame(context.Background(), New(context.Background(), dir, "owner/repo", "main", "main"), FrameOptions{
		Width: 100, Height: 24,
		Keys:   []string{"down", "a"},
		Settle: 300 * time.Millisecond,
	})
	lines := strings.Split(stripANSI(out), "\n")
	if len(lines) < 5 {
		t.Fatalf("attached frame too short: %q", lines)
	}
	if !strings.Contains(lines[1], "attached") || strings.Contains(lines[1], logBase) {
		t.Errorf("row 1 = %q, want the breadcrumb without the log path", lines[1])
	}
	if !strings.Contains(lines[2], "log") || !strings.Contains(lines[2], "/Users/") || !strings.Contains(lines[2], logBase) {
		t.Errorf("row 2 = %q, want the absolute log path under the breadcrumb", lines[2])
	}
	if !strings.Contains(lines[3], "url") || !strings.Contains(lines[3], "fixture-url") {
		t.Errorf("row 3 = %q, want the configured url under the log row", lines[3])
	}
	if !strings.Contains(lines[4], "╌") {
		t.Errorf("row 4 = %q, want the dashed rule under the field rows", lines[4])
	}
	for i, l := range lines {
		if i != 2 && strings.Contains(l, logBase) {
			t.Errorf("log path also appears on row %d: %q", i, l)
		}
	}

	// The child's pty gives up the two field rows: 24 total, minus header,
	// breadcrumb, log, url, dashed rule, solid rule and key hint = 17.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.resizes) == 0 || rec.resizes[len(rec.resizes)-1][0] != 17 {
		t.Errorf("pty resizes = %v, want the last one to be 17 rows", rec.resizes)
	}
}

// TestAttachedViewOutputReachesFloor is the ticket's third acceptance
// criterion: the child's output reaches the last row above the key hint —
// the bottom output row is never blank when the entry has output. It also
// proves the "gap above rather than below" rule: a short tail leaves blank
// rows ABOVE the content, not below it.
func TestAttachedViewOutputReachesFloor(t *testing.T) {
	cmdName := "termfixture-floor"
	dir := t.TempDir()
	writeFixtureManifest(t, dir, cmdName)
	pty := registerFakePTYEntry(t, cmdName, livework.NewRing(0), &termRecorder{})

	p := newAttachablePump(t, dir, 96, 30)
	p.apply(keyMsg("a"))
	p.drain(150 * time.Millisecond)
	if _, err := pty.Write([]byte("only line\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p.drain(300 * time.Millisecond)

	out := p.model.View()
	lines := strings.Split(out, "\n")
	// Row layout: header(1) + breadcrumb(1) + log row(1) + dashed rule(1) +
	// output + solid rule(1) + key hint(1) = len(lines). The row directly
	// above the solid rule (second-to-last) is the floor the child's last
	// output line must occupy.
	if len(lines) < 3 {
		t.Fatalf("frame too short: %d lines", len(lines))
	}
	floor := lines[len(lines)-3]
	if strings.TrimSpace(floor) == "" {
		t.Errorf("bottom output row is blank though the entry has output: %q", floor)
	}
	if !strings.Contains(floor, "only line") {
		t.Errorf("bottom output row = %q, want it to hold the child's last written line", floor)
	}
	// And the row right after the dashed rule (the top of the output
	// region) must be blank — the short tail's gap sits above, not below.
	ruleIdx := -1
	for i, l := range lines {
		if strings.Contains(l, "╌╌╌") {
			ruleIdx = i
			break
		}
	}
	if ruleIdx == -1 || ruleIdx+1 >= len(lines) {
		t.Fatalf("could not find the dashed rule in the frame: %q", lines)
	}
	if strings.TrimSpace(lines[ruleIdx+1]) != "" {
		t.Errorf("row right after the dashed rule = %q, want blank (gap floats above the tail, not below)", lines[ruleIdx+1])
	}
}

// TestAttachedViewSwitchesToDifferentEntry is the ticket's explicit
// interface requirement: attaching to one entry, detaching, then attaching
// to a DIFFERENT entry must show the second entry. A hardcoded breadcrumb
// or body would pass a "does it render" check vacuously while showing the
// wrong process — this proves the render is keyed off the live entry each
// attach records, not a cached first attach.
func TestAttachedViewSwitchesToDifferentEntry(t *testing.T) {
	dir := t.TempDir()
	toml := "project_name = \"fixture\"\n\n" +
		"[commands.termfixture-first]\ndesc = \"fixture\"\nsteps = [\"termfixture-first\"]\n\n" +
		"[actions.termfixture-first]\nkind = \"shell\"\nrun = \"true\"\n\n" +
		"[commands.termfixture-second]\ndesc = \"fixture\"\nsteps = [\"termfixture-second\"]\n\n" +
		"[actions.termfixture-second]\nkind = \"shell\"\nrun = \"true\"\n"
	if err := os.WriteFile(filepath.Join(dir, manifest.Filename), []byte(toml), 0o644); err != nil {
		t.Fatalf("writing fixture admin.toml: %v", err)
	}
	freshLivework(t)
	ring1 := livework.NewRing(0)
	if _, err := ring1.Write([]byte("output from first\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	ring2 := livework.NewRing(0)
	if _, err := ring2.Write([]byte("output from second\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	for _, e := range []*livework.Entry{
		{ID: "termfixture-first", Name: "termfixture-first", Kind: "hosted-pty", Handle: "termfixture-first-pty", State: livework.StateRunning, Output: ring1, Ops: livework.Ops{Kill: func() error { return nil }, Write: (&termRecorder{}).write, Resize: (&termRecorder{}).resize}},
		{ID: "termfixture-second", Name: "termfixture-second", Kind: "hosted-pty", Handle: "termfixture-second-pty", State: livework.StateRunning, Output: ring2, Ops: livework.Ops{Kill: func() error { return nil }, Write: (&termRecorder{}).write, Resize: (&termRecorder{}).resize}},
	} {
		if err := livework.Default.Add(e); err != nil {
			t.Fatalf("Add(%s): %v", e.ID, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := New(ctx, dir, "owner/repo", "main", "main")
	m.herdr = false
	p := &pump{msgs: make(chan tea.Msg, 64), model: m}
	p.apply(tea.WindowSizeMsg{Width: 100, Height: 24})
	p.run(p.model.Init())
	p.drain(2 * time.Second)
	p.apply(keyMsg("down"))
	p.drain(150 * time.Millisecond)

	// Attach to the first row (termfixture-first, cursor's default position).
	p.apply(keyMsg("a"))
	p.drain(150 * time.Millisecond)
	if !p.model.termAttached || p.model.termEntryID != "termfixture-first" {
		t.Fatalf("did not attach to termfixture-first, got termAttached=%v termEntryID=%q", p.model.termAttached, p.model.termEntryID)
	}
	if _, err := ring1.Write([]byte("more from first\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p.drain(300 * time.Millisecond)
	out := p.model.View()
	if !strings.Contains(out, "termfixture-first") {
		t.Fatalf("attached frame does not name the first entry: %q", out)
	}

	// Detach.
	p.apply(keyMsg("esc"))
	p.drain(150 * time.Millisecond)
	if p.model.termAttached {
		t.Fatal("esc did not detach")
	}
	if p.model.termEntryID != "" {
		t.Errorf("detach left termEntryID = %q, want cleared", p.model.termEntryID)
	}

	// Move the cursor to the second row and attach to it.
	p.apply(keyMsg("down"))
	p.drain(150 * time.Millisecond)
	p.apply(keyMsg("a"))
	p.drain(150 * time.Millisecond)
	if !p.model.termAttached || p.model.termEntryID != "termfixture-second" {
		t.Fatalf("did not attach to termfixture-second, got termAttached=%v termEntryID=%q", p.model.termAttached, p.model.termEntryID)
	}
	if _, err := ring2.Write([]byte("more from second\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p.drain(300 * time.Millisecond)
	out = p.model.View()
	if strings.Contains(out, "output from first") || strings.Contains(out, "more from first") {
		t.Errorf("second attach still shows the FIRST entry's output: %q", out)
	}
	if !strings.Contains(out, "termfixture-second") {
		t.Errorf("second attach's breadcrumb does not name termfixture-second: %q", out)
	}
}
