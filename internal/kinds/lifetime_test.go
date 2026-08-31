// lifetime_test.go covers admin-3rgp.17: the Lifetime catalog attribute and
// RunActionRouted's routing between the herdr-pane path and today's inline
// behaviour. The no-herdr fallback is tested first and hardest, per the
// ticket's own instruction — it's the default path in practice.
package kinds

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestClassifyEveryKindHasALifetime proves every declared kind resolves to
// exactly one of Transient/LongRunning (Lifetime's zero value already
// guarantees this in Go, but the point of this test is documenting and
// pinning WHICH kinds landed on which side, per the ticket's "classify
// every kind currently in the catalog" instruction).
func TestClassifyEveryKindHasALifetime(t *testing.T) {
	wantLongRunning := map[string]bool{
		"docker-dev":         true,
		"docker-reload-loop": true,
		"vite-dev":           true,
		"server-reload":      true,
		"apple-dev-loop":     true,
		"bridge-listen":      true,
	}
	for name, k := range Kinds {
		got := k.Lifetime == LongRunning
		want := wantLongRunning[name]
		if got != want {
			t.Errorf("kind %q: Lifetime = %v, want LongRunning=%v", name, k.Lifetime, want)
		}
	}
}

// TestInteractiveShellStaysTransient pins down the one case explicitly
// called out in catalog.go's reasoning: interactive-shell reads the user's
// own keystrokes and must never be routed into a detached pane, even though
// it shares bridge-listen's exact module list and runs just as long.
func TestInteractiveShellStaysTransient(t *testing.T) {
	k, ok := Declared("interactive-shell")
	if !ok {
		t.Fatal("interactive-shell should be declared")
	}
	if k.Lifetime != Transient {
		t.Error("interactive-shell must stay Transient — it reads the user's keystrokes directly")
	}
}

// TestLongRunningKindsDerivedView proves LongRunningKinds is computed from
// Kinds (sorted, matching Kinds' LongRunning entries) rather than a second
// hand-maintained list that could drift.
func TestLongRunningKindsDerivedView(t *testing.T) {
	if !isSortedStrings(LongRunningKinds) {
		t.Errorf("LongRunningKinds = %v, want sorted", LongRunningKinds)
	}
	for _, name := range LongRunningKinds {
		k, ok := Kinds[name]
		if !ok || k.Lifetime != LongRunning {
			t.Errorf("LongRunningKinds contains %q which is not LongRunning in Kinds", name)
		}
	}
	for name, k := range Kinds {
		if k.Lifetime != LongRunning {
			continue
		}
		found := false
		for _, n := range LongRunningKinds {
			if n == name {
				found = true
			}
		}
		if !found {
			t.Errorf("kind %q is LongRunning but missing from LongRunningKinds", name)
		}
	}
}

func isSortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}

// TestActionLifetimeDefaultsToShellIsTransient proves an action with no
// `kind` key (defaulting to "shell", per RunAction's own default) routes as
// Transient.
func TestActionLifetimeDefaultsToShellIsTransient(t *testing.T) {
	if got := actionLifetime(manifest.Action{"run": "true"}); got != Transient {
		t.Errorf("actionLifetime(no kind) = %v, want Transient", got)
	}
}

// TestActionLifetimeUndeclaredKindIsTransient proves an action naming a
// kind absent from the catalog routes as Transient rather than panicking or
// erroring — RunAction is the one place "unknown kind" is a real error;
// this routing check must never be it.
func TestActionLifetimeUndeclaredKindIsTransient(t *testing.T) {
	if got := actionLifetime(manifest.Action{"kind": "not-a-real-kind", "run": "true"}); got != Transient {
		t.Errorf("actionLifetime(undeclared kind) = %v, want Transient", got)
	}
}

// TestRunActionRoutedNoHerdrRunsInline is the ticket's own "test the
// no-Herdr path first and hardest" instruction: with HERDR_ENV unset (the
// default — no herdr binary needed on the test machine either, since
// shim.Available() short-circuits on the env check before ever looking for
// the CLI), a LongRunning-kind action must run exactly like a Transient one
// — inline, in this process, producing its real side effect — and must NOT
// appear in the live-work registry.
//
// MUTATION EVIDENCE: reported separately (see the worker's mutation-evidence
// output) — breaking RunActionRouted's fallback branch makes this test fail.
func TestRunActionRoutedNoHerdrRunsInline(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	os.Unsetenv("HERDR_ENV")

	before := len(livework.Default.List())

	r := NewRegistry()
	// Register a stub factory under a real LongRunning catalog kind so the
	// routing decision is genuine (vite-dev really is LongRunning) while
	// the actual work stays a trivial, fast side effect a test can assert
	// on directly, instead of needing a real dev server.
	r.MustRegister("vite-dev", func(name string, action manifest.Action, m *manifest.Manifest) (Runner, error) {
		return func(args []string) error {
			return os.WriteFile(filepath.Join(dir, "ran.txt"), []byte("inline\n"), 0o644)
		}, nil
	})

	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"dev-server": {"kind": "vite-dev"},
		},
	}
	if err := RunActionRouted(m, r, "dev-server", nil); err != nil {
		t.Fatalf("RunActionRouted = %v, want nil (inline fallback)", err)
	}
	assertFileExists(t, dir, "ran.txt")

	after := livework.Default.List()
	if len(after) != before {
		t.Errorf("live-work registry grew from %d to %d entries — a LongRunning kind with no "+
			"herdr available must fall back to inline and never register itself", before, len(after))
	}
}

