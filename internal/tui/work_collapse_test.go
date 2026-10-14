// work_collapse_test.go pins admin-4zz9: `c` toggles the Work tab's
// session-only collapse mode. Expanded (the default) renders every
// command's targets exactly as work_submenu_test.go and worktargets_test.go
// already pin; collapsed renders targets only for the command the cursor is
// currently inside, and moving the cursor across a command boundary opens
// the new one and closes the old one with no skipped or repeated row.
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// withDeploy adds a second multi-target command ("deploy", two targets,
// group 1 priority 4 — sorting last) to multiTargetModel's fixture, so a
// collapse test has two groups to prove one stays open while the other
// doesn't.
func withDeploy(m Model) Model {
	m.manifest.Actions["deploy"] = manifest.Action{
		"kind":    "multi-target",
		"targets": map[string]any{"mac": "ship mac", "ios": "ship ios"},
	}
	m.manifest.Custom["deploy"] = manifest.Command{Desc: "ship it", Steps: []string{"deploy"}, Group: 1, Priority: 4}
	return m
}

// TestCollapsedModeShowsOnlyCursorsCommandTargets pins the AC "in collapsed
// mode only the cursor's own command shows targets": with the cursor on one
// of dev's targets, toggling collapse keeps dev's four targets visible but
// drops deploy's two — only its bare header row remains.
func TestCollapsedModeShowsOnlyCursorsCommandTargets(t *testing.T) {
	m := withDeploy(multiTargetModel(t))
	m.active = tabWork
	m.commandsTab.cursor = rowIndex(t, m, isTargetRow("dev", "mac"))

	m.commandsTab.toggleCollapse(&m.shell)
	if !m.commandsTab.collapsed {
		t.Fatal("toggleWorkCollapse did not turn collapse on")
	}
	if m.commandsTab.openCmd != "dev" {
		t.Fatalf("workOpenCmd = %q, want %q (the command the cursor was inside)", m.commandsTab.openCmd, "dev")
	}

	got := rowNames(m.commandsTab.rows(&m.shell))
	want := []string{
		"cmd:dev",
		"target:dev/ios", "target:dev/mac", "target:dev/server", "target:dev/tv",
		"cmd:solo", "cmd:test", "cmd:deploy",
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %v, want %v", got, want)
		}
	}
}

// TestExpandedModeUnaffectedByCollapseField proves workCollapsed's zero
// value (the launch default, never explicitly set) renders every command's
// targets exactly as before this field existed — the same row set
// TestEveryCommandsTargetsRenderUnconditionally (worktargets_test.go) pins,
// now re-asserted with a Model that also carries the new field to prove its
// zero value changes nothing.
func TestExpandedModeUnaffectedByCollapseField(t *testing.T) {
	m := withDeploy(multiTargetModel(t))
	got := rowNames(m.commandsTab.rows(&m.shell))
	want := []string{
		"cmd:dev",
		"target:dev/ios", "target:dev/mac", "target:dev/server", "target:dev/tv",
		"cmd:solo", "cmd:test",
		"cmd:deploy", "target:deploy/ios", "target:deploy/mac",
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %v, want %v", got, want)
		}
	}
}

// currentRowName is rowNames applied to the single row under the cursor —
// the cursor-walk tests' own shorthand for "what row is m on right now".
func currentRowName(t *testing.T, m Model) string {
	t.Helper()
	r, ok := m.currentRow()
	if !ok {
		t.Fatal("no current row")
	}
	names := rowNames([]rowItem{r})
	if len(names) != 1 {
		t.Fatalf("current row %+v produced %v, want exactly one name", r, names)
	}
	return names[0]
}

// TestMoveCursorWorkDownThroughTargetsAndOutNoDrift is the AC "down through a
// command and all its targets lands on the next command; up retraces it
// exactly. No drift." Walking down from dev's first target through its
// remaining three, past the plain solo/test rows, and into deploy (closed
// until the walk reaches it) must hit every row once, in order, with no
// row skipped or repeated — and walking back up must retrace the identical
// sequence in reverse.
func TestMoveCursorWorkDownThroughTargetsAndOutNoDrift(t *testing.T) {
	m := withDeploy(multiTargetModel(t))
	m.active = tabWork
	m.commandsTab.cursor = rowIndex(t, m, isTargetRow("dev", "ios"))
	m.commandsTab.toggleCollapse(&m.shell)
	if m.commandsTab.openCmd != "dev" {
		t.Fatalf("workOpenCmd = %q, want %q", m.commandsTab.openCmd, "dev")
	}

	wantDown := []string{
		"target:dev/mac", "target:dev/server", "target:dev/tv",
		"cmd:solo", "cmd:test",
		"target:deploy/ios", "target:deploy/mac",
	}
	var gotDown []string
	for range wantDown {
		m.moveCursor(1)
		gotDown = append(gotDown, currentRowName(t, m))
	}
	if strings.Join(gotDown, ",") != strings.Join(wantDown, ",") {
		t.Fatalf("down walk = %v, want %v", gotDown, wantDown)
	}
	if m.commandsTab.openCmd != "deploy" {
		t.Errorf("workOpenCmd after the down walk = %q, want %q", m.commandsTab.openCmd, "deploy")
	}

	wantUp := []string{
		"target:deploy/ios", "cmd:test", "cmd:solo",
		"target:dev/tv", "target:dev/server", "target:dev/mac", "target:dev/ios",
	}
	var gotUp []string
	for range wantUp {
		m.moveCursor(-1)
		gotUp = append(gotUp, currentRowName(t, m))
	}
	if strings.Join(gotUp, ",") != strings.Join(wantUp, ",") {
		t.Fatalf("up walk = %v, want %v", gotUp, wantUp)
	}
	if m.commandsTab.openCmd != "dev" {
		t.Errorf("workOpenCmd after retracing up = %q, want %q (back where the walk started)", m.commandsTab.openCmd, "dev")
	}
}

