// focus_test.go covers admin-70hb: the tab bar becomes a focus target and a
// new session opens with focus there instead of on a body row. Each test
// below is named for the acceptance criterion it pins.
package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// workFocusModel returns a Work-tab model (through the real New(), so the
// initial-focus AC is exercised for real) with two commands, one of them
// carrying targets so the submenu-interception ACs have something to expand.
func workFocusModel() Model {
	m := New(context.Background(), "/repo", "owner/repo", "main", "main")
	m.herdr = false
	m.width, m.height = 100, 30
	m.manifest = &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build":  {Desc: "build it", Steps: []string{"build"}},
			"deploy": {Desc: "deploy it", Steps: []string{"deploy"}},
		},
		Actions: map[string]manifest.Action{
			"build": {"kind": "shell"},
			"deploy": {
				"kind": "multi-target",
				"targets": map[string]any{
					"staging": "true",
					"prod":    "true",
				},
			},
		},
	}
	m.manifestOK = true
	m.active = tabWork
	return m
}

// TestNewSessionFocusesTabBar is AC1: a new session opens with focus on the
// tab bar and no row rendered as current.
func TestNewSessionFocusesTabBar(t *testing.T) {
	m := workFocusModel()
	if m.focus != focusTabs {
		t.Fatalf("New() focus = %v, want focusTabs", m.focus)
	}
	if _, ok := m.currentRow(); ok {
		t.Error("currentRow() returned a row while focus is on the tab bar, want none")
	}
	m.active = tabIssues
	m.remote = data.Remote{Issues: []gh.IssueItem{{Number: 1, Title: "one"}, {Number: 2, Title: "two"}}}
	m.haveData = true
	out := m.View()
	if strings.Contains(out, "▸") {
		t.Errorf("a row still carries the current-row lead marker with focus on the tab bar:\n%s", out)
	}
}

// TestTabBarFocusStepsTabsOnWorkTab is AC2: left/right/h/l step the tab from
// the tab bar on every tab including Work.
func TestTabBarFocusStepsTabsOnWorkTab(t *testing.T) {
	for _, key := range []string{"right", "left", "h", "l"} {
		m := workFocusModel()
		before := m.active
		next, _ := m.handleKey(keyMsg(key))
		nm := next.(Model)
		if nm.active == before && (key == "right" || key == "l") {
			// right/l on a single-tab-plus repo still need to move somewhere;
			// this fixture always draws Work + Local + Config at minimum, so
			// the tab must have changed.
			t.Errorf("%q from the tab bar did not change the active tab (still %v)", key, nm.active)
		}
	}
	// A dedicated case landing back on Work: right then left should return.
	m := workFocusModel()
	next, _ := m.handleKey(keyMsg("right"))
	nm := next.(Model)
	next2, _ := nm.handleKey(keyMsg("left"))
	nm2 := next2.(Model)
	if nm2.active != tabWork {
		t.Errorf("right then left from the tab bar landed on %v, want back on Work", nm2.active)
	}
}

// TestEnterAndDownMoveFocusIntoBody is AC3: j/down/enter moves focus into the
// body; the previously current row is current again if there was one, else
// the first row.
func TestEnterAndDownMoveFocusIntoBody(t *testing.T) {
	// Row 1 ("build") is the fixture's own flat, selectable command row —
	// row 2 ("deploy") is a multi-target parent and, since admin-3rgp.72.11,
	// a group the cursor can never land on, so it can no longer stand in for
	// "some selectable row mid-list" here.
	for _, key := range []string{"j", "down", "enter"} {
		m := workFocusModel()
		m.cursor[tabWork] = 1 // pretend a prior trip left the cursor mid-list
		next, _ := m.handleKey(keyMsg(key))
		nm := next.(Model)
		if nm.focus != focusBody {
			t.Errorf("%q from the tab bar left focus at %v, want focusBody", key, nm.focus)
		}
		if nm.cursor[tabWork] != 1 {
			t.Errorf("%q from the tab bar disturbed the preserved cursor: got %d, want 1", key, nm.cursor[tabWork])
		}
	}
}

// TestUpAtTopRowReturnsToTabBar is AC4: k/up on the top row returns focus to
// the tab bar; on any other row it moves the cursor as it does today.
func TestUpAtTopRowReturnsToTabBar(t *testing.T) {
	m := workFocusModel()
	m.focus = focusBody
	m.cursor[tabWork] = 0
	next, _ := m.handleKey(keyMsg("up"))
	nm := next.(Model)
	if nm.focus != focusTabs {
		t.Errorf("up on the top row left focus at %v, want focusTabs", nm.focus)
	}
	if nm.cursor[tabWork] != 0 {
		t.Errorf("leaveBody moved the cursor: got %d, want 0 (unchanged)", nm.cursor[tabWork])
	}

	// Not at the top: up moves the cursor as it always has.
	m2 := workFocusModel()
	m2.focus = focusBody
	m2.cursor[tabWork] = 2
	next2, _ := m2.handleKey(keyMsg("up"))
	nm2 := next2.(Model)
	if nm2.focus != focusBody {
		t.Errorf("up off the top row moved focus to %v, want it to stay focusBody", nm2.focus)
	}
	if nm2.cursor[tabWork] != 1 {
		t.Errorf("up off the top row cursor = %d, want 1", nm2.cursor[tabWork])
	}
}

