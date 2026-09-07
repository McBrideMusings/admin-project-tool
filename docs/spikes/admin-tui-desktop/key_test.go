package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/tui"
)

// TestMain builds the three tracker fixtures (fixtures.go) once for every
// test in this package and tears them down on the way out — the same shape
// internal/tui/hermetic_test.go uses for the archetype seam alone; here it
// also owns the temp-dir repos and the canned remote (admin-p55x).
func TestMain(m *testing.M) {
	code := m.Run()
	cleanupFixtures()
	os.Exit(code)
}

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
// tracker tabs exist is a property of the repo (its declared tracker key and
// uses_pull_requests), not a setting.
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
	if !strings.Contains(beads, "beads-repo") {
		t.Errorf("beads side is not the beads fixture")
	}
	if !strings.Contains(github, "github-repo") {
		t.Errorf("github side is not the github fixture")
	}
	if beads == github {
		t.Errorf("the tweak changed nothing")
	}

	// wantsPRs() is the manifest's declared uses_pull_requests key, so the PRs
	// tab exists whenever a repo declares it true — a real repo with an open
	// PR is not required, but a third fixture is still the only way to see
	// this tab drawn without also drawing Beads or Issues (a repo declares at
	// most one tracker plus PRs, and the axes here each declare a single shape).
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

// TestFixtureTabSetsAreWhatTheDigitKeysAssume pins the exact tab bar each
// fixture draws. keyRepo/keyTracker/keyConfig (variants.go) hardcode "2"/"3"/
// "4" on the assumption that every fixture draws Work · Repo · <one tracker
// tab> · Config in that order — if a fixture manifest ever grew a second
// tracker signal (both a tracker key and uses_pull_requests = true, say), the
// digit keys would silently index the wrong tab. This renders repo-dash's own
// Model directly (not the prototype's fake tabBar()) so it asserts on the
// real, declaration-driven tab bar the digit keys actually navigate.
func TestFixtureTabSetsAreWhatTheDigitKeysAssume(t *testing.T) {
	cases := []struct {
		axis    string
		want    string
		exclude []string
	}{
		{"beads", "Beads", []string{"Issues", "PRs"}},
		{"github", "Issues", []string{"Beads", "PRs"}},
		{"prs", "PRs", []string{"Beads", "Issues"}},
	}
	for _, c := range cases {
		src := trackerRepo(c.axis)
		ctx, cancel := context.WithCancel(context.Background())
		m := tui.New(ctx, src.root, src.nwo, src.branch, "main")
		out := Strip(tui.Frame(ctx, m, tui.FrameOptions{Width: 96, Height: 30, Settle: 8 * time.Second}))
		cancel()

		tabRow := tabBarLine(out)
		for _, want := range []string{"Work", "Repo", c.want, "Config"} {
			if !strings.Contains(tabRow, want) {
				t.Errorf("%s: tab bar %q missing %q", c.axis, tabRow, want)
			}
		}
		for _, excl := range c.exclude {
			if strings.Contains(tabRow, excl) {
				t.Errorf("%s: tab bar %q unexpectedly drew %q — digit keys would mis-index", c.axis, tabRow, excl)
			}
		}
	}
}

// TestPaletteDispatchRendersATail pins the fixture-shape fix for
// admin-3rgp.19's hover axis: a palette-dispatched row must be the same
// shape as the hosted docs/test fixtures (hasRing: true, a real tail), not
// the bare state/id/doing/hosted/lastCmd row it used to append. Without
// this, landing on the freshly-dispatched row and switching Hover to
// auto-tail rendered the plain now/last field list instead of the tail —
// dead on the one row a person just dispatched is most likely to be
// looking at.
func TestPaletteDispatchRendersATail(t *testing.T) {
	resetLiveRows()
	defer resetLiveRows()

	e := paletteEntry{parent: "dev", target: "cf", run: "bun run dev:cf"}
	dispatchPalette(e)

	var got *liveRow
	for i := range liveRows {
		if liveRows[i].cmd == "" && liveRows[i].id == e.resolved() {
			got = &liveRows[i]
		}
	}
	if got == nil {
		t.Fatalf("dispatchPalette did not append a Workers row for %q", e.resolved())
	}
	if !got.hasRing {
		t.Fatalf("dispatched row %q has hasRing=false — auto-tail/split-pane/inline-peek all gate on it and stay dead", got.id)
	}
	if len(got.tail) == 0 {
		t.Fatalf("dispatched row %q has hasRing=true but an empty tail", got.id)
	}

	out := Strip(strings.Join(paletteWorkerDetail(got, 60, 12, "auto-tail"), "\n"))
	if strings.Contains(out, "now") {
		t.Errorf("auto-tail on the dispatched row still rendered the now/last field list:\n%s", out)
	}
	if !strings.Contains(out, got.tail[0]) {
		t.Errorf("auto-tail on the dispatched row did not render its tail content:\n%s", out)
	}
}

// tabBarLine returns line index 1 of a rendered frame — Model.frameRaw always
// lays out header, tab bar, blank separator in that fixed order.
func tabBarLine(out string) string {
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		return ""
	}
	return lines[1]
}