// TestRunActionRoutedTransientNeverRegisters proves the AC's other half: a
// Transient kind (shell) never touches the live-work registry, herdr
// available or not.
func TestRunActionRoutedTransientNeverRegisters(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	os.Unsetenv("HERDR_ENV")

	before := len(livework.Default.List())

	r := NewRegistry()
	r.MustRegister("shell", makeShell)
	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"noop": {"kind": "shell", "run": "touch shell-ran.txt"},
		},
	}
	if err := RunActionRouted(m, r, "noop", nil); err != nil {
		t.Fatalf("RunActionRouted = %v, want nil", err)
	}
	assertFileExists(t, dir, "shell-ran.txt")

	if got := len(livework.Default.List()); got != before {
		t.Errorf("live-work registry grew from %d to %d for a Transient kind", before, got)
	}
}

// TestRunActionRoutedTransientIgnoresHerdrEvenWhenAvailable proves the
// lifetime gate itself — not shim.Available() — is what keeps a Transient
// kind from ever reaching the pane-dispatch path: with a working fake herdr
// on PATH and HERDR_ENV=1, a shell action must still run inline and must
// never call `herdr pane split`.
func TestRunActionRoutedTransientIgnoresHerdrEvenWhenAvailable(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "herdr-calls.log")
	withFakeHerdr(t, "w1:p9", logPath)

	before := len(livework.Default.List())

	r := NewRegistry()
	r.MustRegister("shell", makeShell)
	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"noop": {"kind": "shell", "run": "touch shell-ran.txt"},
		},
	}
	if err := RunActionRouted(m, r, "noop", nil); err != nil {
		t.Fatalf("RunActionRouted = %v, want nil", err)
	}
	assertFileExists(t, dir, "shell-ran.txt")

	if got := len(livework.Default.List()); got != before {
		t.Errorf("live-work registry grew from %d to %d for a Transient kind with herdr available", before, got)
	}
	if logged, err := os.ReadFile(logPath); err == nil && len(logged) > 0 {
		t.Errorf("herdr log = %q, want empty — a Transient kind must never call herdr at all", string(logged))
	}
}

// withFakeHerdr puts a fake `herdr` script on PATH for the duration of the
// test, restoring the original PATH after. The script logs every
// invocation (one line per call, space-joined argv) to logPath and answers
// `pane split` with a fixed pane_id JSON payload and `pane run` with a
// clean exit — enough for shim.SplitPane/RunInPane to succeed without a
// real herdr installation.
func withFakeHerdr(t *testing.T, paneID, logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake herdr script is a POSIX shell script")
	}
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %s
case "$1 $2" in
  "pane split")
    echo '{"result":{"pane":{"pane_id":"%s"}}}'
    ;;
  "pane run")
    exit 0
    ;;
  *)
    exit 1
    ;;
esac
`, shellQuote(logPath), paneID)
	path := filepath.Join(dir, "herdr")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_ENV", "1")
}

// TestRunActionRoutedDispatchesLongRunningToPane exercises the herdr-
// available path end to end against a fake herdr CLI: a LongRunning kind's
// action must NOT run inline (no in-process side effect), must appear in
// the live-work registry with the pane ID herdr reported, and the command
// typed into that pane must be the hidden re-entry verb naming the same
// action.
func TestRunActionRoutedDispatchesLongRunningToPane(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "herdr-calls.log")
	withFakeHerdr(t, "w1:p9", logPath)

	inlineRan := false
	r := NewRegistry()
	r.MustRegister("vite-dev", func(name string, action manifest.Action, m *manifest.Manifest) (Runner, error) {
		return func(args []string) error {
			inlineRan = true
			return nil
		}, nil
	})

	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"dev-server": {"kind": "vite-dev"},
		},
	}
	if err := RunActionRouted(m, r, "dev-server", nil); err != nil {
		t.Fatalf("RunActionRouted = %v, want nil", err)
	}
	if inlineRan {
		t.Error("a LongRunning kind with herdr available must NOT run its Runner inline")
	}

	entries := livework.Default.List()
	found := false
	for _, e := range entries {
		if e.Name == "dev-server" && e.PaneID == "w1:p9" && e.Kind == "vite-dev" {
			found = true
		}
	}
	if !found {
		t.Errorf("live-work registry = %+v, want an entry for dev-server with pane w1:p9", entries)
	}

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logged)
	if !strings.Contains(log, "pane split") {
		t.Errorf("herdr log = %q, want a `pane split` call", log)
	}
	if !strings.Contains(log, "__run-action dev-server") {
		t.Errorf("herdr log = %q, want `pane run` to type the __run-action re-entry command", log)
	}
}
