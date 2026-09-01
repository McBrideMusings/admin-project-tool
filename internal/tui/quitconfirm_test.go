// quitconfirm_test.go pins admin-3rgp.63's acceptance criteria: quitting
// with nothing hosted exits immediately, quitting with a hosted pty-addressed
// process open a confirm listing it (and only it — not a pane-addressed
// entry, not an agent worker), accepting kills every listed entry then
// quits, and any other key cancels leaving every entry untouched.
package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// TestHostedRunningEntriesFiltersByAddressAndState proves the "hosted" set
// is exactly PTYID-addressed, non-worker entries in Running/Waiting — a
// PaneID entry (herdr pane) and a Worker entry are pane-addressed and must
// never be listed even though both could otherwise pass a naive "is
// something running" check, and an exited PTYID entry has nothing left to
// warn about.
func TestHostedRunningEntriesFiltersByAddressAndState(t *testing.T) {
	hosted := &livework.Entry{
		ID: "quitconfirm-hosted-running", Name: "hosted-running",
		PTYID: "pty-1", State: livework.StateRunning,
	}
	waiting := &livework.Entry{
		ID: "quitconfirm-hosted-waiting", Name: "hosted-waiting",
		PTYID: "pty-2", State: livework.StateWaiting,
	}
	exited := &livework.Entry{
		ID: "quitconfirm-hosted-exited", Name: "hosted-exited",
		PTYID: "pty-3", State: livework.StateExited,
	}
	paned := &livework.Entry{
		ID: "quitconfirm-paned", Name: "paned",
		PaneID: "pane-1", State: livework.StateRunning,
	}
	worker := &livework.Entry{
		ID: "quitconfirm-worker", Name: "worker",
		PaneID: "pane-2", State: livework.StateRunning,
		Worker: &livework.WorkerInfo{Branch: "b", Worktree: "/tmp/b"},
	}
	for _, e := range []*livework.Entry{hosted, waiting, exited, paned, worker} {
		if err := livework.Default.Add(e); err != nil {
			t.Fatalf("Add(%s): %v", e.ID, err)
		}
	}
	// livework.Default is a process-wide singleton with no Remove — leaving
	// these StateRunning/StateWaiting so later tests in this package see
	// them too would make hostedRunningEntries() non-empty for every test
	// that runs after this one, including the "nothing hosted" cases below.
	t.Cleanup(func() {
		livework.Default.SetState(hosted.ID, livework.StateExited, nil)
		livework.Default.SetState(waiting.ID, livework.StateExited, nil)
	})

	got := hostedRunningEntries()
	names := map[string]bool{}
	for _, e := range got {
		names[e.Name] = true
	}
	if !names["hosted-running"] {
		t.Error("hosted-running (PTYID, StateRunning) missing from hostedRunningEntries")
	}
	if !names["hosted-waiting"] {
		t.Error("hosted-waiting (PTYID, StateWaiting) missing from hostedRunningEntries")
	}
	if names["hosted-exited"] {
		t.Error("hosted-exited (PTYID, StateExited) present, want excluded — nothing to warn about")
	}
	if names["paned"] {
		t.Error("paned (PaneID) present, want excluded — pane-backed entries outlive the dashboard on purpose")
	}
	if names["worker"] {
		t.Error("worker (Worker != nil) present, want excluded — agent workers outlive the dashboard on purpose")
	}
}

// TestRequestQuitWithNothingHostedQuitsImmediately proves the no-hosted-work
// path is unchanged from before this feature: no overlay, a Cmd that
// produces tea.QuitMsg.
func TestRequestQuitWithNothingHostedQuitsImmediately(t *testing.T) {
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
	e := &livework.Entry{
		ID: "quitconfirm-opens-confirm", Name: "opens-confirm",
		PTYID: "pty-opens-confirm", State: livework.StateRunning,
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Cleanup(func() { livework.Default.SetState(e.ID, livework.StateExited, nil) })

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
	killed := false
	e := &livework.Entry{
		ID: "quitconfirm-enter-kills", Name: "enter-kills",
		PTYID: "pty-enter-kills", State: livework.StateRunning,
		Ops: livework.Ops{Kill: func() error { killed = true; return nil }},
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// The stub Kill above never touches the registry's own State (a real
	// Ops.Kill's producer does that from outside); this test's own kill call
	// must not leave the entry looking StateRunning for every test that
	// runs after it against the same process-wide singleton.
	t.Cleanup(func() { livework.Default.SetState(e.ID, livework.StateExited, nil) })

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
	killed := false
	e := &livework.Entry{
		ID: "quitconfirm-esc-cancels", Name: "esc-cancels",
		PTYID: "pty-esc-cancels", State: livework.StateRunning,
		Ops: livework.Ops{Kill: func() error { killed = true; return nil }},
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Cleanup(func() { livework.Default.SetState(e.ID, livework.StateExited, nil) })

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
	if got, _ := livework.Default.Get(e.ID); got.State != livework.StateRunning {
		t.Errorf("entry state = %v after cancel, want %v — still running", got.State, livework.StateRunning)
	}
}

// TestFrameShowsQuitConfirmOverHostedProcess is the acceptance criterion's
// required headless-frame assertion: a frame taken after pressing "q" with a
// hosted process running must show the confirm and the process's name.
func TestFrameShowsQuitConfirmOverHostedProcess(t *testing.T) {
	e := &livework.Entry{
		ID: "quitconfirm-frame-entry", Name: "frame-hosted-fixture",
		PTYID: "pty-frame-entry", State: livework.StateRunning,
		Ops: livework.Ops{Kill: func() error { return nil }},
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Cleanup(func() { livework.Default.SetState(e.ID, livework.StateExited, nil) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, t.TempDir(), "owner/repo", "main", "main")
	m.herdr = false

	out := Frame(ctx, m, FrameOptions{
		Width: 100, Height: 24,
		Keys:   []string{"q"},
		Settle: 200 * time.Millisecond,
	})

	if !strings.Contains(out, "Quit") {
		t.Errorf("frame after q with a hosted process running lost the quit-confirm title: %q", out)
	}
	if !strings.Contains(out, "frame-hosted-fixture") {
		t.Errorf("frame after q with a hosted process running is missing the process name: %q", out)
	}
}
