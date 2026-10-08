// workfooter_test.go pins admin-3rgp.72.12: ↵ is the Work tab's one primary
// action, its meaning follows the row's state, kill moved off k onto x, and
// the footer is computed from the SAME row-state resolution the key handler
// uses rather than a second, parallel check. Each test names the acceptance
// criterion it pins.
package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// twoCommandModel is "build" (left idle) and "deploy" (given a live entry by
// each test that needs one), so a row above and below the live one both
// exist to prove k merely moves the cursor rather than acting on whatever it
// passes over.
func twoCommandModel() Model {
	m := workTestModel(&manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build":  {Desc: "build it", Steps: []string{"build"}},
			"deploy": {Desc: "deploy it", Steps: []string{"deploy"}},
		},
		Actions: map[string]manifest.Action{
			"build":  {"kind": "shell"},
			"deploy": {"kind": "shell"},
		},
	})
	m.active = tabWork
	return m
}

// rowIndex and isCmdRow are work_submenu_test.go's own helpers — reused here
// rather than redeclared, same "compute the index, then set the cursor to
// it" shape worktargets_test.go's TestKillOnRunningTargetRowKillsIt also
// uses, so the new tests aren't hardcoding row positions that a header row
// or a blank spacer would silently shift.

// TestKKeyOnLiveRowMovesCursorAndDoesNotKill is the AC "k on a live row moves
// the cursor and does not kill anything" — k must fall through to the exact
// same movement every other tab already gives it, never touch a live entry.
//
// MUTATION EVIDENCE: reinstate the old `if m.active == tabWork { return
// m.killCurrentWork() }` special case ahead of the movement handling in
// case "k" (model.go) and this test fails: killed becomes true and the
// cursor never moves off the deploy row.
func TestKKeyOnLiveRowMovesCursorAndDoesNotKill(t *testing.T) {
	freshLivework(t)
	killed := false
	live := &livework.Entry{
		ID: "deploy", Name: "deploy",
		Handle: "pane-deploy", Via: "terminal",
		Ops: livework.Ops{Kill: func() error { killed = true; return nil }},
	}
	if err := livework.Default.Add(live); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m := twoCommandModel()
	buildIdx := rowIndex(t, m, isCmdRow("build"))
	deployIdx := rowIndex(t, m, isCmdRow("deploy"))
	if deployIdx != buildIdx+1 {
		t.Fatalf("deploy row (%d) is not directly below build row (%d) — fixture assumption broke", deployIdx, buildIdx)
	}
	m.cursor[tabWork] = deployIdx

	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if cmd != nil {
		t.Errorf("k on the Work tab returned a non-nil Cmd, want nil (cursor movement is synchronous)")
	}
	if killed {
		t.Error("k killed the live entry under the old cursor position — kill must live on x now, never k")
	}
	got := updated.(Model).cursor[tabWork]
	if got != buildIdx {
		t.Errorf("cursor after k = %d, want %d (moved up onto the build row, same as every other tab's k)", got, buildIdx)
	}
}

// TestXKeyKillsLiveCommandRow is half of the AC "x on a live row kills it,
// from a row reached through Commands and from one reached through Workers
// alike" — the Commands half, driven through the real key handler (not
// killCurrentWork called directly, which worktargets_test.go's
// TestKillOnRunningTargetRowKillsIt already covers for a target row) so a
// regression in case "x"'s routing itself would be caught.
//
// MUTATION EVIDENCE: delete the `if m.active == tabWork { return
// m.killCurrentWork() }` branch this ticket adds to case "x" and this test
// fails: killed stays false.
func TestXKeyKillsLiveCommandRow(t *testing.T) {
	freshLivework(t)
	killed := false
	live := &livework.Entry{
		ID: "deploy", Name: "deploy",
		Handle: "pane-deploy", Via: "terminal",
		Ops: livework.Ops{Kill: func() error { killed = true; return nil }},
	}
	if err := livework.Default.Add(live); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m := twoCommandModel()
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("deploy"))

	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd != nil {
		t.Errorf("x on the Work tab returned a non-nil Cmd, want nil (kill acts synchronously via Ops.Kill)")
	}
	if !killed {
		t.Error("x on a live command row did not invoke Ops.Kill")
	}
}

// TestXKeyKillsLiveWorkerRow is the Workers half of the same AC: a genuine
// agent worker row, reached with no command behind it at all, must kill the
// same way a live command row does.
func TestXKeyKillsLiveWorkerRow(t *testing.T) {
	freshLivework(t)
	killed := false
	worker := &livework.Entry{
		ID: "review-agent", Name: "review-agent",
		Handle: "pane-review", Via: "terminal",
		Worker: &livework.WorkerInfo{Branch: "feat/x", Worktree: "/repo/.worktrees/x"},
		Ops:    livework.Ops{Kill: func() error { killed = true; return nil }},
	}
	if err := livework.Default.Add(worker); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m := twoCommandModel()
	m.active = tabWorkers
	m.cursor[tabWorkers] = rowIndex(t, m, func(r rowItem) bool {
		return r.kind == rowWorker && r.work != nil && r.work.worker == worker
	})

	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd != nil {
		t.Errorf("x on the Work tab returned a non-nil Cmd, want nil (kill acts synchronously via Ops.Kill)")
	}
	if !killed {
		t.Error("x on a live worker row did not invoke Ops.Kill")
	}
}

// TestWorkFooterMatchesFramesVerbatim is the AC "the footer text changes
// between those states" — one text for the idle case, another for the live
// case (a command or a worker row). Checked against
// workFooterPairs() directly — the same values view.go's viewKeymap() and
// the case "k"/"x" handlers both key off — rather than the rendered ANSI
// string, so this test can never pass by accident of two parallel
// implementations agreeing with each other.
//
// MUTATION EVIDENCE: hardcode workFooterPairs() back to always returning the
// live pairs and this test's idle assertion fails; hardcode it to always
// return idle and the live assertion fails.
func TestWorkFooterMatchesFramesVerbatim(t *testing.T) {
	freshLivework(t)
	live := &livework.Entry{
		ID: "deploy", Name: "deploy",
		Handle: "pane-deploy", Via: "terminal",
	}
	if err := livework.Default.Add(live); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m := twoCommandModel()

	idlePairs := [][2]string{{"↑↓", "move"}, {"↵", "dispatch"}, {"c", "collapse"}, {"q/esc", "quit"}}
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("build"))
	if got := m.workFooterPairs(); !pairsEqual(got, idlePairs) {
		t.Errorf("footer pairs on an idle command row = %v, want %v (commands.txt:30 verbatim, plus admin-4zz9's c collapse)", got, idlePairs)
	}

	livePairs := [][2]string{{"↑↓", "move"}, {"↵", "attach"}, {"x", "kill"}, {"c", "collapse"}, {"q/esc", "quit"}}
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("deploy"))
	if got := m.workFooterPairs(); !pairsEqual(got, livePairs) {
		t.Errorf("footer pairs on a live command row = %v, want %v (commands-running.txt:30 verbatim, plus admin-4zz9's c collapse)", got, livePairs)
	}

	// TestCKeyTogglesFooterHintCollapseVsExpand below pins the hint flipping
	// to "expand" once collapsed; this test stays on the expanded default.
}

func pairsEqual(a, b [][2]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
