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

// TestArrowsMoveTheCursor drives the combined cursor: the Commands rows first,
// then the live sections. The band must follow it — there is no second
// selector, so a cursor the band ignores is the bug this pins.
func TestArrowsMoveTheCursor(t *testing.T) {
	resetCursor()
	m := newModel()
	m.w, m.h = 120, 40
	if got := m.state().Get("screen"); got != "commands" {
		t.Fatalf("start screen = %q, want commands", got)
	}
	cmds := selectableCommands()
	if len(cmds) < 2 {
		t.Fatal("fixture has fewer than two command rows to move between")
	}
	// A target row draws its tree connector between the marker and the name
	// ("◆ ├ chat-moderation"), so the invariant is that the marker and the
	// label land on the SAME row, not that they are adjacent characters.
	m = send(m, "down")
	if !markedRowHas(Strip(m.View()), cmds[1].label()) {
		t.Errorf("one down did not land on %q", cmds[1].label())
	}
	// Past the Commands rows, the cursor enters the live sections and the band
	// switches from an entry's detail to a live row's own fields.
	for i := 0; i < len(cmds); i++ {
		m = send(m, "down")
	}
	v := Strip(m.View())
	if !strings.Contains(v, "log      /") {
		t.Errorf("the band did not follow the cursor into the live sections:\n%s", v)
	}
}

func TestUpWrapsBackwards(t *testing.T) {
	resetCursor()
	resetLiveRows()
	defer resetLiveRows()
	m := newModel()
	m.w, m.h = 120, 40
	m = send(m, "up") // from the first Commands row, wrap to the last live row
	last := liveSectionRows(false)
	if len(last) == 0 {
		t.Fatal("no live rows to wrap onto")
	}
	if !markedRowHas(Strip(m.View()), last[len(last)-1].id) {
		t.Errorf("up from the first Commands row did not wrap onto the last live row %q", last[len(last)-1].id)
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

// TestPaletteDispatchRendersATail pins the fixture shape a palette-dispatched
// row must have: the same shape as the hosted docs/test fixtures (hasRing, a
// real tail), not the bare state/id/doing row it used to append. Without it the
// band is dead on the one row a person just dispatched is most likely to be
// looking at.
func TestDispatchRendersATail(t *testing.T) {
	resetLiveRows()
	defer resetLiveRows()

	e := commandEntry{parent: "dev", target: "cf", run: "bun run dev:cf"}
	dispatchCommand(e)

	var got *liveRow
	for i := range liveRows {
		if liveRows[i].cmd == "" && liveRows[i].id == e.resolved() {
			got = &liveRows[i]
		}
	}
	if got == nil {
		t.Fatalf("dispatchCommand did not append a live row for %q", e.resolved())
	}
	if !got.hasRing {
		t.Fatalf("dispatched row %q has hasRing=false", got.id)
	}
	if len(got.tail) == 0 {
		t.Fatalf("dispatched row %q has hasRing=true but an empty tail", got.id)
	}
	out := Strip(strings.Join(tailLines(got.tail, 6, 60), "\n"))
	if !strings.Contains(out, strings.TrimSpace(got.tail[len(got.tail)-1])) {
		t.Errorf("the band did not render the dispatched row's newest tail line:\n%s", out)
	}
}

// TestBandReachesFloor replaces the old bottomOrient test: the two-orientation
// switch is gone, so the invariant is no longer "the stacked band reaches the
// floor" but "the band's last row is real content at every geometry". 117x59 is
// the tall, narrow terminal the old layout wasted ~29 rows on.
func TestBandReachesFloor(t *testing.T) {
	for _, g := range []struct{ w, h int }{{117, 59}, {96, 30}, {200, 50}, {80, 24}} {
		out := Strip(render(State{Values: map[string]string{"screen": "swarm"}, W: g.w, H: g.h}))
		lines := strings.Split(out, "\n")
		if len(lines) != g.h {
			t.Fatalf("%dx%d: got %d lines, want %d", g.w, g.h, len(lines), g.h)
		}
		// lines[len-1] is render's keys footer and lines[len-2] its rule; the
		// row before those is the band's own last output row.
		if last := lines[len(lines)-3]; strings.TrimSpace(last) == "" {
			t.Errorf("%dx%d: band's bottom row is blank, so it did not reach the floor", g.w, g.h)
		}
	}
}

// TestBandDoesNotShift is the invariant the fixed field slot exists for: moving
// the cursor between things in different states must not move any row of the
// band. The eye reads motion as meaning, and a block that grew a row for an
// entry that had actually been run once slid everything below it down and
// claimed a state change that had not happened.
func TestBandDoesNotShift(t *testing.T) {
	if n := len(bandIdle(&commandEntry{parent: "dev", target: "cf", run: "bun run dev:cf"}, 60)); n != fieldRowsH {
		t.Errorf("bandIdle for a never-run entry is %d rows, want exactly %d", n, fieldRowsH)
	}
	ran := commandEntry{}
	for _, e := range commandEntries {
		if e.resolved() == "grafana deploy" {
			ran = e
		}
	}
	if ran.parent == "" {
		t.Fatal("no previously-run entry to test the has-run shape against")
	}
	if n := len(bandIdle(&ran, 60)); n != fieldRowsH {
		t.Errorf("bandIdle for an entry that HAS run is %d rows, want exactly %d", n, fieldRowsH)
	}
	resetLiveRows()
	defer resetLiveRows()
	for i := range liveRows {
		if n := len(bandFields(&liveRows[i], 60)); n != fieldRowsH {
			t.Errorf("bandFields for %q is %d rows, want exactly %d", liveRows[i].id, n, fieldRowsH)
		}
	}

	// And the rule itself lands on the same line whatever the cursor is on.
	want := -1
	for _, screen := range []string{"commands-palette", "running", "swarm"} {
		resetCursor()
		out := Strip(render(State{Values: map[string]string{"screen": screen}, W: 120, H: 40}))
		lines := strings.Split(out, "\n")
		got := -1
		for i, l := range lines {
			if strings.HasPrefix(l, "── ") {
				got = i
				break
			}
		}
		if got < 0 {
			t.Fatalf("%s: no band title rule found", screen)
		}
		if want < 0 {
			want = got
			continue
		}
		if got != want {
			t.Errorf("%s: band title rule on line %d, want %d — the band moved with the cursor", screen, got, want)
		}
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

// markedRowHas reports whether the row carrying the cursor marker also carries
// want. Marker and name are not adjacent on every row kind — a target row has
// its tree connector between them — so proximity, not adjacency, is the rule.
func markedRowHas(view, want string) bool {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "◆") && strings.Contains(line, want) {
			return true
		}
	}
	return false
}
