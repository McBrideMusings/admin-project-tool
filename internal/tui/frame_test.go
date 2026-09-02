package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/lipgloss"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestFrameRendersExactRequestedSize pins the property that makes a dumped frame
// worth diffing: it is the same bounded frame the alt-screen draws, so two runs at
// the same size differ only where the dashboard differs. A frame that came back a
// line short or a column wide would make every comparison noise.
func TestFrameRendersExactRequestedSize(t *testing.T) {
	// A repo root that does not exist: every git load fails fast, so this measures
	// the frame without waiting on real git or depending on this checkout's state.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, t.TempDir(), "owner/repo", "main", "main")
	m.herdr = false

	const w, h = 100, 24
	out := Frame(ctx, m, FrameOptions{Width: w, Height: h, Settle: 200 * time.Millisecond})

	lines := strings.Split(out, "\n")
	if len(lines) != h {
		t.Errorf("frame has %d lines, want exactly %d", len(lines), h)
	}
	for i, l := range lines {
		if got := lipgloss.Width(l); got > w {
			t.Errorf("line %d is %d columns wide, want at most %d: %q", i, got, w, l)
		}
	}
	if !strings.Contains(lines[0], "owner/repo") {
		t.Errorf("header not on the first line: %q", lines[0])
	}
}

// TestFrameKeysDriveTheModel pins that --keys reaches Update: without it a dump
// can only ever show the first pane of the first tab, which is the half of the
// dashboard that was never hard to check.
func TestFrameKeysDriveTheModel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, t.TempDir(), "owner/repo", "main", "main")
	m.herdr = false
	// tracker = ["github"] declared, uses_pull_requests left undeclared
	// (defaults false) — the second drawn tab is Issues, with no PRs tab
	// ahead of it.
	m.manifest = &manifest.Manifest{Trackers: []string{"github"}, HasTracker: true}

	opts := FrameOptions{Width: 100, Height: 24, Settle: 200 * time.Millisecond}
	before := Frame(ctx, m, opts)

	opts.Keys = []string{"right"}
	after := Frame(ctx, m, opts)

	if before == after {
		t.Fatal("pressing right produced an identical frame; keys never reached Update")
	}
	// tracker = "github" with uses_pull_requests undeclared draws no PRs tab, so
	// the second drawn tab is Issues — the tab bar names it, and the Branches
	// pane the Repo tab draws is gone, which is what proves the key moved the
	// tab rather than repainting.
	if !strings.Contains(strings.Split(after, "\n")[1], "Issues") {
		t.Errorf("expected the Issues tab after right; tab bar was %q", strings.Split(after, "\n")[1])
	}
	if strings.Contains(after, "─ Branches ") {
		t.Error("right left the Repo tab's Branches pane on screen; the tab never changed")
	}
}

// TestFrameRendersBothTrackerTabsTogether is admin-j00d's headless proof
// that tracker = ["beads", "github"] draws both the Beads and Issues tabs at
// once — the acceptance criterion the query classifier and check-time tests
// can't reach on their own, since drawing the tab bar is internal/tui's job.
func TestFrameRendersBothTrackerTabsTogether(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, t.TempDir(), "owner/repo", "main", "main")
	m.herdr = false
	m.manifest = &manifest.Manifest{Trackers: []string{"beads", "github"}, HasTracker: true}

	out := Frame(ctx, m, FrameOptions{Width: 100, Height: 24, Settle: 200 * time.Millisecond})
	tabBar := strings.Split(out, "\n")[1]
	if !strings.Contains(tabBar, "Beads") {
		t.Errorf("expected the Beads tab in the tab bar, got %q", tabBar)
	}
	if !strings.Contains(tabBar, "Issues") {
		t.Errorf("expected the Issues tab in the tab bar, got %q", tabBar)
	}
}

func TestKeyMsgNamesTheSpecialKeys(t *testing.T) {
	for name, want := range map[string]tea.KeyType{
		"tab": tea.KeyTab, "shift+tab": tea.KeyShiftTab, "enter": tea.KeyEnter,
		"esc": tea.KeyEsc, "space": tea.KeySpace, "up": tea.KeyUp,
		"down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
	} {
		if got := keyMsg(name).Type; got != want {
			t.Errorf("keyMsg(%q).Type = %v, want %v", name, got, want)
		}
	}
	// Anything else is its literal runes, so a plain letter binding needs no entry.
	k := keyMsg("x")
	if k.Type != tea.KeyRunes || string(k.Runes) != "x" {
		t.Errorf("keyMsg(\"x\") = %+v, want KeyRunes 'x'", k)
	}
}
