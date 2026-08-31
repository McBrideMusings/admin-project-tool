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
	"fmt"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestActivateCommandDispatchesWhenIdle proves Enter on an idle command row
// (no live entry, not a workflow) hands back a tea.Cmd built from
// dispatchCommandCmd — the AC's "a command row runs the command" — and,
// critically, that the returned Cmd is bubbletea's own execMsg wrapper
// rather than something run synchronously here: calling the Cmd func must
// not block or actually run the command, matching "a running command must
// not block the dashboard's event loop." tea.Exec's own contract (see
// bubbletea's exec.go) is that invoking the Cmd only returns a message
// describing what to run; the runtime executes it later. Asserting the
// dynamic type is "tea.execMsg" pins that this file didn't invent a second,
// blocking dispatch mechanism.
//
// MUTATION EVIDENCE: change activateCommand's final line from
// `return m, dispatchCommandCmd(m.manifest, wd.cmdName)` to `return m, nil`
// and this test fails: "activateCommand returned a nil Cmd for an idle
// command, want a dispatch Cmd."
func TestActivateCommandDispatchesWhenIdle(t *testing.T) {
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
	if got := fmt.Sprintf("%T", msg); got != "tea.execMsg" {
		t.Errorf("dispatch Cmd produced a %s, want tea.execMsg (tea.Exec's own non-blocking wrapper) — "+
			"a different message type means dispatch is no longer routed through tea.Exec", got)
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
		Ops: livework.Ops{Attach: func() error { attached = true; return nil }},
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
		State: livework.StateRunning,
		Worker: &livework.WorkerInfo{
			Branch: "test-enter-worker-attaches", Worktree: "/tmp/test-enter-worker-attaches",
		},
		Ops: livework.Ops{Attach: func() error { attached = true; return nil }},
	}
	livework.Default.Add(worker)

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
