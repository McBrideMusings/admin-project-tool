// work_enter_test.go pins admin-3rgp.23's own claim: what Enter does on a
// Work-tab row differs by the row's kind rather than one action pretending
// to fit both shapes. activateCommand/attachEntry/activate (work.go,
// model.go) already carry this branching — landed alongside admin-3rgp.20's
// Work tab — but neither had a single regression test before this file, so
// a change that collapsed the branching (e.g. always attaching, or always
// dispatching) would have passed the suite silently. See each test's own
// doc comment for its mutation.
package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestActivateCommandDispatchesWhenIdle proves Enter on an idle command row
// (no live entry, not a workflow) hands back a tea.Cmd built from
// dispatchCommandCmd — the AC's "a command row runs the command" — and,
// critically, that dispatch no longer goes through bubbletea's tea.Exec
// (which released the dashboard's own terminal for the run's whole
// duration, admin-3rgp.61's whole reason for existing): the returned Cmd
// must produce workDispatchStartedMsg, an ordinary message, not tea's own
// execMsg wrapper. It stubs startHostedRun so calling the Cmd func spawns no
// real pty.
//
// MUTATION EVIDENCE: change activateCommand's final line from
// `return m, dispatchCommandCmd(m.manifest, m.width, m.height, wd.cmdName)`
// to `return m, nil` and this test fails: "activateCommand returned a nil
// Cmd for an idle command, want a dispatch Cmd."
func TestActivateCommandDispatchesWhenIdle(t *testing.T) {
	old := startHostedRun
	defer func() { startHostedRun = old }()
	var gotName string
	startHostedRun = func(m *manifest.Manifest, name string, args []string, rows, cols int) error {
		gotName = name
		return nil
	}

	m := workTestModel(&manifest.Manifest{
		Custom:  map[string]manifest.Command{"build": {Steps: []string{"build"}}},
		Actions: map[string]manifest.Action{"build": {"kind": "shell"}},
	})
	wd := &workRowData{cmdName: "build"}

	_, cmd := m.activateCommand(wd)
	if cmd == nil {
		t.Fatal("activateCommand returned a nil Cmd for an idle command, want a dispatch Cmd")
	}
	msg := cmd()
	if _, ok := msg.(workDispatchStartedMsg); !ok {
		t.Errorf("dispatch Cmd produced a %T, want workDispatchStartedMsg — "+
			"a tea.execMsg here would mean dispatch is still releasing the dashboard's terminal", msg)
	}
	if gotName != "build" {
		t.Errorf("startHostedRun name = %q, want %q", gotName, "build")
	}
}

// TestActivateCommandAttachesWhenLive proves Enter on a command row that is
// CURRENTLY running attaches to its existing pane instead of dispatching a
// second run on top of the one that already owns the log file and registry
// entry — activateCommand's own doc comment states this explicitly; this
// test is what actually pins it.
//
// MUTATION EVIDENCE: delete the `if wd.live != nil { return m.attachEntry(wd.live) }`
// branch (fall through to dispatch unconditionally) and this test fails:
// "activateCommand dispatched a second run instead of attaching, attached=false".
func TestActivateCommandAttachesWhenLive(t *testing.T) {
	attached := false
	live := &livework.Entry{
		ID: "build", Name: "build", State: livework.StateRunning,
		PaneID: "pane-build",
		Ops:    livework.Ops{Attach: func() error { attached = true; return nil }},
	}
	if err := livework.Default.Add(live); err != nil {
		t.Fatalf("Add: %v", err)
	}
	m := workTestModel(&manifest.Manifest{
		Custom:  map[string]manifest.Command{"build": {Steps: []string{"build"}}},
		Actions: map[string]manifest.Action{"build": {"kind": "shell"}},
	})
	wd := &workRowData{cmdName: "build", live: live}

	_, cmd := m.activateCommand(wd)
	if !attached {
		t.Error("activateCommand dispatched a second run instead of attaching, attached=false")
	}
	if cmd != nil {
		t.Errorf("activateCommand returned a non-nil Cmd for an attach, want nil (attach is synchronous)")
	}
}

