package kinds

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestHostedReentryArgsUsesTheHostedVerb pins the child's argv: it must name
// "__run-command" (which reaches kinds.RunCommand with a nil producer)
// rather than pane.go's "__run-action" — the whole reason a long-running
// Kind dispatched through the hosted pty can't open a further herdr pane of
// its own.
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
// registers a hosted-pty (Via empty) live-work entry keyed on the command's
// first STEP name
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
	if err := StartHostedPTY(m, "build", nil, 24, 80, nil); err != nil {
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
	if e.Handle == "" {
		t.Error("entry has no Handle set")
	}
	if e.Via != "" {
		t.Errorf("entry has Via %q set, want empty — a hosted dispatch addresses a pty, not a launcher", e.Via)
	}
	if e.Ops.Kill == nil {
		t.Error("entry missing a Kill op")
	}
	if e.Ops.Retry == nil {
		t.Error("entry missing a Retry op")
	}
	if state, _ := livework.Default.State("build-step"); state != livework.StateRunning {
		t.Errorf("entry state = %q, want %q", state, livework.StateRunning)
	}
}

// TestStartHostedPTYEntryReportsExited is the regression seam for Cause A
// (diagnosed 2026-08-31): nothing reading the pty master left the child
// blocked forever at exit on macOS, so the entry never reached StateExited.
// Unlike an "exit 0" fixture (which writes nothing and would pass even with
// no drain at all — that is exactly how the previous round of this test
// went green while the real dispatch hung), this fixture WRITES a line
// before exiting, so a build with no drain reproduces the hang and this
// test times out instead of passing by accident.
//
// It also asserts the entry's Ring actually captured those written bytes
// (proving the drain, not just the exit, happened) and that the child's env
// carries the parent-resolved color profile (Cause B's fix).
func TestStartHostedPTYEntryReportsExited(t *testing.T) {
	envProbe := filepath.Join(t.TempDir(), "env.out")
	stubSelfExecutable(t, fixtureAdminScript(t,
		"echo hi from hosted pty\n"+
			"env | grep '^ADMIN_HOSTED_' > "+envProbe+"\n"+
			"exit 0",
	))

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"quick": {Steps: []string{"quick-step"}},
		},
	}
	if err := StartHostedPTY(m, "quick", nil, 24, 80, nil); err != nil {
		t.Fatalf("StartHostedPTY: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := livework.Default.Get("quick-step"); ok {
			if state, _ := livework.Default.State("quick-step"); state != livework.StateExited {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if e.Output == nil {
				t.Fatal("entry has no Output ring — the drain never wired one up")
			}
			if got := string(e.Output.Bytes()); !strings.Contains(got, "hi from hosted pty") {
				t.Errorf("entry.Output.Bytes() = %q, want it to contain the child's written output", got)
			}

			envOut, err := os.ReadFile(envProbe)
			if err != nil {
				t.Fatalf("reading env probe: %v", err)
			}
			if !strings.Contains(string(envOut), HostedColorProfileEnv+"=") {
				t.Errorf("child env missing %s: %q", HostedColorProfileEnv, envOut)
			}
			if !strings.Contains(string(envOut), HostedDarkBackgroundEnv+"=") {
				t.Errorf("child env missing %s: %q", HostedDarkBackgroundEnv, envOut)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("entry never reported exited — this is Cause A (no master drain) if it reproduces")
}

// TestStartHostedPTYWiresWriteAndResize proves the registered entry's
// Ops.Write reaches the real child (the terminal view's, admin-3rgp.65,
// only way to answer a prompting process) and Ops.Resize succeeds without
// error while the child is alive, then both refuse — rather than
// panicking on a closed *os.File — once the child has exited and its pty
// master is closed.
func TestStartHostedPTYWiresWriteAndResize(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "typed.out")
	stubSelfExecutable(t, fixtureAdminScript(t, "cat > "+outFile+"\n"))

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"echoer": {Steps: []string{"echoer-step"}},
		},
	}
	if err := StartHostedPTY(m, "echoer", nil, 24, 80, nil); err != nil {
		t.Fatalf("StartHostedPTY: %v", err)
	}

	e, ok := livework.Default.Get("echoer-step")
	if !ok {
		t.Fatal("no live-work entry registered")
	}
	if e.Ops.Write == nil {
		t.Fatal("entry missing a Write op")
	}
	if e.Ops.Resize == nil {
		t.Fatal("entry missing a Resize op")
	}

	if err := e.Ops.Resize(30, 100); err != nil {
		t.Errorf("Resize on a live child: %v", err)
	}
	if _, err := e.Ops.Write([]byte("hello from the terminal view\n")); err != nil {
		t.Fatalf("Write on a live child: %v", err)
	}

	if err := e.Ops.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok := livework.Default.Get("echoer-step"); ok && got.State == livework.StateExited {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, err := e.Ops.Write([]byte("too late")); err == nil {
		t.Error("Write after Kill closed the pty = nil error, want one")
	}
	if err := e.Ops.Resize(24, 80); err == nil {
		t.Error("Resize after Kill closed the pty = nil error, want one")
	}
}

// TestStartHostedPTYUnknownCommandErrors proves an undeclared command
// produces an error before anything is spawned, rather than a pty allocated
// for nothing.
func TestStartHostedPTYUnknownCommandErrors(t *testing.T) {
	m := &manifest.Manifest{}
	if err := StartHostedPTY(m, "missing", nil, 24, 80, nil); err == nil {
		t.Fatal("StartHostedPTY on an undeclared command returned nil error, want one")
	}
}

// TestHostedPTYKillTerminatesTheWholeProcessGroup is the criterion the slice
// exists for: a hosted Command routinely spawns children of its own (a
// bundler, a container, a simulator), so Kill must signal the whole process
// group, not only the direct child. The fixture backgrounds a grandchild
// (plain `sleep 300 &` under sh with no job control leaves it in the same
// process group as the direct child) and writes its pid to a file; Kill is
// asserted to reach that grandchild too.
//
// MUTATION EVIDENCE: revert Ops.Kill to only `c.Process.Kill()` and this test
// fails — the grandchild is orphaned and keeps running after Kill returns.
func TestHostedPTYKillTerminatesTheWholeProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	stubSelfExecutable(t, fixtureAdminScript(t,
		"sleep 300 &\n"+
			"echo $! > "+pidFile+"\n"+
			"wait",
	))

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"host-and-spawn": {Steps: []string{"host-and-spawn-step"}},
		},
	}
	if err := StartHostedPTY(m, "host-and-spawn", nil, 24, 80, nil); err != nil {
		t.Fatalf("StartHostedPTY: %v", err)
	}

	var grandchildPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if err == nil && len(strings.TrimSpace(string(raw))) > 0 {
			pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if perr == nil {
				grandchildPID = pid
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if grandchildPID == 0 {
		t.Fatal("grandchild never wrote its pid — fixture did not start in time")
	}
	if err := syscall.Kill(grandchildPID, 0); err != nil {
		t.Fatalf("grandchild pid %d not alive before Kill: %v", grandchildPID, err)
	}

	e, ok := livework.Default.Get("host-and-spawn-step")
	if !ok {
		t.Fatal("no live-work entry registered")
	}
	if err := e.Ops.Kill(); err != nil {
		t.Fatalf("Ops.Kill() = %v, want nil", err)
	}

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(grandchildPID, 0); err != nil {
			// ESRCH (or any error) means the process is gone.
			if state, ok := livework.Default.State("host-and-spawn-step"); ok && state != livework.StateExited {
				t.Errorf("entry state = %q, want %q", state, livework.StateExited)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild pid %d still alive after Kill — process group was not signalled", grandchildPID)
}

// TestHostedPTYKillIsIdempotent covers both no-op cases the AC requires:
// killing an entry that already exited on its own, and killing an entry
// twice. Neither may return an error, and neither may clobber the state the
// wait goroutine already recorded.
func TestHostedPTYKillIsIdempotent(t *testing.T) {
	t.Run("already exited naturally", func(t *testing.T) {
		stubSelfExecutable(t, fixtureAdminScript(t, "exit 0"))

		m := &manifest.Manifest{
			Custom: map[string]manifest.Command{
				"finishes-fast": {Steps: []string{"finishes-fast-step"}},
			},
		}
		if err := StartHostedPTY(m, "finishes-fast", nil, 24, 80, nil); err != nil {
			t.Fatalf("StartHostedPTY: %v", err)
		}

		deadline := time.Now().Add(5 * time.Second)
		var e *livework.Entry
		for time.Now().Before(deadline) {
			if got, ok := livework.Default.Get("finishes-fast-step"); ok {
				if state, _ := livework.Default.State("finishes-fast-step"); state == livework.StateExited {
					e = got
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		if e == nil {
			t.Fatal("entry never reported exited")
		}

		if err := e.Ops.Kill(); err != nil {
			t.Fatalf("Ops.Kill() on an already-exited entry = %v, want nil", err)
		}
		if state, ok := livework.Default.State("finishes-fast-step"); !ok || state != livework.StateExited {
			t.Errorf("state after Kill on exited entry = %v, want %q", state, livework.StateExited)
		}
	})

	t.Run("killed twice", func(t *testing.T) {
		stubSelfExecutable(t, fixtureAdminScript(t, "sleep 300"))

		m := &manifest.Manifest{
			Custom: map[string]manifest.Command{
				"long-running": {Steps: []string{"long-running-step"}},
			},
		}
		if err := StartHostedPTY(m, "long-running", nil, 24, 80, nil); err != nil {
			t.Fatalf("StartHostedPTY: %v", err)
		}

		e, ok := livework.Default.Get("long-running-step")
		if !ok {
			t.Fatal("no live-work entry registered")
		}
		if err := e.Ops.Kill(); err != nil {
			t.Fatalf("first Ops.Kill() = %v, want nil", err)
		}
		if err := e.Ops.Kill(); err != nil {
			t.Fatalf("second Ops.Kill() = %v, want nil", err)
		}
		if state, ok := livework.Default.State("long-running-step"); !ok || state != livework.StateExited {
			t.Errorf("state after double Kill = %v, want %q", state, livework.StateExited)
		}
	})
}