// TestWorkRowCountStableAcrossCollapseAndCursorWalk pins the AC "the badge
// and section header count what exists, not what's currently painted":
// workRowCount must read the same total in expanded mode, right after
// collapsing (with the cursor inside dev, so dev's targets still render),
// and at every step of a cursor walk that closes dev and opens it again —
// the exact flicker (10 -> 6 -> 10 -> 6) admin-4zz9 round 2 was filed over.
func TestWorkRowCountStableAcrossCollapseAndCursorWalk(t *testing.T) {
	m := multiTargetModel(t)
	m.active = tabWork
	const want = 6 // dev's 4 targets + solo + test — multiTargetModel's own invocation count

	if got := m.commandsTab.count(); got != want {
		t.Fatalf("expanded workRowCount = %d, want %d", got, want)
	}

	m.commandsTab.cursor = rowIndex(t, m, isTargetRow("dev", "ios"))
	m.commandsTab.toggleCollapse(&m.shell)
	if !m.commandsTab.collapsed {
		t.Fatal("toggleWorkCollapse did not turn collapse on")
	}
	if got := m.commandsTab.count(); got != want {
		t.Fatalf("collapsed workRowCount (cursor inside dev) = %d, want %d", got, want)
	}

	// Walk off dev (closing it) through solo and test, then back (reopening
	// it) — the count must not move at any step, though the rendered row
	// list does.
	walk := []string{
		"target:dev/mac", "target:dev/server", "target:dev/tv",
		"cmd:solo", "cmd:test",
	}
	for _, wantRow := range walk {
		m.moveCursor(1)
		if got := currentRowName(t, m); got != wantRow {
			t.Fatalf("cursor walk landed on %q, want %q", got, wantRow)
		}
		if got := m.commandsTab.count(); got != want {
			t.Fatalf("workRowCount at %q = %d, want %d", wantRow, got, want)
		}
	}
	for range walk {
		m.moveCursor(-1)
		if got := m.commandsTab.count(); got != want {
			t.Fatalf("workRowCount walking back = %d, want %d", got, want)
		}
	}
	if got := currentRowName(t, m); got != "target:dev/ios" {
		t.Fatalf("walk back landed on %q, want %q (retrace)", got, "target:dev/ios")
	}
}

// TestCKeyOnOtherTabsIsANoOp proves `c` does nothing outside the Work tab —
// no collapse field ever flips, no tab switch, no crash.
func TestCKeyOnOtherTabsIsANoOp(t *testing.T) {
	m := workTestModel(&manifest.Manifest{})
	m.active = tabBeads

	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if cmd != nil {
		t.Errorf("c on a non-Work tab returned a non-nil Cmd, want nil")
	}
	nm := next.(Model)
	if nm.commandsTab.collapsed {
		t.Error("c on a non-Work tab turned collapse mode on, want it untouched")
	}
	if nm.active != tabBeads {
		t.Errorf("active tab changed to %v, want c to be a no-op", nm.active)
	}
}

// TestCKeyTogglesFooterHintCollapseVsExpand is the AC "the footer hint shows
// what pressing it would do": expanded shows "c collapse", and once
// collapsed the same key relabels to "c expand".
func TestCKeyTogglesFooterHintCollapseVsExpand(t *testing.T) {
	m := twoCommandModel()
	m.commandsTab.cursor = rowIndex(t, m, isCmdRow("build"))

	pairs := m.viewKeymap()
	found := false
	for _, p := range pairs {
		if p[0] == "c" {
			found = true
			if p[1] != "collapse" {
				t.Errorf(`footer "c" pair = %v, want label "collapse" while expanded`, p)
			}
		}
	}
	if !found {
		t.Fatalf("no \"c\" pair in the expanded footer: %v", pairs)
	}

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	nm := next.(Model)
	if !nm.commandsTab.collapsed {
		t.Fatal("c did not turn collapse mode on")
	}
	pairs = nm.viewKeymap()
	found = false
	for _, p := range pairs {
		if p[0] == "c" {
			found = true
			if p[1] != "expand" {
				t.Errorf(`footer "c" pair = %v, want label "expand" once collapsed`, p)
			}
		}
	}
	if !found {
		t.Fatalf("no \"c\" pair in the collapsed footer: %v", pairs)
	}
}