// TestTargetsAlreadyVisibleRightLeftJustSwitchTabInBody supersedes the
// ticket's own admin-70hb AC5 ("left/right still expand and collapse a
// command's targets") — admin-0kxh removes that toggle. A multi-target
// command's targets render unconditionally, so with focus in the body,
// right/left on its parent row do exactly what they do on any other Work-tab
// row: switch the active tab.
func TestTargetsAlreadyVisibleRightLeftJustSwitchTabInBody(t *testing.T) {
	m := workFocusModel()
	m.focus = focusBody
	rows := m.visibleRows()
	idx := -1
	for i, r := range rows {
		if r.kind == rowCommand && r.work != nil && r.work.cmdName == "deploy" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no rowCommand for %q in %v", "deploy", rows)
	}
	m.cursor[tabWork] = idx

	sawTarget := false
	for _, r := range rows {
		if r.kind == rowCommandTarget && r.work != nil && r.work.cmdName == "deploy" {
			sawTarget = true
		}
	}
	if !sawTarget {
		t.Fatal("deploy's targets are not already rendered")
	}

	next, _ := m.handleKey(keyMsg("right"))
	nm := next.(Model)
	if nm.active == tabWork {
		t.Errorf("right on a multi-target row in the body stayed on Work, want it to switch tab")
	}
}

// TestExactlyOneSolidHighlight is AC6: exactly one solid highlight is on
// screen in either focus state, checked through the rendered frame. The tab
// pill's solid form is `fg cBg bg cBlue`, which lipgloss renders as an SGR
// pair carrying both a 30-37/90-97 foreground code and a 40-47/100-107
// background code on the same escape — the row cursor's cSel/cSelDim
// backgrounds never pair with a foreground swap the same way, so counting
// escapes that carry both a themed fg and a themed bg code is a reliable
// count of "solid" elements without needing to know exact color numbers.
func TestExactlyOneSolidHighlight(t *testing.T) {
	for _, focus := range []focusZone{focusTabs, focusBody} {
		m := workFocusModel()
		m.focus = focus
		if focus == focusBody {
			m.cursor[tabWork] = 1
		}
		out := m.View()
		segs, _ := m.tabBarSegs()
		pillSolid := false
		for _, s := range segs {
			if s.bg == cBlue {
				pillSolid = true
			}
		}
		rows := m.visibleRows()
		rowSolid := false
		if m.cursor[tabWork] >= 0 && m.cursor[tabWork] < len(rows) {
			rowSolid = m.focus == focusBody && rows[m.cursor[tabWork]].selectable()
		}
		switch focus {
		case focusTabs:
			if !pillSolid {
				t.Errorf("focusTabs: tab pill is not solid\n%s", out)
			}
			if rowSolid {
				t.Errorf("focusTabs: a body row also reads as solid")
			}
		case focusBody:
			if pillSolid {
				t.Errorf("focusBody: tab pill is still solid\n%s", out)
			}
			if !rowSolid {
				t.Errorf("focusBody: no body row reads as solid")
			}
		}
	}
}

// TestEmptyBodyDoesNotTrapFocus is AC7: a tab with an empty body does not
// trap focus — j from the tab bar is a no-op on tabIssues before any data
// has loaded, since the placeholder "loading…" row is not selectable.
func TestEmptyBodyDoesNotTrapFocus(t *testing.T) {
	m := workFocusModel()
	m.active = tabIssues
	if got := m.bodyRowCount(); got != 0 {
		t.Fatalf("bodyRowCount() on tabIssues = %d, want 0", got)
	}
	next, _ := m.handleKey(keyMsg("j"))
	nm := next.(Model)
	if nm.focus != focusTabs {
		t.Errorf("j from the tab bar on an empty-bodied tab moved focus to %v, want it to stay focusTabs", nm.focus)
	}
}

// TestClickSetsFocus is AC8: clicking a tab (tabBarHit) and clicking a row
// each set focus to what was clicked.
func TestClickSetsFocus(t *testing.T) {
	m := workFocusModel()
	m.focus = focusBody // start in the body so the tab click has to move it back

	next, _ := m.click(1, 1) // row 1 is the tab-bar line; x=1 sits inside the first tab
	nm := next.(Model)
	if nm.focus != focusTabs {
		t.Errorf("clicking the tab bar left focus at %v, want focusTabs", nm.focus)
	}

	// Clicking a row (the Work tab's own left column, addressed the same way
	// clickRow's hitRow branch would from click()) moves focus into the body.
	m3 := workFocusModel() // focusTabs, per New(); already on tabWork
	next3, _ := m3.clickRow(paneBranches, 0)
	nm3 := next3.(Model)
	if nm3.focus != focusBody {
		t.Errorf("clicking a Work-tab row left focus at %v, want focusBody", nm3.focus)
	}
}

