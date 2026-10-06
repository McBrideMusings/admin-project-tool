// work_target_attach_test.go pins admin-3rgp.72.18: ↵ on a running TARGET
// row (rowCommandTarget) must attach to the run already in flight, exactly
// like ↵ on a running flat command row (rowCommand) already did —
// activateCommandTarget used to carry its own copy of activateCommand's
// ladder and the copy never got the "already live → attach" branch
// admin-3rgp.72.12 added to the other one, so ↵ on e.g. "command-test ├
// counter" while it was running dispatched a SECOND run instead of opening
// the attached view, even though workFooterPairs — which read row state
// independently and got it right — was already advertising "↵ attach".
//
// Every test here drives the real key handler (m.activate(), reached via
// m.cursor[m.active] the way a keypress would move it) rather than calling
// activateCommand/activateCommandTarget directly, and every test written for
// one row kind is written for the other — the previous round's tests only
// ever exercised activateCommand, which is exactly how this shipped broken.
package tui

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// targetRowIndex finds the row index of parent/target ("dev"/"mac") in m's
// current Work-tab row list, failing the test if it's missing.
func targetRowIndex(t *testing.T, m Model, cmdName, targetKey string) int {
	t.Helper()
	for i, r := range m.workRows() {
		if r.kind == rowCommandTarget && r.work != nil && r.work.cmdName == cmdName && r.work.targetKey == targetKey {
			return i
		}
	}
	t.Fatalf("no target row %s/%s", cmdName, targetKey)
	return -1
}

// flatCommandRowIndex finds the row index of a flat (non-target) command row.
func flatCommandRowIndex(t *testing.T, m Model, cmdName string) int {
	t.Helper()
	for i, r := range m.workRows() {
		if r.kind == rowCommand && r.work != nil && r.work.cmdName == cmdName {
			return i
		}
	}
	t.Fatalf("no command row %q", cmdName)
	return -1
}

// stubStartHostedRunCalled replaces startHostedRun for the test's duration
// and reports whether it was ever called — the seam work.go:806 exists for
// exactly this: proving a dispatch path was NOT taken. Distinct from
// rowkeys_test.go's stubStartHostedRun (which hands back captured env),
// since this file cares only about call/no-call.
func stubStartHostedRunCalled(t *testing.T) *bool {
	t.Helper()
	called := new(bool)
	old := startHostedRun
	t.Cleanup(func() { startHostedRun = old })
	startHostedRun = func(mf *manifest.Manifest, name string, args []string, rows, cols int, env []string) error {
		*called = true
		return nil
	}
	return called
}

// TestEnterOnRunningTargetRowAttachesNotDispatches is the bug's own repro:
// ↵ on "dev/mac" while it is running must attach to the existing pane and
// must NOT start a second run.
//
// MUTATION EVIDENCE (see mutation.discriminates in the pass report): revert
// resolveRowAction's rowCommandTarget branch (or activateCommandTarget's use
// of it) back to the old "always dispatch" body and this test fails with
// dispatched=true, attached=false — startHostedRun gets called a second time
// on a row workFooterPairs was already labeling "↵ attach".
func TestEnterOnRunningTargetRowAttachesNotDispatches(t *testing.T) {
	freshLivework(t)
	dispatched := stubStartHostedRunCalled(t)
	attached := false
	live := &livework.Entry{
		ID: "dev:mac", Name: kinds.TargetLiveKey("dev", "mac"), Kind: "multi-target",
		State: livework.StateRunning, Handle: "pane-dev-mac", Via: "terminal",
		Ops: livework.Ops{Attach: func() error { attached = true; return nil }},
	}
	if err := livework.Default.Add(live); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = targetRowIndex(t, m, "dev", "mac")

	_, cmd := m.activate()
	if !attached {
		t.Error("enter on a running target row did not attach, attached=false")
	}
	if *dispatched {
		t.Error("enter on a running target row dispatched a SECOND run via startHostedRun — the bug this ticket fixes")
	}
	if cmd != nil {
		t.Errorf("enter on a running target row returned a non-nil Cmd, want nil (attach is synchronous)")
	}
}

// TestAttachedBreadcrumbUsesBandName: the breadcrumb names an attached run
// the way the band title names its row — a target "dev mac", never the
// registry id "dev:mac"; a worker by its own Name, whatever its id holds.
func TestAttachedBreadcrumbUsesBandName(t *testing.T) {
	for _, c := range []struct {
		entry *livework.Entry
		want  string
	}{
		{&livework.Entry{ID: "dev:mac", Name: "dev:mac", Kind: "hosted-pty", Handle: "pty-dev-mac"}, "dev mac"},
		{&livework.Entry{ID: "implement:admin-1", Name: "admin-1", Kind: "workflow", Handle: "pane-1", Via: "terminal",
			Worker: &livework.WorkerInfo{Branch: "admin-1", Worktree: "/tmp/admin-1"}}, "admin-1"},
	} {
		freshLivework(t)
		c.entry.State = livework.StateRunning
		if err := livework.Default.Add(c.entry); err != nil {
			t.Fatalf("Add: %v", err)
		}
		m := multiTargetModel(t)
		m.termAttached, m.termEntryID = true, c.entry.ID
		crumb := stripANSI(m.attachBreadcrumb(80))
		if !strings.Contains(crumb, "● "+c.want+"  · ") {
			t.Errorf("breadcrumb for %s = %q, want it to name %q", c.entry.ID, crumb, c.want)
		}
		if band := stripANSI(strings.Join(m.renderWorkBand(m.mustEntryRow(t, c.entry.ID), 80, 12), "\n")); !strings.Contains(band, " "+c.want+"  running") {
			t.Errorf("band for %s = %q, want its title to name %q", c.entry.ID, band, c.want)
		}
	}
}

