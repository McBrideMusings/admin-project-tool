// quitconfirm_test.go pins admin-3rgp.63's acceptance criteria: quitting
// with nothing hosted exits immediately, quitting with a hosted (Via-empty)
// process open a confirm listing it (and only it — not a launcher-dispatched
// entry, not an agent worker), accepting kills every listed entry then
// quits, and any other key cancels leaving every entry untouched.
package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// TestHostedRunningEntriesFiltersByAddressAndState proves the "hosted" set
// is exactly Via-empty, non-worker entries in Running/Waiting — a Via-set
// entry (a launcher-dispatched pane) and a Worker entry must never be
// listed even though both could otherwise pass a naive "is something
// running" check, and an exited hosted entry has nothing left to warn
// about.
func TestHostedRunningEntriesFiltersByAddressAndState(t *testing.T) {
	freshLivework(t)
	hosted := &livework.Entry{
		ID: "quitconfirm-hosted-running", Name: "hosted-running",
		Handle: "pty-1",
	}
	waiting := &livework.Entry{
		ID: "quitconfirm-hosted-waiting", Name: "hosted-waiting",
		Handle: "pty-2",
	}
	exited := &livework.Entry{
		ID: "quitconfirm-hosted-exited", Name: "hosted-exited",
		Handle: "pty-3",
	}
	paned := &livework.Entry{
		ID: "quitconfirm-paned", Name: "paned",
		Handle: "pane-1", Via: "terminal",
	}
	worker := &livework.Entry{
		ID: "quitconfirm-worker", Name: "worker",
		Handle: "pane-2", Via: "terminal",
		Worker: &livework.WorkerInfo{Branch: "b", Worktree: "/tmp/b"},
	}
	for _, e := range []*livework.Entry{hosted, waiting, exited, paned, worker} {
		if err := livework.Default.Add(e); err != nil {
			t.Fatalf("Add(%s): %v", e.ID, err)
		}
	}
	livework.Default.SetState(waiting.ID, livework.StateWaiting, nil)
	livework.Default.SetState(exited.ID, livework.StateExited, nil)
	got := hostedRunningEntries()
	names := map[string]bool{}
	for _, e := range got {
		names[e.Name] = true
	}
	if !names["hosted-running"] {
		t.Error("hosted-running (Via empty, StateRunning) missing from hostedRunningEntries")
	}
	if !names["hosted-waiting"] {
		t.Error("hosted-waiting (Via empty, StateWaiting) missing from hostedRunningEntries")
	}
	if names["hosted-exited"] {
		t.Error("hosted-exited (Via empty, StateExited) present, want excluded — nothing to warn about")
	}
	if names["paned"] {
		t.Error("paned (Via set) present, want excluded — launcher-dispatched entries outlive the dashboard on purpose")
	}
	if names["worker"] {
		t.Error("worker (Worker != nil) present, want excluded — agent workers outlive the dashboard on purpose")
	}
}

