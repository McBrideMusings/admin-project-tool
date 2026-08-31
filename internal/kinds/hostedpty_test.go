package kinds

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestHostedReentryArgsUsesTheHostedVerb pins the child's argv: it must name
// "__run-command" (which reaches kinds.RunCommandHosted, never RunAction/
// RunActionRouted) rather than pane.go's "__run-action" — the whole reason a
// long-running Kind dispatched through the hosted pty can't open a further
// herdr pane of its own.
func TestHostedReentryArgsUsesTheHostedVerb(t *testing.T) {
	got := hostedReentryArgs("build", []string{"cf"})
	want := []string{"__run-command", "build", "cf"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hostedReentryArgs(%q, %v) = %v, want %v", "build", []string{"cf"}, got, want)
	}
}

// fixtureAdminScript writes a tiny shell script standing in for the admin
// binary selfExecutable would otherwise resolve to, so StartHostedPTY spawns
// something real on a real pty without depending on this repo's own build.
// It ignores whatever __run-command argv it's handed and just runs body.
func fixtureAdminScript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture-admin")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fixture script: %v", err)
	}
	return path
}

func stubSelfExecutable(t *testing.T, path string) {
	t.Helper()
	old := selfExecutable
	t.Cleanup(func() { selfExecutable = old })
	selfExecutable = func() string { return path }
}

// TestStartHostedPTYRegistersALiveEntry proves a successful dispatch
// registers a PTYID live-work entry keyed on the command's first STEP name
// (matching internal/tui/work.go:liveEntryFor, which looks entries up by
// step name, not command name) with Kill and Retry both wired.
//
// MUTATION EVIDENCE: key the entry on the command name instead of
// cmd.Steps[0] and this test fails — livework.Default.Get("build-step")
// finds nothing.
func TestStartHostedPTYRegistersALiveEntry(t *testing.T) {
	stubSelfExecutable(t, fixtureAdminScript(t, "sleep 0.3\nexit 0"))

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build": {Steps: []string{"build-step"}},
		},
	}
	if err := StartHostedPTY(m, "build", nil, 24, 80); err != nil {
		t.Fatalf("StartHostedPTY: %v", err)
	}
	t.Cleanup(func() {
		if e, ok := livework.Default.Get("build-step"); ok && e.Ops.Kill != nil {
			_ = e.Ops.Kill()
		}
	})

	e, ok := livework.Default.Get("build-step")
	if !ok {
		t.Fatal("no live-work entry registered under the command's first step name")
	}
	if e.PTYID == "" {
		t.Error("entry has no PTYID set")
	}
	if e.PaneID != "" {
		t.Errorf("entry has PaneID %q set, want empty — a hosted dispatch addresses a pty, not a pane", e.PaneID)
	}
	if e.Ops.Kill == nil {
		t.Error("entry missing a Kill op")
	}
	if e.Ops.Retry == nil {
		t.Error("entry missing a Retry op")
	}
	if e.State != livework.StateRunning {
		t.Errorf("entry state = %q, want %q", e.State, livework.StateRunning)
	}
}

// TestStartHostedPTYEntryReportsExited proves the background goroutine
// StartHostedPTY spawns to wait on the child flips the entry to StateExited
// once the process actually exits — the AC's "reports exited when it
// finishes."
func TestStartHostedPTYEntryReportsExited(t *testing.T) {
	stubSelfExecutable(t, fixtureAdminScript(t, "exit 0"))

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"quick": {Steps: []string{"quick-step"}},
		},
	}
	if err := StartHostedPTY(m, "quick", nil, 24, 80); err != nil {
		t.Fatalf("StartHostedPTY: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := livework.Default.Get("quick-step"); ok && e.State == livework.StateExited {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("entry never reported exited")
}

// TestStartHostedPTYUnknownCommandErrors proves an undeclared command
// produces an error before anything is spawned, rather than a pty allocated
// for nothing.
func TestStartHostedPTYUnknownCommandErrors(t *testing.T) {
	m := &manifest.Manifest{}
	if err := StartHostedPTY(m, "missing", nil, 24, 80); err == nil {
		t.Fatal("StartHostedPTY on an undeclared command returned nil error, want one")
	}
}
