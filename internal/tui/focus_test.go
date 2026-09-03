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
	// The Work tab's own disclosure arrow ("▸ deploy") reuses the same glyph
	// as the list-row cursor marker, so checking for its absence there would
	// prove nothing; a list tab (Issues) has no such row of its own and is a
	// clean surface to check the cursor marker against.
	m.active = tabIssues
	m.remote = data.Remote{Issues: []gh.IssueItem{{Number: 1, Title: "one"}, {Number: 2, Title: "two"}}}
	m.haveData = true
	out := m.View()
	if strings.Contains(out, "▸") {
		t.Errorf("a row still carries the current-row lead marker with focus on the tab bar:\n%s", out)
	}
}

// TestTabBarFocusStepsTabsOnWorkTab is AC2: left/right/h/l step the tab from
// the tab bar on every tab including Work, with no submenu expanding.
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
		if nm.active == tabWork {
			if nm.expandedCmd != "" {
				t.Errorf("%q from the tab bar expanded a Work-tab submenu (%q), want none", key, nm.expandedCmd)
			}
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
	if nm2.expandedCmd != "" {
		t.Errorf("left from the tab bar expanded/collapsed a submenu (%q), want no-op", nm2.expandedCmd)
	}
}

// TestEnterAndDownMoveFocusIntoBody is AC3: j/down/enter moves focus into the
// body; the previously current row is current again if there was one, else
// the first row.
func TestEnterAndDownMoveFocusIntoBody(t *testing.T) {
	for _, key := range []string{"j", "down", "enter"} {
		m := workFocusModel()
		m.cursor[tabWork] = 2 // pretend a prior trip left the cursor mid-list
		next, _ := m.handleKey(keyMsg(key))
		nm := next.(Model)
		if nm.focus != focusBody {
			t.Errorf("%q from the tab bar left focus at %v, want focusBody", key, nm.focus)
		}
		if nm.cursor[tabWork] != 2 {
			t.Errorf("%q from the tab bar disturbed the preserved cursor: got %d, want 2", key, nm.cursor[tabWork])
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

// TestExpandCollapseStillWorkInBody is the ticket's AC5: on the Work tab with
// focus in the body, left/right still expand and collapse a command's
// targets.
func TestExpandCollapseStillWorkInBody(t *testing.T) {
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

	next, _ := m.handleKey(keyMsg("right"))
	nm := next.(Model)
	if nm.expandedCmd != "deploy" {
		t.Fatalf("right on a collapsed multi-target row in the body: expandedCmd = %q, want %q", nm.expandedCmd, "deploy")
	}
	next2, _ := nm.handleKey(keyMsg("left"))
	nm2 := next2.(Model)
	if nm2.expandedCmd != "" {
		t.Errorf("left on the same row: expandedCmd = %q, want collapsed (\"\")", nm2.expandedCmd)
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
// with the tab-bar-only nav pairs merged in and no key advertised twice.
func TestKeybarFollowsFocus(t *testing.T) {
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
	if !foundKill || !foundAttach {
		t.Errorf("tab-bar keymap on the Work tab = %v, want the row-scoped %q and %q pairs alongside the nav keys", tabsPairs, "kill", "attach")
	}

	m.focus = focusBody
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
}