// TestRequestQuitWithNothingHostedQuitsImmediately proves the no-hosted-work
// path is unchanged from before this feature: no overlay, a Cmd that
// produces tea.QuitMsg.
func TestRequestQuitWithNothingHostedQuitsImmediately(t *testing.T) {
	freshLivework(t)
	m := workTestModel(nil)
	updated, cmd := m.requestQuit()
	um := updated.(Model)
	if um.quitOpen {
		t.Error("quitOpen = true with nothing hosted, want false — no confirm when there is nothing to warn about")
	}
	if cmd == nil {
		t.Fatal("requestQuit returned a nil Cmd with nothing hosted, want a quit Cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("requestQuit's Cmd produced a %T, want tea.QuitMsg", cmd())
	}
}

// TestRequestQuitWithHostedProcessOpensConfirm proves a hosted, running
// entry blocks the immediate quit and opens the overlay over a snapshot of
// what's running instead.
func TestRequestQuitWithHostedProcessOpensConfirm(t *testing.T) {
	freshLivework(t)
	e := &livework.Entry{
		ID: "quitconfirm-opens-confirm", Name: "opens-confirm",
		Handle: "pty-opens-confirm",
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m := workTestModel(nil)
	updated, cmd := m.requestQuit()
	um := updated.(Model)
	if !um.quitOpen {
		t.Error("quitOpen = false with a hosted process running, want true")
	}
	if cmd != nil {
		t.Error("requestQuit returned a non-nil Cmd while opening the confirm, want nil — nothing quits yet")
	}
	found := false
	for _, h := range um.quitHosted {
		if h.ID == e.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("quitHosted = %v, want it to contain %q", um.quitHosted, e.ID)
	}
}

// TestQuitConfirmKeyEnterKillsAndQuits proves accepting the confirm calls
// Kill (via the registry, routed to the entry's own Ops.Kill) on every
// snapshotted entry and then returns a quit Cmd.
func TestQuitConfirmKeyEnterKillsAndQuits(t *testing.T) {
	freshLivework(t)
	killed := false
	e := &livework.Entry{
		ID: "quitconfirm-enter-kills", Name: "enter-kills",
		Handle: "pty-enter-kills",
		Ops:    livework.Ops{Kill: func() error { killed = true; return nil }},
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m := workTestModel(nil)
	m.quitOpen = true
	m.quitHosted = []*livework.Entry{e}

	updated, cmd := m.quitConfirmKey("enter")
	um := updated.(Model)
	if !killed {
		t.Error("quitConfirmKey(\"enter\") did not call the entry's Kill op")
	}
	if um.quitOpen {
		t.Error("quitOpen still true after enter, want false")
	}
	if cmd == nil {
		t.Fatal("quitConfirmKey(\"enter\") returned a nil Cmd, want a quit Cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("quitConfirmKey(\"enter\")'s Cmd produced a %T, want tea.QuitMsg", cmd())
	}
}

// TestQuitConfirmKeyOtherCancelsWithoutKilling proves any key besides
// enter/y/Y cancels: the overlay closes, the entry is never killed, and its
// state is untouched (still StateRunning).
func TestQuitConfirmKeyOtherCancelsWithoutKilling(t *testing.T) {
	freshLivework(t)
	killed := false
	e := &livework.Entry{
		ID: "quitconfirm-esc-cancels", Name: "esc-cancels",
		Handle: "pty-esc-cancels",
		Ops:    livework.Ops{Kill: func() error { killed = true; return nil }},
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m := workTestModel(nil)
	m.quitOpen = true
	m.quitHosted = []*livework.Entry{e}

	updated, cmd := m.quitConfirmKey("esc")
	um := updated.(Model)
	if killed {
		t.Error("quitConfirmKey(\"esc\") called Kill, want it left untouched")
	}
	if um.quitOpen {
		t.Error("quitOpen still true after esc, want false")
	}
	if cmd != nil {
		t.Error("quitConfirmKey(\"esc\") returned a non-nil Cmd, want nil — cancelling must not quit")
	}
	if st, _ := livework.Default.State(e.ID); st != livework.StateRunning {
		t.Errorf("entry state = %v after cancel, want %v — still running", st, livework.StateRunning)
	}
}

// TestFrameShowsQuitConfirmOverHostedProcess is the acceptance criterion's
// required headless-frame assertion: a frame taken after pressing "q" with a
// hosted process running must show the confirm, the process's name in the
// Work band's spaced form, and a footer naming only the confirm's own keys.
func TestFrameShowsQuitConfirmOverHostedProcess(t *testing.T) {
	freshLivework(t)
	e := &livework.Entry{
		ID: "frame-fixture:cf", Name: "frame-fixture:cf",
		Handle: "pty-frame-entry",
		Ops:    livework.Ops{Kill: func() error { return nil }},
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}
	livework.Default.SetActivity(e.ID, "vitepress dev · localhost:5173")

	// The entry's name comes from its Work row, so the project declares it.
	dir := t.TempDir()
	toml := `project_name = "frame"

[commands.frame-fixture]
desc  = "fixture"
steps = ["frame-fixture"]

[actions.frame-fixture]
kind = "multi-target"

[actions.frame-fixture.cf]
run = "true"

[actions.frame-fixture.mac]
run = "true"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, dir, "owner/repo", "main", "main")
	m.herdr = false

	out := Frame(ctx, m, FrameOptions{
		Width: 100, Height: 24,
		Keys:   []string{"q"},
		Settle: 200 * time.Millisecond,
	})

	if !strings.Contains(out, "Quit") {
		t.Errorf("frame after q with a hosted process running lost the quit-confirm title: %q", out)
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "running  frame-fixture cf  vitepress dev") {
		t.Errorf("frame after q is missing the process row (state, spaced name, activity): %q", plain)
	}
	lines := strings.Split(plain, "\n")
	if got, want := strings.TrimRight(lines[len(lines)-1], " "), " ⏎ kill them and quit · esc cancel"; got != want {
		t.Errorf("footer under the quit confirm = %q, want %q", got, want)
	}
}

// TestQuitConfirmMatchesGolden compares the confirm box cell for cell with
// testdata/quit-confirm-two.txt (96x30), fed the same two hosted processes
// the golden frame lists, and checks the 80x24 placement follows the same
// centering.
func TestQuitConfirmMatchesGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/quit-confirm-two.txt")
	if err != nil {
		t.Fatalf("read golden frame: %v", err)
	}
	design := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	freshLivework(t)
	for _, e := range []*livework.Entry{
		{ID: "dev", Name: "dev", Handle: "pty-dev"},
		{ID: "test", Name: "test", Handle: "pty-test"},
	} {
		e.Ops = livework.Ops{Kill: func() error { return nil }}
		if err := livework.Default.Add(e); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	livework.Default.SetActivity("dev", "vitepress dev · localhost:5173")
	livework.Default.SetState("test", livework.StateWaiting, nil)
	livework.Default.SetActivity("test", "Enter a number, or q to cancel:")

	const boxW, boxH = 62, 10
	for _, geo := range []struct{ w, h, top, left int }{
		{96, 30, 10, 17},
		{80, 24, 7, 9},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		m := New(ctx, t.TempDir(), "owner/repo", "main", "main")
		m.herdr = false
		out := stripANSI(Frame(ctx, m, FrameOptions{
			Width: geo.w, Height: geo.h, Keys: []string{"q"}, Settle: 200 * time.Millisecond,
		}))
		cancel()
		lines := strings.Split(out, "\n")
		if len(lines) != geo.h {
			t.Fatalf("%dx%d: frame has %d rows, want %d", geo.w, geo.h, len(lines), geo.h)
		}
		for i := 0; i < boxH; i++ {
			got := cells(lines[geo.top+i], geo.left, geo.left+boxW)
			want := cells(design[10+i], 17, 17+boxW)
			if got != want {
				t.Errorf("%dx%d box row %d = %q, want %q", geo.w, geo.h, i, got, want)
			}
		}
	}
}

// TestQuitConfirmCapsLongList proves a confirm listing more hosted processes
// than the screen has rows for keeps its title and action row on screen: at
// 96x30 the box matches testdata/quit-confirm-twenty.txt cell for cell, fed
// the same twenty processes, and at 80x24 it sits flush under the tab
// separator and over the footer rule, listing ten and counting the other ten.
func TestQuitConfirmCapsLongList(t *testing.T) {
	raw, err := os.ReadFile("testdata/quit-confirm-twenty.txt")
	if err != nil {
		t.Fatalf("read golden frame: %v", err)
	}
	design := strings.Split(stripANSI(strings.TrimRight(string(raw), "\n")), "\n")

	freshLivework(t)
	procs := [][3]string{
		{"dev", "running", "vitepress dev · localhost:5173"},
		{"test", "waiting", "Enter a number, or q to cancel:"},
		{"api", "running", "bun --watch src/server.ts"},
		{"web", "running", "next dev · localhost:3000"},
		{"stories", "running", "storybook dev · localhost:6006"},
		{"docs", "running", "vitepress dev · localhost:5174"},
		{"worker", "running", "wrangler dev · localhost:8788"},
		{"queue", "running", "bullmq worker · 3 jobs active"},
		{"db", "running", "postgres · localhost:5432"},
		{"redis", "running", "redis-server · localhost:6379"},
		{"tailwind", "running", "tailwindcss --watch"},
		{"tsc", "running", "tsc --watch · 0 errors"},
		{"e2e", "running", "playwright test --ui"},
		{"mail", "running", "mailpit · localhost:8025"},
		{"proxy", "running", "caddy run · :443"},
		{"metro", "running", "expo start · localhost:8081"},
		{"ios", "running", "xcrun simctl log stream"},
		{"android", "running", "adb logcat"},
		{"lint", "running", "eslint --watch · 2 warnings"},
		{"tunnel", "running", "cloudflared tunnel run"},
	}
	for _, p := range procs {
		e := &livework.Entry{ID: p[0], Name: p[0], Handle: "pty-" + p[0]}
		e.Ops = livework.Ops{Kill: func() error { return nil }}
		if err := livework.Default.Add(e); err != nil {
			t.Fatalf("Add: %v", err)
		}
		livework.Default.SetState(e.ID, livework.State(p[1]), nil)
		livework.Default.SetActivity(e.ID, p[2])
	}

	frame := func(w, h int) []string {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		m := New(ctx, t.TempDir(), "owner/repo", "main", "main")
		m.herdr = false
		return strings.Split(stripANSI(Frame(ctx, m, FrameOptions{
			Width: w, Height: h, Keys: []string{"q"}, Settle: 200 * time.Millisecond,
		})), "\n")
	}

	const boxW = 62
	lines := frame(96, 30)
	for y := 3; y < 28; y++ {
		if got, want := cells(lines[y], 17, 17+boxW), cells(design[y], 17, 17+boxW); got != want {
			t.Errorf("96x30 row %d = %q, want %q", y, got, want)
		}
	}

	lines = frame(80, 24)
	for y, want := range map[int]string{
		3:  "┌",
		4:  "│  Quit — 20 processes still running",
		16: "│    +10 more running",
		20: "│    ⏎ kill them and quit     esc cancel",
		21: "└",
	} {
		if got := cells(lines[y], 9, 9+boxW); !strings.HasPrefix(got, want) {
			t.Errorf("80x24 row %d = %q, want it to start %q", y, got, want)
		}
	}
}

// cells returns the runes in columns [a, b) of a plain line, space-padded.
func cells(s string, a, b int) string {
	r := []rune(s)
	for len(r) < b {
		r = append(r, ' ')
	}
	return string(r[a:b])
}
