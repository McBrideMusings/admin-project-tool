package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func send(m model, k string) model {
	var msg tea.Msg
	switch k {
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	out, _ := m.Update(msg)
	return out.(model)
}

func TestArrowsMoveTheCursor(t *testing.T) {
	m := newModel()
	m.w, m.h = 120, 40
	if got := m.state().Get("screen"); got != "commands" {
		t.Fatalf("start screen = %q, want commands", got)
	}
	// The commands screen starts on deploy (index 2), so one down is docs and
	// two is logs. The reader must follow the cursor, not just the marker.
	m = send(m, "down")
	if v := m.View(); !strings.Contains(v, "◆ docs") {
		t.Errorf("one down from deploy did not land on docs")
	}
	m = send(m, "down")
	v := m.View()
	if !strings.Contains(v, "◆ logs") {
		t.Errorf("two downs from deploy did not land on logs")
	}
	if !strings.Contains(v, "admin logs dev") {
		t.Errorf("the reader pane did not follow the cursor to logs")
	}
}

func TestUpWrapsBackwards(t *testing.T) {
	m := newModel()
	m.w, m.h = 120, 40
	m = send(m, "up") // deploy -> check
	if v := m.View(); !strings.Contains(v, "◆ check") {
		t.Errorf("up from deploy did not land on check")
	}
}

func TestTabSwitchesTabs(t *testing.T) {
	m := newModel()
	m.w, m.h = 120, 40
	m = send(m, "tab")
	if got := m.state().Get("screen"); got != "repo" {
		t.Fatalf("after tab, screen = %q, want repo", got)
	}
	// The Repo tab is repo-dash's own frame, loaded off the render path, so a
	// cold render returns a placeholder by design. Take the blocking path to
	// assert on content.
	Blocking = true
	defer func() { Blocking = false }()
	if v := m.View(); !strings.Contains(v, "Branches") {
		t.Errorf("repo screen did not render")
	}
}

func TestEnterOpensTheDispatchSheet(t *testing.T) {
	m := newModel()
	m.w, m.h = 120, 40
	m = send(m, "tab") // repo
	m = send(m, "tab") // issues
	if got := m.state().Get("screen"); got != "issues" {
		t.Fatalf("screen = %q, want issues", got)
	}
	m = send(m, "enter")
	if got := m.state().Get("screen"); got != "dispatch-sheet" {
		t.Fatalf("after enter, screen = %q, want dispatch-sheet", got)
	}
}

// The tracker tweak swaps which repo the embedded frame renders, because which
// tracker tabs exist is a property of the repo (hasBeads/usesPRs), not a setting.
func TestTrackerTweakSwapsTheEmbeddedRepo(t *testing.T) {
	beads := Strip(render(State{
		Values: map[string]string{"screen": "issues", "tracker": "beads"}, W: 96, H: 30}))
	github := Strip(render(State{
		Values: map[string]string{"screen": "issues", "tracker": "github"}, W: 96, H: 30}))

	if !strings.Contains(beads, "Beads") {
		t.Errorf("beads side did not draw a Beads tab")
	}
	if !strings.Contains(github, "Issues") {
		t.Errorf("github side did not draw an Issues tab")
	}
	if !strings.Contains(beads, "admin-project-tool") {
		t.Errorf("beads side is not this repo")
	}
	if !strings.Contains(github, "etv-station") {
		t.Errorf("github side is not etv-station")
	}
	if beads == github {
		t.Errorf("the tweak changed nothing")
	}

	// usesPRs() is len(remote.PRs) > 0, so the PRs tab only exists on a repo
	// with open PRs — a third repo is the only way to see it at all.
	prs := Strip(render(State{
		Values: map[string]string{"screen": "issues", "tracker": "prs"}, W: 96, H: 30}))
	if !strings.Contains(prs, "PRs") {
		t.Errorf("prs side did not draw a PRs tab")
	}
}

// A tab press must never wait on a load. The embedded frames come from a repo
// that takes about a second to settle, so rendering them synchronously made
// every tab toggle stall — and what a prototype is being judged on is how it
// feels.
func TestTabPressNeverBlocks(t *testing.T) {
	frameMu.Lock()
	frameCache = map[string][]string{}
	warming = map[string]bool{}
	frameMu.Unlock()
	Blocking = false
	defer func() { Blocking = false }()

	for _, screen := range []string{"repo", "issues", "tracker-graph", "config"} {
		start := time.Now()
		render(State{Values: map[string]string{"screen": screen}, W: 96, H: 30})
		if d := time.Since(start); d > 100*time.Millisecond {
			t.Errorf("%s blocked for %v — a tab press must not wait on a load", screen, d)
		}
	}
}
