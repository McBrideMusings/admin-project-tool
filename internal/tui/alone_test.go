package tui

import (
	"context"
	"strings"
	"testing"
)

// TestMountedAloneDrawsNoTabChrome pins the verbs' contract: opened alone, the
// dashboard has the body in focus, draws no tab bar, advertises no tab-switch
// key, and no key moves it to another tab.
func TestMountedAloneDrawsNoTabChrome(t *testing.T) {
	m := New(context.Background(), t.TempDir(), "", "main", "main", WithMountedAlone("repo"))
	m.width, m.height, m.headless = 100, 30, true

	if m.active != tabLocal || m.focus != focusBody {
		t.Fatalf("active=%v focus=%v, want the repo tab with focus on the body", m.active, m.focus)
	}
	if !m.MountedDrawn() {
		t.Fatal("the repo tab is always drawn, MountedDrawn = false")
	}
	view := m.View()
	for _, banned := range []string{"←→", "jump", "Config", "Beads"} {
		if strings.Contains(view, banned) {
			t.Errorf("frame mentions %q:\n%s", banned, view)
		}
	}
	for _, key := range []string{"right", "left", "h", "l", "3", "up", "k", "esc"} {
		next, _ := m.Update(keyMsg(key))
		m = next.(Model)
		if m.active != tabLocal || m.focus != focusBody {
			t.Errorf("after %q: active=%v focus=%v, want the repo tab with focus on the body", key, m.active, m.focus)
		}
	}
}

// TestMountedAloneUndrawnTab: a project without the tab reports it undrawn, so
// the verb can print "this project has no prs tab" instead of an empty shell.
func TestMountedAloneUndrawnTab(t *testing.T) {
	for _, verb := range []string{"prs", "issues", "beads", "workers", "cmd"} {
		m := New(context.Background(), t.TempDir(), "", "main", "main", WithMountedAlone(verb))
		if m.MountedDrawn() {
			t.Errorf("%s drawn in a bare directory with no manifest, remote or tracker", verb)
		}
	}
	for _, verb := range []string{"repo", "config"} {
		m := New(context.Background(), t.TempDir(), "", "main", "main", WithMountedAlone(verb))
		if !m.MountedDrawn() {
			t.Errorf("%s not drawn, but it is unconditional", verb)
		}
	}
}