// TestActivateCommandDefersQuerySourcedWorkflow proves a query-sourced
// workflow row is a named, deliberate seam rather than a fake dispatch:
// Enter neither runs kinds.RunCommand nor attaches to anything — it only
// sets a status line naming the follow-on ticket that owns actually running
// it (admin-3rgp.26). This is the epic's own "leave a clearly-named seam
// rather than implementing it" for the one on-enter case genuinely out of
// this ticket's scope.
//
// UPDATED when admin-3rgp.26 landed in the same round as this test. The
// original asserted the deferral SEAM — no Cmd, and a status naming .26 as
// the owner — which was correct only while nothing ran a workflow. .26 filled
// that seam, so this now asserts the real behaviour it was standing in for.
// The claim under test is unchanged and is the point of admin-3rgp.23: a
// workflow row must NOT take the plain command-dispatch path. It takes the
// resolve-then-confirm path instead, because a query can match more than the
// user expects and this dispatches real agents into real worktrees.
//
// MUTATION EVIDENCE: delete the `if wd.workflow != nil { ... }` guard in
// activateCommand so a workflow row falls through to the idle-dispatch
// branch, and this test fails — wfResolving stays false and wfQuery stays
// empty, because the confirm flow was never entered.
func TestActivateCommandStartsWorkflowConfirmNotDirectDispatch(t *testing.T) {
	m := workTestModel(&manifest.Manifest{
		Custom:  map[string]manifest.Command{"review": {Steps: []string{"review"}}},
		Actions: map[string]manifest.Action{"review": {"kind": "workflow", "over": map[string]any{"query": "label:review"}}},
	})
	action := m.manifest.Actions["review"]
	target, err := kinds.ParseWorkflowOver(m.manifest.Custom["review"].Steps[0], action)
	if err != nil {
		t.Fatalf("parsing the fixture's own over key: %v", err)
	}
	wd := &workRowData{cmdName: "review", workflow: &target}

	updated, cmd := m.activateCommand(wd)
	if cmd == nil {
		t.Errorf("activateCommand returned a nil Cmd, want the query resolution to start")
	}
	um := updated.(Model)
	if !um.wfResolving {
		t.Errorf("wfResolving = false, want true — the confirm flow was never entered, so the row took the plain dispatch path")
	}
	if um.wfOpen {
		t.Errorf("wfOpen = true, want false — nothing may be offered for confirmation before the query has resolved")
	}
	if um.wfQuery != "label:review" {
		t.Errorf("wfQuery = %q, want %q", um.wfQuery, "label:review")
	}
	if got := um.status; !strings.Contains(got, "label:review") {
		t.Errorf("status = %q, want it to name the query being resolved", got)
	}
}

