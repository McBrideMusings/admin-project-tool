package tui

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fixtureFrame seeds the named fixture into a fresh registry and renders one
// headless frame after keys.
func fixtureFrame(t *testing.T, name string, keys ...string) string {
	t.Helper()
	freshLivework(t)
	if err := SeedFrameFixture(name); err != nil {
		t.Fatalf("SeedFrameFixture(%q): %v", name, err)
	}
	dir := t.TempDir()
	writeFixtureManifest(t, dir, "build")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := New(ctx, dir, "owner/repo", "main", "main")
	m.herdr = false
	return stripANSI(Frame(ctx, m, FrameOptions{Width: 96, Height: 25, Keys: keys, Settle: 2 * time.Second}))
}

// TestFrameFixtureSwarmDrawsThreeWorkers: the swarm fixture puts the running,
// waiting and failed workers on the Workers tab.
func TestFrameFixtureSwarmDrawsThreeWorkers(t *testing.T) {
	out := fixtureFrame(t, "swarm", "2")
	for _, want := range []string{"admin-3rgp.36", "running", "admin-3rgp.7", "waiting"} {
		if !strings.Contains(out, want) {
			t.Errorf("swarm frame lacks %q:\n%s", want, out)
		}
	}
}

// TestFrameFixtureHostedAttachesToAPtyWithAPrompt: the hosted fixtures attach
// to the waiting worker, and the output shows the prompt or, with none printed,
// the waiting copy.
func TestFrameFixtureHostedAttachesToAPtyWithAPrompt(t *testing.T) {
	keys := []string{"2", "down", "down", "a"}
	hosted := fixtureFrame(t, "hosted", keys...)
	if !strings.Contains(hosted, "attached") || !strings.Contains(hosted, "what should the root command be called?") {
		t.Errorf("hosted frame is not the attached view over the prompt:\n%s", hosted)
	}
	empty := fixtureFrame(t, "hosted-empty", keys...)
	if !strings.Contains(empty, "waiting for output…") || strings.Contains(empty, "root command be called") {
		t.Errorf("hosted-empty frame is not the attached view with no output:\n%s", empty)
	}
}

func TestSeedFrameFixtureRejectsAnUnknownName(t *testing.T) {
	freshLivework(t)
	if err := SeedFrameFixture("nope"); err == nil {
		t.Error("SeedFrameFixture(\"nope\") = nil, want an error naming the valid fixtures")
	}
}
