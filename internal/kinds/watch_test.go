package kinds

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// withFakeGh puts a fake `gh` script on PATH ahead of the real one for the
// duration of t: `gh run watch <id>` appends "watch <id>" to a marker file
// under dir and exits 0. This is the same shape TestShellPTY* uses for a
// stand-in binary — a POSIX shell script on a prepended PATH — rather than
// mocking exec.Command, since watch.go shells out via a bare "gh" lookup.
func withFakeGh(t *testing.T, dir string) (markerPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gh script is POSIX shell")
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	marker := filepath.Join(dir, "gh-calls.txt")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\nexit 0\n", shellQuote(marker))
	ghPath := filepath.Join(bin, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

// TestShellWatchTriggersRegisteredWatcher proves `watch = "github-action"`
// under kind="shell" actually fires: a run string that prints a GitHub
// Actions run URL to stdout results in the built-in watcher shelling out to
// `gh run watch <id>` with the extracted run ID.
func TestShellWatchTriggersRegisteredWatcher(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	marker := withFakeGh(t, dir)

	runner, err := makeShell("deploy", manifest.Action{
		"run":   `echo "see https://github.com/acme/repo/actions/runs/4242 for details"`,
		"watch": "github-action",
	}, nil, nil)
	if err != nil {
		t.Fatalf("makeShell: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}

	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("watcher never invoked gh: %v", err)
	}
	if !strings.Contains(string(got), "run watch 4242") {
		t.Fatalf("gh was called with %q, want it to contain %q", got, "run watch 4242")
	}
}

// TestShellWatchUnknownKindWarns proves an unregistered `watch` kind warns
// rather than failing the action — the action's own command already
// succeeded, and a bad watch value in the manifest must not turn a
// successful run into a failed one.
func TestShellWatchUnknownKindWarns(t *testing.T) {
	t.Chdir(t.TempDir())

	runner, err := makeShell("deploy", manifest.Action{
		"run":   "true",
		"watch": "no-such-watcher",
	}, nil, nil)
	if err != nil {
		t.Fatalf("makeShell: %v", err)
	}

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = runner(nil)
	})
	if runErr != nil {
		t.Fatalf("runner() = %v, want nil (an unknown watch kind must not fail the action)", runErr)
	}
	if !strings.Contains(stderr, "no watcher registered for kind 'no-such-watcher'") {
		t.Fatalf("stderr = %q, want it to name the unregistered kind", stderr)
	}
}

// TestShellWatchSkippedOnFailure proves watch is never consulted when the
// action's own command fails: the watcher must not run (and must not shell
// out to gh) for a non-zero exit — makeShell only calls runWatcher after
// rc == 0.
func TestShellWatchSkippedOnFailure(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	marker := withFakeGh(t, dir)

	runner, err := makeShell("deploy", manifest.Action{
		"run":   `echo "https://github.com/acme/repo/actions/runs/9999"; exit 1`,
		"watch": "github-action",
	}, nil, nil)
	if err != nil {
		t.Fatalf("makeShell: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() = nil, want an error for a nonzero exit")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("gh was invoked even though the action failed")
	}
}