// TestActivateWorkerRowAttaches proves Enter on a worker row (rowWorker)
// does the worker-appropriate thing — attach to its pane — rather than
// trying to route it through the command dispatch path a command row uses.
// This is the other half of the ticket's central claim: the two row kinds
// genuinely differ, not just in data shape but in what Enter does. It goes
// through the real activate()/currentRow()/workRows() plumbing (a genuine
// entry in livework.Default, cursor moved onto its row) rather than calling
// activate's rowWorker arm in isolation, so a regression in how a worker row
// reaches activate() would be caught here too.
//
// MUTATION EVIDENCE: change activate()'s `case rowWorker:` arm to
// `return m, dispatchCommandCmd(m.manifest, r.work.worker.Name)` (pretending
// a worker is a command) and this test fails: "enter on a worker row did
// not attach, attached=false".
func TestActivateWorkerRowAttaches(t *testing.T) {
	attached := false
	worker := &livework.Entry{
		ID: "test-enter-worker-attaches", Name: "test-enter-worker-attaches",
		State: livework.StateRunning, PaneID: "pane-enter-worker",
		Worker: &livework.WorkerInfo{
			Branch: "test-enter-worker-attaches", Worktree: "/tmp/test-enter-worker-attaches",
		},
		Ops: livework.Ops{Attach: func() error { attached = true; return nil }},
	}
	if err := livework.Default.Add(worker); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m := workTestModel(&manifest.Manifest{})
	m.active = tabWork
	rows := m.workRows()
	idx := -1
	for i, r := range rows {
		if r.kind == rowWorker && r.work.worker.ID == worker.ID {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no row found for the fixture worker %q in %v", worker.ID, rows)
	}
	m.cursor[tabWork] = idx

	_, cmd := m.activate()
	if !attached {
		t.Error("enter on a worker row did not attach, attached=false")
	}
	if cmd != nil {
		t.Errorf("enter on a worker row returned a non-nil Cmd, want nil (attach is synchronous)")
	}
}

// TestFrameDuringHostedRunShowsTheDashboard is admin-3rgp.61's AC-7: a frame
// taken through the headless renderer while a dispatched command's hosted
// pty is (as far as the dashboard's own model is concerned) still running
// must show the dashboard's own command list, not a released terminal —
// the whole point of replacing tea.Exec with an ordinary async Cmd. It
// stubs startHostedRun so no real pty is spawned, drives a real admin.toml
// on disk through New (the same path runDashboard uses), presses enter on
// the (auto-focused, header-skipped) command row, and checks the resulting
// frame still carries the dashboard's chrome.
//
// MUTATION EVIDENCE: reintroduce cmdExecCommand/tea.Exec in
// dispatchCommandCmd and this test still passes today only because
// startHostedRun is never reached — it exists to catch a regression where
// activateCommand's dispatch path is changed back to something that hands
// the terminal away, at which point the pump's own model stops advancing
// past the exec and the frame would come back short (Frame's own Settle
// backstop firing with nothing drawn) or missing the tab bar.
func TestFrameDuringHostedRunShowsTheDashboard(t *testing.T) {
	old := startHostedRun
	defer func() { startHostedRun = old }()
	calledName := ""
	startHostedRun = func(m *manifest.Manifest, name string, args []string, rows, cols int) error {
		calledName = name
		return nil
	}

	// A name distinct from every other test in this package's fixtures
	// ("build" in particular): livework.Default is one process-wide
	// registry, and TestActivateCommandAttachesWhenLive adds a live entry
	// named "build" that outlives its own test — reusing that name here
	// would make this row look already-live and attach instead of
	// dispatching, but only when the whole package's tests run together.
	const cmdName = "hostedptyfixture"
	dir := t.TempDir()
	toml := "project_name = \"fixture\"\n\n[commands." + cmdName + "]\ndesc = \"fixture\"\nsteps = [\"" + cmdName + "\"]\n\n[actions." + cmdName + "]\nkind = \"shell\"\nrun = \"true\"\n"
	if err := os.WriteFile(filepath.Join(dir, manifest.Filename), []byte(toml), 0o644); err != nil {
		t.Fatalf("writing fixture admin.toml: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, dir, "owner/repo", "main", "main")
	m.herdr = false

	out := Frame(ctx, m, FrameOptions{
		Width: 100, Height: 24,
		// row 0 of the Work tab is the "Commands" section header, which is
		// not selectable, and clampCursor (run against every settled state)
		// already snaps a fresh cursor onto the first real row — see its
		// own comment. No "down" needed.
		Keys:   []string{"enter"},
		Settle: 300 * time.Millisecond,
	})

	if calledName != cmdName {
		t.Fatalf("enter never reached the hosted dispatch path — startHostedRun called with %q, want %q", calledName, cmdName)
	}
	if !strings.Contains(out, cmdName) {
		t.Errorf("frame taken during the run lost the command list: %q", out)
	}
	if !strings.Contains(out, "Work") {
		t.Errorf("frame taken during the run lost the dashboard's own tab bar: %q", out)
	}
}

// TestAttachEntryNilOpsIsANoOp proves attachEntry degrades to a silent
// no-op — not a panic — for a nil entry or one whose producer never wired
// an Attach op, matching every other action here that requires a live row
// to act on (work.go's own doc comment on attachEntry).
func TestAttachEntryNilOpsIsANoOp(t *testing.T) {
	m := workTestModel(&manifest.Manifest{})
	if _, cmd := m.attachEntry(nil); cmd != nil {
		t.Errorf("attachEntry(nil) returned a non-nil Cmd, want nil")
	}
	noAttach := &livework.Entry{ID: "x", Name: "x"}
	if _, cmd := m.attachEntry(noAttach); cmd != nil {
		t.Errorf("attachEntry with nil Ops.Attach returned a non-nil Cmd, want nil")
	}
}