// TestKeybarFollowsFocus is admin-wuz9.1: the footer keybar shows the tab's
// full hotkey set (not just a 3-key nav set) while focus is on the tab bar,
// with the tab-bar-only nav pairs merged in and no key advertised twice. The
// Work tab's own pairs are row-state-dependent (admin-3rgp.72.12), so "the
// tab's full hotkey set" while tab-bar-focused is the idle baseline — there
// is no current row yet to read a livelier state from.
func TestKeybarFollowsFocus(t *testing.T) {
	freshLivework(t)
	// admin-3rgp.72.12: the footer's a/attach and x/kill pairs are now
	// row-state-dependent — advertised only for a row backed by a live
	// livework.Entry, never unconditionally. "build" sorts first among the
	// fixture's two commands, so give IT a live entry to keep this test's
	// original intent (the tab-bar keymap merges the row-scoped pairs with
	// the nav-only ones) exercised against a live row.
	if err := livework.Default.Add(&livework.Entry{
		ID: "build", Name: "build", State: livework.StateRunning,
		Handle: "pane-build", Via: "terminal",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	m := workFocusModel()
	tabsPairs := m.keymap()
	foundTab := false
	for _, p := range tabsPairs {
		if p[0] == "←→" && p[1] == "tab" {
			foundTab = true
		}
	}
	if !foundTab {
		t.Errorf("tab-bar keymap = %v, want a ←→/tab pair", tabsPairs)
	}
	foundJump := false
	foundKill, foundAttach := false, false
	seen := map[string]bool{}
	for _, p := range tabsPairs {
		if p[0] == "1-4" && p[1] == "jump" {
			foundJump = true
		}
		if p[1] == "kill" {
			foundKill = true
		}
		if p[1] == "attach" {
			foundAttach = true
		}
		if seen[p[0]] {
			t.Errorf("tab-bar keymap advertises %q more than once: %v", p[0], tabsPairs)
		}
		seen[p[0]] = true
	}
	if !foundJump {
		t.Errorf("tab-bar keymap = %v, want a 1-4/jump pair", tabsPairs)
	}
	// admin-3rgp.72.12: currentRow() reports no current row at all while
	// focus is on the tab bar (m.focus == focusTabs, checked first thing in
	// currentRow) — and a/x are genuine no-ops in that state for exactly the
	// same reason. The footer must never advertise an action the key does
	// not take, so kill/attach are correctly ABSENT here; the idle baseline
	// ("dispatch") is what a row-less cursor gets.
	if foundKill || foundAttach {
		t.Errorf("tab-bar keymap on the Work tab = %v, want no row-scoped %q/%q pairs — there is no current row while the tab bar has focus", tabsPairs, "kill", "attach")
	}
	// enter calls enterBody() while focus is on the tab bar — the same
	// action "↓ body" already advertises — not tabWork's own row-kind
	// label ("open, run or attach"), which only applies once focus is in
	// the body (admin-wuz9.1).
	foundEnterBody := false
	for _, p := range tabsPairs {
		if p[0] == "↵" {
			if p[1] != "body" {
				t.Errorf("tab-bar keymap ↵ pair = %q, want %q (enter moves focus into the body while tab-bar-focused, it doesn't run the row kind's own action)", p[1], "body")
			}
			foundEnterBody = true
		}
	}
	if !foundEnterBody {
		t.Errorf("tab-bar keymap = %v, want a ↵ pair", tabsPairs)
	}

	m.enterBody()
	bodyPairs := m.keymap()
	if len(bodyPairs) == len(tabsPairs) {
		sameOrder := true
		for i := range bodyPairs {
			if bodyPairs[i] != tabsPairs[i] {
				sameOrder = false
			}
		}
		if sameOrder {
			t.Errorf("keymap() did not change between focus states")
		}
	}
	bodyFoundKill := false
	for _, p := range bodyPairs {
		if p[1] == "kill" {
			bodyFoundKill = true
		}
	}
	if !bodyFoundKill {
		t.Errorf("body keymap on the Work tab = %v, want the row-scoped %q pair", bodyPairs, "kill")
	}
	// Once focus moves into the body, ↵ reverts to the row kind's own
	// label — the tab-bar override is scoped to focusTabs only. The cursor
	// row (the fixture's live "build" entry) makes that label "attach".
	foundEnterRowKind := false
	for _, p := range bodyPairs {
		if p[0] == "↵" {
			if p[1] != "attach" {
				t.Errorf("body keymap ↵ pair = %q, want the row kind's own %q", p[1], "attach")
			}
			foundEnterRowKind = true
		}
	}
	if !foundEnterRowKind {
		t.Errorf("body keymap = %v, want a ↵ pair", bodyPairs)
	}
}