// TestQuitConfirmNamesTargetLikeBand: the quit confirm lists a running target
// as "dev mac", the band title's name for it.
func TestQuitConfirmNamesTargetLikeBand(t *testing.T) {
	freshLivework(t)
	e := &livework.Entry{ID: "dev:mac", Name: "dev:mac", Kind: "hosted-pty", Handle: "pty-dev-mac", State: livework.StateRunning}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}
	m := multiTargetModel(t)
	m.quitHosted = []*livework.Entry{e}
	box := stripANSI(strings.Join(m.quitConfirmBox(60, 30), "\n"))
	if !strings.Contains(box, "dev mac") || strings.Contains(box, "dev:mac") {
		t.Errorf("quit confirm = %q, want it to list %q", box, "dev mac")
	}
}

func (m Model) mustEntryRow(t *testing.T, id string) rowItem {
	t.Helper()
	r, _, ok := m.entryRow(id)
	if !ok {
		t.Fatalf("no Work row for live entry %q", id)
	}
	return r
}

// TestEnterOnRunningFlatCommandRowStillAttaches is the sibling of the test
// above for rowCommand, pinning that fixing the target ladder didn't break
// the flat-command ladder it was copied from.
func TestEnterOnRunningFlatCommandRowStillAttaches(t *testing.T) {
	freshLivework(t)
	dispatched := stubStartHostedRunCalled(t)
	attached := false
	live := &livework.Entry{
		ID: "test", Name: "test", Kind: "shell",
		State: livework.StateRunning, Handle: "pane-test", Via: "terminal",
		Ops: livework.Ops{Attach: func() error { attached = true; return nil }},
	}
	if err := livework.Default.Add(live); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = flatCommandRowIndex(t, m, "test")

	_, cmd := m.activate()
	if !attached {
		t.Error("enter on a running flat command row did not attach, attached=false")
	}
	if *dispatched {
		t.Error("enter on a running flat command row dispatched a second run")
	}
	if cmd != nil {
		t.Errorf("enter on a running flat command row returned a non-nil Cmd, want nil (attach is synchronous)")
	}
}

// TestEnterOnIdleTargetRowDispatches: an idle target row (no live entry)
// must still dispatch — the attach branch must not swallow the idle case.
func TestEnterOnIdleTargetRowDispatches(t *testing.T) {
	freshLivework(t)
	dispatched := stubStartHostedRunCalled(t)

	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = targetRowIndex(t, m, "dev", "mac")

	if _, cmd := m.activate(); cmd != nil {
		cmd()
	}
	if !*dispatched {
		t.Error("enter on an idle target row did not dispatch, want startHostedRun called")
	}
}

// TestEnterOnIdleFlatCommandRowDispatches is TestEnterOnIdleTargetRowDispatches's
// sibling for rowCommand.
func TestEnterOnIdleFlatCommandRowDispatches(t *testing.T) {
	freshLivework(t)
	dispatched := stubStartHostedRunCalled(t)

	m := multiTargetModel(t)
	m.active = tabWork
	m.cursor[tabWork] = flatCommandRowIndex(t, m, "test")

	if _, cmd := m.activate(); cmd != nil {
		cmd()
	}
	if !*dispatched {
		t.Error("enter on an idle flat command row did not dispatch, want startHostedRun called")
	}
}

// TestFooterAgreesWithActionForBothRowKinds asserts workFooterPairs' label
// and resolveRowAction's decision can never drift for either row kind, idle
// or live — both are read from the exact same function, so this test is
// mostly documentation, but it pins the wiring: workFooterPairs must call
// resolveRowAction rather than re-deriving row state.
func TestFooterAgreesWithActionForBothRowKinds(t *testing.T) {
	freshLivework(t)
	m := multiTargetModel(t)
	m.active = tabWork

	cases := []struct {
		name      string
		cmdName   string
		targetKey string // empty for the flat "test" row
		live      bool
		wantLabel string
		wantAct   rowAction
	}{
		{"idle target", "dev", "mac", false, "dispatch", actionDispatch},
		{"idle flat", "test", "", false, "dispatch", actionDispatch},
		{"live target", "dev", "mac", true, "attach", actionAttach},
		{"live flat", "test", "", true, "attach", actionAttach},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			freshLivework(t)
			if c.live {
				id := c.cmdName
				if c.targetKey != "" {
					id = kinds.TargetLiveKey(c.cmdName, c.targetKey)
				}
				live := &livework.Entry{
					ID: id, Name: id, Kind: "shell", State: livework.StateRunning,
					Handle: "pane-" + id, Via: "terminal",
					Ops: livework.Ops{Attach: func() error { return nil }},
				}
				if err := livework.Default.Add(live); err != nil {
					t.Fatalf("Add: %v", err)
				}
			}

			mm := multiTargetModel(t)
			mm.active = tabWork
			if c.targetKey != "" {
				mm.cursor[tabWork] = targetRowIndex(t, mm, c.cmdName, c.targetKey)
			} else {
				mm.cursor[tabWork] = flatCommandRowIndex(t, mm, c.cmdName)
			}

			r, ok := mm.currentRow()
			if !ok {
				t.Fatal("currentRow() not ok")
			}
			gotAct := resolveRowAction(r.kind, r.work)
			if gotAct != c.wantAct {
				t.Errorf("resolveRowAction = %v, want %v", gotAct, c.wantAct)
			}
			pairs := mm.workFooterPairs()
			var gotLabel string
			for _, p := range pairs {
				if p[0] == "↵" {
					gotLabel = p[1]
				}
			}
			if gotLabel != c.wantLabel {
				t.Errorf("footer ↵ label = %q, want %q", gotLabel, c.wantLabel)
			}
		})
	}
}
